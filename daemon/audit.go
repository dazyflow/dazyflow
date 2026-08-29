// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/internal/pgstore"
)

const defaultAuditLimit = 100

// ---- in-memory backend ----------------------------------------------

// MemAuditLog is an in-process audit trail for single-binary / dev runs.
// Concurrency-safe; lost on restart (use PgAuditLog for durability).
type MemAuditLog struct {
	mu     sync.Mutex
	events []core.AuditEvent
}

func NewMemAuditLog() *MemAuditLog { return &MemAuditLog{} }

func (m *MemAuditLog) Append(_ context.Context, e core.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *MemAuditLog) List(_ context.Context, q core.AuditQuery) ([]core.AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]core.AuditEvent, 0)
	for _, e := range m.events {
		if e.Tenant == q.Tenant {
			out = append(out, e)
		}
	}
	// Newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return paginate(out, q.Limit, q.Offset), nil
}

func paginate(events []core.AuditEvent, limit, offset int) []core.AuditEvent {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(events) {
		return []core.AuditEvent{}
	}
	end := offset + limit
	if end > len(events) {
		end = len(events)
	}
	return events[offset:end]
}

// ---- Postgres backend -----------------------------------------------

const pgAuditSchema = `
CREATE TABLE IF NOT EXISTS audit_events (
    id      BIGSERIAL PRIMARY KEY,
    ts      TIMESTAMPTZ NOT NULL,
    tenant  TEXT NOT NULL,
    actor   TEXT NOT NULL,
    action  TEXT NOT NULL,
    target  TEXT NOT NULL,
    detail  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS audit_events_tenant_id ON audit_events (tenant, id DESC);
`

// PgAuditLog persists the audit trail to Postgres (durable, multi-node).
type PgAuditLog struct {
	pool *pgxpool.Pool
}

func NewPgAuditLog(ctx context.Context, pool *pgxpool.Pool) (*PgAuditLog, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgAuditSchema); err != nil {
		return nil, err
	}
	return &PgAuditLog{pool: pool}, nil
}

