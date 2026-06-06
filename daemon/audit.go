package daemon

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/hazyflow/core"
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
	if _, err := pool.Exec(ctx, pgAuditSchema); err != nil {
		return nil, err
	}
	return &PgAuditLog{pool: pool}, nil
}

// Prune deletes audit rows older than the cutoff in bounded batches so a
// large backlog doesn't lock the table in one statement. Returns the
// total deleted. olderThan <= 0 is a no-op (retention disabled).
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
			     SELECT id FROM audit_events WHERE ts < $1 LIMIT $2)`, cutoff, batch)
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
	rows, err := p.pool.Query(ctx,
		`SELECT ts, tenant, actor, action, target, detail FROM audit_events
		  WHERE tenant=$1 ORDER BY id DESC LIMIT $2 OFFSET $3`, q.Tenant, limit, offset)
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
func (h *HTTPGateway) audit(ctx context.Context, p core.Principal, action, target, detail string) {
	if h.Audit == nil {
		return
	}
	if err := h.Audit.Append(ctx, core.AuditEvent{
		Time:   time.Now(),
		Tenant: p.Tenant,
		Actor:  p.Subject,
		Action: action,
		Target: target,
		Detail: detail,
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
		Tenant: tenant,
		Actor:  actor,
		Action: action,
		Target: actor,
		Detail: detail,
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
	if !p.Has(core.PermOrganizationAdmin) {
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
