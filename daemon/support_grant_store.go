// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// pgScanner is the shared Scan surface of pgx.Row and pgx.Rows, so one
// scanGrant/scanBundle helper serves both single-row and iteration paths.
type pgScanner interface {
	Scan(dest ...any) error
}

// support_grant_store.go is the in-memory core.GrantStore for the Support
// feature (tests + single-node setups). The Postgres implementation mirrors it
// for production, the same way JobStore / AuditLog have both. All lifecycle
// transitions go through the pure core guards (CanDecide / CanRevoke), so the
// store never invents a state the core model disallows.

// errGrantExists / errGrantNotDecidable / errGrantNotRevocable are the
// store-level transition errors. A missing grant reports core.ErrNotFound so
// callers can errors.Is it the same way they do for jobs.
var (
	errGrantExists       = errors.New("grant already exists")
	errGrantNotDecidable = errors.New("grant is not in the requested state")
	errGrantNotRevocable = errors.New("grant is not approved")
	errBadDecision       = errors.New("decision must be approved or denied")
)

// MemGrantStore is a mutex-guarded in-memory GrantStore.
type MemGrantStore struct {
	mu   sync.Mutex
	byID map[string]core.AccessGrant
}

// NewMemGrantStore returns an empty in-memory grant store.
func NewMemGrantStore() *MemGrantStore {
	return &MemGrantStore{byID: map[string]core.AccessGrant{}}
}

var _ core.GrantStore = (*MemGrantStore)(nil)

// Create records a new grant. ID is required and must be unique; the grant is
// normalized into the requested state (Create is the request entry point).
func (s *MemGrantStore) Create(_ context.Context, g core.AccessGrant) error {
	if g.ID == "" {
		return fmt.Errorf("grant id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[g.ID]; exists {
		return fmt.Errorf("%w: %s", errGrantExists, g.ID)
	}
	g.Status = core.GrantRequested
	s.byID[g.ID] = g
	return nil
}

// Decide approves or denies a requested grant. On approval it stamps the time
// box (expiresAt); a denial ignores it.
func (s *MemGrantStore) Decide(_ context.Context, id string, status core.GrantStatus, by string, at, expiresAt time.Time) error {
	if status != core.GrantApproved && status != core.GrantDenied {
		return fmt.Errorf("%w: got %q", errBadDecision, status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: grant %s", core.ErrNotFound, id)
	}
	if !g.CanDecide() {
		return fmt.Errorf("%w: %s is %q", errGrantNotDecidable, id, g.Status)
	}
	g.Status = status
	g.DecidedBy = by
	decidedAt := at
	g.DecidedAt = &decidedAt
	if status == core.GrantApproved {
		g.ExpiresAt = expiresAt
	}
	s.byID[id] = g
	return nil
}

// Revoke ends an approved grant early.
func (s *MemGrantStore) Revoke(_ context.Context, id, by string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.byID[id]
	if !ok {
		return fmt.Errorf("%w: grant %s", core.ErrNotFound, id)
	}
	if !g.CanRevoke() {
		return fmt.Errorf("%w: %s is %q", errGrantNotRevocable, id, g.Status)
	}
	g.Status = core.GrantRevoked
	g.RevokedBy = by
	revokedAt := at
	g.RevokedAt = &revokedAt
	s.byID[id] = g
	return nil
}

// Get returns the grant, or core.ErrNotFound.
func (s *MemGrantStore) Get(_ context.Context, id string) (core.AccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.byID[id]
	if !ok {
		return core.AccessGrant{}, fmt.Errorf("%w: grant %s", core.ErrNotFound, id)
	}
	return g, nil
}

// ActiveGrant returns the active grant authorizing (agent, tenant, flowID) at
// now, if any. When several match (unusual — a fresh request supersedes an old
// one), the latest-expiring wins so a re-grant extends access rather than
// tripping over a stale entry.
func (s *MemGrantStore) ActiveGrant(_ context.Context, agent, tenant, flowID string, now time.Time) (core.AccessGrant, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best core.AccessGrant
	found := false
	for _, g := range s.byID {
		if g.AgentSubject != agent || g.Tenant != tenant || g.FlowID != flowID {
			continue
		}
		if !g.IsActive(now) {
			continue
		}
		if !found || g.ExpiresAt.After(best.ExpiresAt) {
			best, found = g, true
		}
	}
	return best, found, nil
}

// ListForTenant returns every grant in tenant, newest request first.
func (s *MemGrantStore) ListForTenant(_ context.Context, tenant string) ([]core.AccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.AccessGrant, 0)
	for _, g := range s.byID {
		if g.Tenant == tenant {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.After(out[j].RequestedAt) })
	return out, nil
}

// ListForAgent returns every grant requested by agent, newest request first.
func (s *MemGrantStore) ListForAgent(_ context.Context, agent string) ([]core.AccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.AccessGrant, 0)
	for _, g := range s.byID {
		if g.AgentSubject == agent {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.After(out[j].RequestedAt) })
	return out, nil
}

