// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// PgShareStore is the durable ShareStore — one row per (tenant, workspace),
// so a workspace has at most one live overview link. The token is stored in
// the clear (not hashed) because it must be re-displayable: reopening the
// Share dialog shows the existing link. Unlike a password or session, the
// token only unlocks a read-only, sanitized status view — possession of the
// link is the whole threat model, and anyone with DB read already sees more.
type PgShareStore struct {
	pool *pgxpool.Pool
}

const pgShareSchema = `
CREATE TABLE IF NOT EXISTS workspace_shares (
    tenant     TEXT NOT NULL,
    workspace  TEXT NOT NULL,
    token      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant, workspace)
);
CREATE UNIQUE INDEX IF NOT EXISTS workspace_shares_token_idx ON workspace_shares (token);
`

func NewPgShareStore(ctx context.Context, pool *pgxpool.Pool) (*PgShareStore, error) {
	if err := applyPgSchema(ctx, pool, pgShareSchema); err != nil {
		return nil, err
	}
	return &PgShareStore{pool: pool}, nil
}

func (s *PgShareStore) Get(ctx context.Context, tenant, workspace string) (Share, error) {
	sh := Share{Tenant: tenant, Workspace: workspace}
	err := s.pool.QueryRow(ctx,
		`SELECT token, created_at, created_by FROM workspace_shares
		   WHERE tenant = $1 AND workspace = $2`,
		tenant, workspace).Scan(&sh.Token, &sh.CreatedAt, &sh.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Share{}, core.ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	return sh, nil
}

func (s *PgShareStore) Upsert(ctx context.Context, tenant, workspace, token, createdBy string) (Share, error) {
	sh := Share{Tenant: tenant, Workspace: workspace}
	// Rotate in place: a fresh token replaces the old one, and created_at /
	// created_by are reset so the dialog reflects the current link's age.
	err := s.pool.QueryRow(ctx,
		`INSERT INTO workspace_shares (tenant, workspace, token, created_by)
		   VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant, workspace)
		   DO UPDATE SET token = EXCLUDED.token,
		                 created_at = now(),
		                 created_by = EXCLUDED.created_by
		 RETURNING token, created_at, created_by`,
		tenant, workspace, token, createdBy).Scan(&sh.Token, &sh.CreatedAt, &sh.CreatedBy)
	if err != nil {
		return Share{}, err
	}
	return sh, nil
}

func (s *PgShareStore) Delete(ctx context.Context, tenant, workspace string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM workspace_shares WHERE tenant = $1 AND workspace = $2`,
		tenant, workspace)
	return err
}

func (s *PgShareStore) Lookup(ctx context.Context, token string) (Share, error) {
	var sh Share
	err := s.pool.QueryRow(ctx,
		`SELECT tenant, workspace, token, created_at, created_by
		   FROM workspace_shares WHERE token = $1`,
		token).Scan(&sh.Tenant, &sh.Workspace, &sh.Token, &sh.CreatedAt, &sh.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Share{}, core.ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	return sh, nil
}

// DeleteByTenant removes every share for a tenant — the org-erasure cascade
// hook (gdpr.go's tenantEraser).
func (s *PgShareStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE workspace_shares SET created_by = $2 WHERE created_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgShareStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM workspace_shares WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
