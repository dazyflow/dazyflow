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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AgentGrant struct {
	Email     string    `json:"email"`
	GrantedBy string    `json:"granted_by"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentStore interface {
	Granted(email string) bool
	Grant(ctx context.Context, email, grantedBy string) error
	Revoke(ctx context.Context, email string) error
	List(ctx context.Context) ([]AgentGrant, error)
	AnonymizeGrantedBy(ctx context.Context, email string) (int, error)
}

type MemAgentStore struct {
	mu     sync.RWMutex
	grants map[string]AgentGrant // keyed by normalized email
}

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

const pgAgentSchema = `
CREATE TABLE IF NOT EXISTS support_agents (
    email      TEXT PRIMARY KEY,
    granted_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func EnsurePgAgentSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgAgentSchema)
}

type PgAgentStore struct {
	pool   *pgxpool.Pool
	logger *log.Logger

	mu      sync.RWMutex
	granted map[string]struct{}
}

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
	// Compare the NORMALIZED stored value, not the raw column: Grant normalizes
	// the grantee's email but stores grantedBy as the admin form supplied it, so
	// "Operator@Acme.COM" never matched the normalized identifier an erasure
	// request arrives with — leaving the address in the table and reporting 0
	// rows changed. This also repairs rows already stored.
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

// normalizeEmail case-folds and trims, because membership and every grant check
// compare addresses and nobody types one into an admin form the same way twice.
// Spelled out here rather than shared with daemon/platformadmin.go and auth/
// because the package stands alone.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
