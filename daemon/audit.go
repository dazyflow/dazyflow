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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
)

type auditAPI struct {
	Audit core.AuditLog
}

func (h *HTTPGateway) auditAPI() *auditAPI {
	return &auditAPI{Audit: h.Audit}
}

const defaultAuditLimit = 100

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
-- Retention's predicate, partial on the same exemption the sweep applies:
-- approval events are kept indefinitely, so they need never be indexed here.
CREATE INDEX IF NOT EXISTS audit_events_prune_idx
    ON audit_events (ts) WHERE action <> 'approval';
`

type PgAuditLog struct {
	pool *pgxpool.Pool
}

func NewPgAuditLog(ctx context.Context, pool *pgxpool.Pool) (*PgAuditLog, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgAuditSchema); err != nil {
		return nil, err
	}
	return &PgAuditLog{pool: pool}, nil
}

// Bounded batches, so a large backlog does not lock the table in one statement.
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

func (p *PgAuditLog) AnonymizeActor(ctx context.Context, actor string) (int, error) {
	tag, err := p.pool.Exec(ctx,
		`UPDATE audit_events SET actor = $2, detail = '' WHERE actor = $1`, actor, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

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

// Best-effort: a failed audit write must never fail the action being audited.
const auditFieldLimit = 512

// An audit line is read in a terminal, so a control character in it can forge
// what the operator sees.
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

// The entire dependency a handler needs, so handlers stay testable.
type auditor struct{ log core.AuditLog }

func (a auditor) audit(ctx context.Context, p core.Principal, action, target, detail string) {
	if a.log == nil {
		return
	}
	if err := a.log.Append(ctx, core.AuditEvent{
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

// The authentication lifecycle, which is the trail an incident is read from.
func (a auditor) auditAuth(ctx context.Context, r *http.Request, tenant, actor, action, detail string) {
	if a.log == nil {
		return
	}
	ipNote := "ip=" + clientIP(r)
	if detail == "" {
		detail = ipNote
	} else {
		detail += " " + ipNote
	}
	if err := a.log.Append(ctx, core.AuditEvent{
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

func (h *auditAPI) listAudit(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Audit == nil {
		writeJSONError(rw, http.StatusNotImplemented, "audit log not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	// Force-scoped: an admin cannot read another tenant's trail.
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

func (h *HTTPGateway) auditor() auditor { return auditor{h.Audit} }
