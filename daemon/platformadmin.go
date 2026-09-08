// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

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

// Runtime platform-admin grants. The DAZYFLOW_PLATFORM_ADMINS env allowlist is
// the immutable bootstrap layer (it can't be edited without a restart); this
// store is the mutable layer a platform admin manages from the UI to grant or
// revoke the cross-tenant super-admin role without redeploying.
//
// Both layers feed the SAME chokepoint (HTTPGateway.elevatePlatformAdmin),
// which stamps core.PlatformAdminRole onto a session at issue time. So a grant
// takes effect on the target's next session issue (sign-in or org switch); a
// revoke takes effect once their live sessions are dropped (the handler revokes
// them) and they re-authenticate. Env-allowlist admins are NOT revocable here —
// elevatePlatformAdmin would re-grant them on next login — so the revoke
// handler refuses them and points the operator at the env var.
//
// Like the drop killswitch and entitlement stores, a cached in-memory snapshot
// keeps the per-session-issue Granted lookup off the DB hot path; writes
// refresh it and a ticker catches cross-node changes.

type PlatformAdminGrant struct {
	Email     string    `json:"email"`
	GrantedBy string    `json:"granted_by"`
	CreatedAt time.Time `json:"created_at"`
}

type PlatformAdminStore interface {
	Granted(email string) bool
	Grant(ctx context.Context, email, grantedBy string) error
	Revoke(ctx context.Context, email string) error
	List(ctx context.Context) ([]PlatformAdminGrant, error)
	// AnonymizeGrantedBy replaces an erased person's email where it appears as
	// the GRANTER of someone else's role, returning the rows changed.
	//
	// Their own grant row is removed by Revoke; this is the other half. A
	// granter's address sits in rows belonging to people who are still here, so
	// it cannot be deleted with them — it is pseudonymised, like the audit
	// trail. Without this an erased admin's address survives in every role they
	// ever handed out.
	AnonymizeGrantedBy(ctx context.Context, email string) (int, error)
}

const pgPlatformAdminSchema = `
CREATE TABLE IF NOT EXISTS platform_admins (
    email      TEXT PRIMARY KEY,
    granted_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func EnsurePgPlatformAdminSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgPlatformAdminSchema)
}

type PgPlatformAdminStore struct {
	pool   *pgxpool.Pool
	logger *log.Logger

	mu      sync.RWMutex
	granted map[string]struct{}
}

func NewPgPlatformAdminStore(ctx context.Context, pool *pgxpool.Pool) (*PgPlatformAdminStore, error) {
	if err := EnsurePgPlatformAdminSchema(ctx, pool); err != nil {
		return nil, err
	}
	s := &PgPlatformAdminStore{
		pool:    pool,
		logger:  log.New(log.Writer(), "platformadmin: ", log.LstdFlags),
		granted: map[string]struct{}{},
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	go s.refreshLoop(ctx)
	return s, nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *PgPlatformAdminStore) Granted(email string) bool {
	email = normalizeEmail(email)
	if email == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.granted[email]
	return ok
}

func (s *PgPlatformAdminStore) Grant(ctx context.Context, email, grantedBy string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("email required")
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO platform_admins (email, granted_by) VALUES ($1, $2)
		 ON CONFLICT (email) DO UPDATE SET granted_by = EXCLUDED.granted_by`,
		email, grantedBy); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgPlatformAdminStore) Revoke(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("email required")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM platform_admins WHERE email=$1`, email); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgPlatformAdminStore) AnonymizeGrantedBy(ctx context.Context, email string) (int, error) {
	email = normalizeEmail(email)
	if email == "" {
		return 0, fmt.Errorf("email required")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE platform_admins SET granted_by = $2 WHERE granted_by = $1`, email, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgPlatformAdminStore) List(ctx context.Context) ([]PlatformAdminGrant, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT email, granted_by, created_at FROM platform_admins ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PlatformAdminGrant, 0)
	for rows.Next() {
		var g PlatformAdminGrant
		if err := rows.Scan(&g.Email, &g.GrantedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *PgPlatformAdminStore) reload(ctx context.Context) error {
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

func (s *PgPlatformAdminStore) refreshLoop(ctx context.Context) {
	pgstore.PollReload(ctx, s.reload, s.logger.Printf, "refresh: %v")
}