// ---- Postgres --------------------------------------------------------------

const pgGrantSchema = `
CREATE TABLE IF NOT EXISTS access_grants (
    id            TEXT PRIMARY KEY,
    ticket_id     TEXT NOT NULL DEFAULT '',
    tenant        TEXT NOT NULL,
    flow_id       TEXT NOT NULL,
    agent_subject TEXT NOT NULL,
    status        TEXT NOT NULL,
    requested_at  TIMESTAMPTZ NOT NULL,
    requested_by  TEXT NOT NULL DEFAULT '',
    decided_by    TEXT NOT NULL DEFAULT '',
    decided_at    TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ,
    revoked_by    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS access_grants_active_idx
    ON access_grants (agent_subject, tenant, flow_id, status);
CREATE INDEX IF NOT EXISTS access_grants_tenant_idx ON access_grants (tenant);
`

// EnsurePgGrantSchema creates the access_grants table. Idempotent.
func EnsurePgGrantSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgGrantSchema)
}

// PgGrantStore is the Postgres core.GrantStore. No cached snapshot: grants are
// low-volume and every read is authoritative (an expiry/revoke must be seen
// immediately across nodes), so it queries the table directly.
type PgGrantStore struct {
	pool *pgxpool.Pool
}

// NewPgGrantStore creates the schema and returns the store.
func NewPgGrantStore(ctx context.Context, pool *pgxpool.Pool) (*PgGrantStore, error) {
	if err := EnsurePgGrantSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgGrantStore{pool: pool}, nil
}

var _ core.GrantStore = (*PgGrantStore)(nil)

const grantCols = `id, ticket_id, tenant, flow_id, agent_subject, status,
	requested_at, requested_by, decided_by, decided_at, expires_at, revoked_at, revoked_by`

func scanGrant(r pgScanner) (core.AccessGrant, error) {
	var (
		g         core.AccessGrant
		status    string
		decidedAt *time.Time
		expiresAt *time.Time
		revokedAt *time.Time
	)
	if err := r.Scan(&g.ID, &g.TicketID, &g.Tenant, &g.FlowID, &g.AgentSubject, &status,
		&g.RequestedAt, &g.RequestedBy, &g.DecidedBy, &decidedAt, &expiresAt, &revokedAt, &g.RevokedBy); err != nil {
		return core.AccessGrant{}, err
	}
	g.Status = core.GrantStatus(status)
	g.DecidedAt = decidedAt
	if expiresAt != nil {
		g.ExpiresAt = *expiresAt
	}
	g.RevokedAt = revokedAt
	return g, nil
}