// Prune deletes audit rows older than the cutoff in bounded batches so a
// large backlog doesn't lock the table in one statement. Returns the
// total deleted. olderThan <= 0 is a no-op (retention disabled).
//
// AUTHORISATIONS ARE EXEMPT. Retention exists to stop routine chatter — key
// reads, config edits, sign-ins — accumulating forever, and for that a window
// is right. An approval is a different kind of record: it is the answer to
// "who authorised this", and that question is characteristically asked long
// after the fact, during an incident review or when someone asks who signed
// off on the change that broke something. Pruning it puts an expiry on the
// one entry nobody wants expired: at the Pro tier's 90 days the record of a
// production deploy is gone within a quarter, and on Free within a week.
//
// Keeping them costs nothing worth counting — an approval is a deliberate
// human act, so the volume is bounded by how often people click, not by
// traffic. And it does not undercut erasure: a user erasure pseudonymises the
// actor on these rows rather than deleting them (see gdpr_coverage_test), so
// the authorisation survives while the person stops being identifiable, which
// is the outcome both rules actually want.
func (p *PgAuditLog) Prune(ctx context.Context, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	cutoff := time.Now().Add(-olderThan)
	total := 0
	for {
		tag, err := p.pool.Exec(ctx,
			`DELETE FROM audit_events WHERE id IN (
			     SELECT id FROM audit_events
			      WHERE ts < $1 AND action <> 'approval' LIMIT $2)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}
}

// AnonymizeActor pseudonymises a data subject in the audit trail: it
// replaces their actor identifier with a fixed marker and blanks the
// free-text detail (which can carry the client IP on auth events). The
// action/target/tenant/timestamp survive, so the security trail stays
// intact without retaining personal data — the GDPR-preferred treatment
// for logs kept under a legal-obligation/legitimate-interest basis
// (Art. 17(3), Recital 26). Returns the number of rows affected.
func (p *PgAuditLog) AnonymizeActor(ctx context.Context, actor string) (int, error) {
	tag, err := p.pool.Exec(ctx,
		`UPDATE audit_events SET actor = $2, detail = '' WHERE actor = $1`, actor, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeleteByTenant hard-deletes a tenant's whole audit trail — used when an
// entire org is deleted (no security trail to preserve for a gone tenant).
func (p *PgAuditLog) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := p.pool.Exec(ctx, `DELETE FROM audit_events WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (p *PgAuditLog) Append(ctx context.Context, e core.AuditEvent) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO audit_events (ts, tenant, actor, action, target, detail) VALUES ($1,$2,$3,$4,$5,$6)`,
		e.Time, e.Tenant, e.Actor, e.Action, e.Target, e.Detail)
	return err
}

func (p *PgAuditLog) List(ctx context.Context, q core.AuditQuery) ([]core.AuditEvent, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	// $4 = "" means "any actor", so one statement serves both the admin trail
	// and the per-subject export.
	rows, err := p.pool.Query(ctx,
		`SELECT ts, tenant, actor, action, target, detail FROM audit_events
		  WHERE tenant=$1 AND ($4 = '' OR actor = $4)
		  ORDER BY id DESC LIMIT $2 OFFSET $3`, q.Tenant, limit, offset, q.Actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.AuditEvent, 0)
	for rows.Next() {
		var e core.AuditEvent
		if err := rows.Scan(&e.Time, &e.Tenant, &e.Actor, &e.Action, &e.Target, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- gateway integration --------------------------------------------

// audit records an administrative action. Best-effort: a write failure is
// logged but never fails the user action being audited, and a nil Audit
// store (auditing disabled) is a no-op.
// auditFieldLimit caps a single audit field. A caller-supplied value (a tried
// email, a flow id) shouldn't be able to bloat the trail.
const auditFieldLimit = 512

// sanitizeAuditField strips control characters from a value destined for the
// audit trail and caps its length.
//
// The failed-sign-in path records the email the caller TYPED — it has to, that
// address is the whole signal for credential-stuffing detection — and at that
// point nothing has validated it. A newline in that value forges a second,
// fake line in a compliance-relevant log, which is exactly the log-injection
// A.8.15 asks us to prevent. Sanitizing at the sink covers every caller
// rather than relying on each one to remember.
func sanitizeAuditField(v string) string {
	if v == "" {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	for _, r := range v {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// Other C0 controls and DEL: drop entirely.
		default:
			b.WriteRune(r)
		}
		if b.Len() >= auditFieldLimit {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > auditFieldLimit {
		out = out[:auditFieldLimit]
	}
	return out
}

func (h *HTTPGateway) audit(ctx context.Context, p core.Principal, action, target, detail string) {
	if h.Audit == nil {
		return
	}
	if err := h.Audit.Append(ctx, core.AuditEvent{
		Time:   time.Now(),
		Tenant: p.Tenant,
		Actor:  sanitizeAuditField(p.Subject),
		Action: action,
		Target: sanitizeAuditField(target),
		Detail: sanitizeAuditField(detail),
	}); err != nil {
		log.Printf("audit append (%s %s): %v", action, target, err)
	}
}

// auditAuth records an authentication-lifecycle event — sign-in, sign-out,
// signup, and the MFA legs (ISO/IEC 27001:2022 A.8.15/A.8.16: detection of
// anomalous sign-in activity such as credential stuffing).
//
// It differs from audit() in two ways. First, the actor is an email rather
// than a resolved principal — a *failed* sign-in has no principal yet, only
// the address that was tried. Second, the caller's source IP is appended to
// Detail so a burst of failures from one IP is visible in the trail.
//
// Tenant is best-effort: it's the user's tenant on a successful sign-in, but
// empty on a pre-auth failure — we don't resolve (and so don't reveal)
// whether the attempted email maps to a tenant. Failed-login events
// therefore land in the platform-level trail (a platform admin querying
// ?tenant=) rather than a specific tenant's view. Best-effort like audit():
// a write failure is logged, never blocking the auth path; a nil store is a
// no-op.
func (h *HTTPGateway) auditAuth(ctx context.Context, r *http.Request, tenant, actor, action, detail string) {
	if h.Audit == nil {
		return
	}
	ipNote := "ip=" + clientIP(r)
	if detail == "" {
		detail = ipNote
	} else {
		detail += " " + ipNote
	}
	if err := h.Audit.Append(ctx, core.AuditEvent{
		Time:   time.Now(),
		Tenant: sanitizeAuditField(tenant),
		Actor:  sanitizeAuditField(actor),
		Action: action,
		Target: sanitizeAuditField(actor),
		Detail: sanitizeAuditField(detail),
	}); err != nil {
		log.Printf("audit append (%s %s): %v", action, actor, err)
	}
}

// listAudit serves GET /api/v1/admin/audit — the admin audit trail,
// organization:admin only, scoped to the caller's tenant (platform admins may
// pass ?tenant=). Paginated via ?limit / ?offset.
func (h *HTTPGateway) listAudit(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Audit == nil {
		writeJSONError(rw, http.StatusNotImplemented, "audit log not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	// Force-scoped to the caller's own tenant — an admin can't read
	// another tenant's trail. (Cross-tenant inspection for a platform
	// super-admin is a future refinement.)
	events, err := h.Audit.List(r.Context(), core.AuditQuery{
		Tenant: p.Tenant,
		Limit:  queryInt(r, "limit", defaultAuditLimit),
		Offset: queryInt(r, "offset", 0),
	})
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"events": events})
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
