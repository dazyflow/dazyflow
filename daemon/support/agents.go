// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runtime support-agent grants. A support agent carries core.SupportAgentRole
// (PermSupportAgent) — the weak, grant-gated support permission. Unlike
// platform-admin there is NO env-allowlist bootstrap: support agents are managed
// entirely at runtime from this store, so an operator can add/remove vendor
// support staff without a redeploy.
//
// The store feeds the same session-issue chokepoint as platform-admin
// elevation: at issue time, a user whose email is Granted here gets
// SupportAgentRole appended. A grant takes effect on the target's next session
// issue; a revoke once their live sessions drop and they re-authenticate.
// Holding the role grants NO ambient access — it only lets the agent request an
// AccessGrant and use the support-view capability (see AuthorizeGraphSupportView).
//
// Like platform-admin, a cached in-memory snapshot keeps the per-session-issue
// Granted lookup off the DB hot path; writes refresh it and a ticker catches
// cross-node changes.

// AgentGrant is one runtime support-agent grant row.
type AgentGrant struct {
	Email     string    `json:"email"`
	GrantedBy string    `json:"granted_by"`
	CreatedAt time.Time `json:"created_at"`
}

// AgentStore is the runtime support-agent grant boundary.
type AgentStore interface {
	// Granted reports whether email currently holds a support-agent grant. Reads
	// the cached snapshot, so it's cheap and safe at every session issue.
	Granted(email string) bool
	Grant(ctx context.Context, email, grantedBy string) error
	Revoke(ctx context.Context, email string) error
	List(ctx context.Context) ([]AgentGrant, error)
	// AnonymizeGrantedBy replaces an erased person's email where it appears as
	// the GRANTER of someone else's agent role, returning the rows changed.
	// See PlatformAdminStore.AnonymizeGrantedBy — same shape, same reason.
	AnonymizeGrantedBy(ctx context.Context, email string) (int, error)
}

// ---- In-memory (tests + single-node) ---------------------------------------

// MemAgentStore is a mutex-guarded in-memory AgentStore.
type MemAgentStore struct {
	mu     sync.RWMutex
	grants map[string]AgentGrant // keyed by normalized email
}

// NewMemAgentStore returns an empty in-memory support-agent store.
func NewMemAgentStore() *MemAgentStore {
	return &MemAgentStore{grants: map[string]AgentGrant{}}
}

var _ AgentStore = (*MemAgentStore)(nil)

func (s *MemAgentStore) Granted(email string) bool {
	email = normalizeEmail(email)
	if email == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.grants[email]
	return ok
}

func (s *MemAgentStore) Grant(_ context.Context, email, grantedBy string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("email required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.grants[email]
	if !ok {
		existing = AgentGrant{Email: email, CreatedAt: time.Time{}}
	}
	existing.GrantedBy = grantedBy
	s.grants[email] = existing
	return nil
}

func (s *MemAgentStore) Revoke(_ context.Context, email string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("email required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.grants, email)
	return nil
}

func (s *MemAgentStore) AnonymizeGrantedBy(_ context.Context, email string) (int, error) {
	email = normalizeEmail(email)
	if email == "" {
		return 0, fmt.Errorf("email required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, g := range s.grants {
		if normalizeEmail(g.GrantedBy) == email {
			g.GrantedBy = core.ErasedIdentity
			s.grants[k] = g
			n++
		}
	}
	return n, nil
}

func (s *MemAgentStore) List(_ context.Context) ([]AgentGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AgentGrant, 0, len(s.grants))
	for _, g := range s.grants {
		out = append(out, g)
	}
	return out, nil
}

// ---- Postgres (production) -------------------------------------------------

const pgAgentSchema = `
CREATE TABLE IF NOT EXISTS support_agents (
    email      TEXT PRIMARY KEY,
    granted_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// EnsurePgAgentSchema creates the support_agents table. Idempotent.
func EnsurePgAgentSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgAgentSchema)
}

// PgAgentStore is the Postgres AgentStore with a cached snapshot.
type PgAgentStore struct {
	pool   *pgxpool.Pool
	logger *log.Logger

	mu      sync.RWMutex
	granted map[string]struct{}
}

// NewPgAgentStore creates the schema, loads the snapshot, and starts the
// cross-node refresh loop.
func NewPgAgentStore(ctx context.Context, pool *pgxpool.Pool) (*PgAgentStore, error) {
	if err := EnsurePgAgentSchema(ctx, pool); err != nil {
		return nil, err
	}
	s := &PgAgentStore{
		pool:    pool,
		logger:  log.New(log.Writer(), "supportagent: ", log.LstdFlags),
		granted: map[string]struct{}{},
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	go s.refreshLoop(ctx)
	return s, nil
}

func (s *PgAgentStore) Granted(email string) bool {
	email = normalizeEmail(email)
	if email == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.granted[email]
	return ok
}

func (s *PgAgentStore) Grant(ctx context.Context, email, grantedBy string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("email required")
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO support_agents (email, granted_by) VALUES ($1, $2)
		 ON CONFLICT (email) DO UPDATE SET granted_by = EXCLUDED.granted_by`,
		email, grantedBy); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgAgentStore) Revoke(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("email required")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM support_agents WHERE email=$1`, email); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgAgentStore) AnonymizeGrantedBy(ctx context.Context, email string) (int, error) {
	email = normalizeEmail(email)
	if email == "" {
		return 0, fmt.Errorf("email required")
	}
	// Compare on the NORMALIZED stored value, not the raw column. Grant()
	// normalizes the grantee's email but stores grantedBy exactly as the admin
	// form supplied it, so a granter recorded as "Operator@Acme.COM" would not
	// match the normalized identifier an erasure request arrives with — leaving
	// the erased person's address in the table and reporting 0 rows changed.
	// MemAgentStore.AnonymizeGrantedBy already normalizes both sides; this is
	// the same comparison in SQL, and it also repairs rows already stored.
	tag, err := s.pool.Exec(ctx,
		`UPDATE support_agents SET granted_by = $2 WHERE lower(btrim(granted_by)) = $1`,
		email, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgAgentStore) List(ctx context.Context) ([]AgentGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT email, granted_by, created_at FROM support_agents ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AgentGrant, 0)
	for rows.Next() {
		var g AgentGrant
		if err := rows.Scan(&g.Email, &g.GrantedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *PgAgentStore) reload(ctx context.Context) error {
	grants, err := s.List(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]struct{}, len(grants))
	for _, g := range grants {
		m[normalizeEmail(g.Email)] = struct{}{}
	}
	s.mu.Lock()
	s.granted = m
	s.mu.Unlock()
	return nil
}

func (s *PgAgentStore) refreshLoop(ctx context.Context) {
	pgstore.PollReload(ctx, s.reload, s.logger.Printf, "refresh: %v")
}

// normalizeEmail is the identity form of an address: case-folded and trimmed.
//
// Support-agent membership and every grant check compare addresses, and the
// person who types one into an admin form does not type it the same way twice.
// The same two calls appear in daemon/platformadmin.go and across auth/ — the
// repo's idiom for this, spelled here because the package now stands alone.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