func (s *PgGrantStore) Create(ctx context.Context, g core.AccessGrant) error {
	if g.ID == "" {
		return fmt.Errorf("grant id is required")
	}
	ct, err := s.pool.Exec(ctx,
		`INSERT INTO access_grants (id, ticket_id, tenant, flow_id, agent_subject, status, requested_at, requested_by)
		 VALUES ($1,$2,$3,$4,$5,'requested',$6,$7) ON CONFLICT (id) DO NOTHING`,
		g.ID, g.TicketID, g.Tenant, g.FlowID, g.AgentSubject, g.RequestedAt, g.RequestedBy)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", errGrantExists, g.ID)
	}
	return nil
}

func (s *PgGrantStore) Decide(ctx context.Context, id string, status core.GrantStatus, by string, at, expiresAt time.Time) error {
	if status != core.GrantApproved && status != core.GrantDenied {
		return fmt.Errorf("%w: got %q", errBadDecision, status)
	}
	// Set expires_at only on approval; the conditional UPDATE also enforces the
	// requested→decided transition (CanDecide) atomically.
	ct, err := s.pool.Exec(ctx,
		`UPDATE access_grants
		 SET status=$2, decided_by=$3, decided_at=$4,
		     expires_at = CASE WHEN $2='approved' THEN $5 ELSE expires_at END
		 WHERE id=$1 AND status='requested'`,
		id, string(status), by, at, expiresAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return s.transitionError(ctx, id, errGrantNotDecidable)
	}
	return nil
}

func (s *PgGrantStore) Revoke(ctx context.Context, id, by string, at time.Time) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE access_grants SET status='revoked', revoked_at=$2, revoked_by=$3
		 WHERE id=$1 AND status='approved'`,
		id, at, by)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return s.transitionError(ctx, id, errGrantNotRevocable)
	}
	return nil
}

// transitionError disambiguates a no-op conditional UPDATE: a missing grant is
// ErrNotFound, otherwise the wrong-state transition error.
func (s *PgGrantStore) transitionError(ctx context.Context, id string, stateErr error) error {
	g, err := s.Get(ctx, id)
	if err != nil {
		return err // ErrNotFound (or a real error)
	}
	return fmt.Errorf("%w: %s is %q", stateErr, id, g.Status)
}

func (s *PgGrantStore) Get(ctx context.Context, id string) (core.AccessGrant, error) {
	g, err := scanGrant(s.pool.QueryRow(ctx, `SELECT `+grantCols+` FROM access_grants WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.AccessGrant{}, fmt.Errorf("%w: grant %s", core.ErrNotFound, id)
	}
	return g, err
}

func (s *PgGrantStore) ActiveGrant(ctx context.Context, agent, tenant, flowID string, now time.Time) (core.AccessGrant, bool, error) {
	g, err := scanGrant(s.pool.QueryRow(ctx,
		`SELECT `+grantCols+` FROM access_grants
		 WHERE agent_subject=$1 AND tenant=$2 AND flow_id=$3
		   AND status='approved' AND revoked_at IS NULL AND expires_at > $4
		 ORDER BY expires_at DESC LIMIT 1`,
		agent, tenant, flowID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.AccessGrant{}, false, nil
	}
	if err != nil {
		return core.AccessGrant{}, false, err
	}
	return g, true, nil
}

func (s *PgGrantStore) ListForTenant(ctx context.Context, tenant string) ([]core.AccessGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+grantCols+` FROM access_grants WHERE tenant=$1 ORDER BY requested_at DESC`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.AccessGrant, 0)
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *PgGrantStore) ListForAgent(ctx context.Context, agent string) ([]core.AccessGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+grantCols+` FROM access_grants WHERE agent_subject=$1 ORDER BY requested_at DESC`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.AccessGrant, 0)
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteByTenant removes every access grant naming one org — requested,
// approved, denied or revoked alike. A grant is a record of consent to read
// that org's data; with the org gone it has nothing left to authorize, and
// leaving it behind would let a stale row outlive the tenant it points at.
func (s *MemGrantStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, g := range s.byID {
		if g.Tenant == tenant {
			delete(s.byID, id)
			n++
		}
	}
	return n, nil
}

// DeleteByTenant removes an org's access grants.
func (s *PgGrantStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM access_grants WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}
