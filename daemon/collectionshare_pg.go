// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
)

// PgCollectionShareStore is the durable CollectionShareStore — one row per
// (tenant, workspace, collection), so a collection has at most one live link.
//
// The token is stored in the clear rather than hashed, for the same reason
// share.go's is: it must be re-displayable, since reopening the Share dialog
// shows the existing link instead of forcing a rotation that would break
// whatever was already sent out.
type PgCollectionShareStore struct {
	pool *pgxpool.Pool
}

const pgCollectionShareSchema = `
CREATE TABLE IF NOT EXISTS collection_shares (
    tenant     TEXT NOT NULL,
    workspace  TEXT NOT NULL,
    collection TEXT NOT NULL,
    token      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant, workspace, collection)
);
CREATE UNIQUE INDEX IF NOT EXISTS collection_shares_token_idx ON collection_shares (token);
`

func NewPgCollectionShareStore(ctx context.Context, pool *pgxpool.Pool) (*PgCollectionShareStore, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgCollectionShareSchema); err != nil {
		return nil, err
	}
	return &PgCollectionShareStore{pool: pool}, nil
}

func (s *PgCollectionShareStore) Get(ctx context.Context, tenant, workspace, collection string) (CollectionShare, error) {
	sh := CollectionShare{Tenant: tenant, Workspace: workspace, Collection: collection}
	err := s.pool.QueryRow(ctx,
		`SELECT token, created_at, created_by FROM collection_shares
		   WHERE tenant = $1 AND workspace = $2 AND collection = $3`,
		tenant, workspace, collection).Scan(&sh.Token, &sh.CreatedAt, &sh.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return CollectionShare{}, core.ErrNotFound
	}
	if err != nil {
		return CollectionShare{}, err
	}
	return sh, nil
}

func (s *PgCollectionShareStore) List(ctx context.Context, tenant, workspace string) ([]CollectionShare, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT collection, token, created_at, created_by FROM collection_shares
		   WHERE tenant = $1 AND workspace = $2 ORDER BY collection`,
		tenant, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CollectionShare{}
	for rows.Next() {
		sh := CollectionShare{Tenant: tenant, Workspace: workspace}
		if err := rows.Scan(&sh.Collection, &sh.Token, &sh.CreatedAt, &sh.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *PgCollectionShareStore) Upsert(ctx context.Context, tenant, workspace, collection, token, createdBy string) (CollectionShare, error) {
	sh := CollectionShare{Tenant: tenant, Workspace: workspace, Collection: collection}
	// Rotate in place: a fresh token replaces the old one, and created_at /
	// created_by are reset so the dialog reflects the current link's age.
	err := s.pool.QueryRow(ctx,
		`INSERT INTO collection_shares (tenant, workspace, collection, token, created_by)
		   VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (tenant, workspace, collection)
		   DO UPDATE SET token = EXCLUDED.token,
		                 created_at = now(),
		                 created_by = EXCLUDED.created_by
		 RETURNING token, created_at, created_by`,
		tenant, workspace, collection, token, createdBy).Scan(&sh.Token, &sh.CreatedAt, &sh.CreatedBy)
	if err != nil {
		return CollectionShare{}, err
	}
	return sh, nil
}

func (s *PgCollectionShareStore) Delete(ctx context.Context, tenant, workspace, collection string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM collection_shares
		   WHERE tenant = $1 AND workspace = $2 AND collection = $3`,
		tenant, workspace, collection)
	return err
}

func (s *PgCollectionShareStore) Lookup(ctx context.Context, token string) (CollectionShare, error) {
	var sh CollectionShare
	err := s.pool.QueryRow(ctx,
		`SELECT tenant, workspace, collection, token, created_at, created_by
		   FROM collection_shares WHERE token = $1`,
		token).Scan(&sh.Tenant, &sh.Workspace, &sh.Collection, &sh.Token, &sh.CreatedAt, &sh.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return CollectionShare{}, core.ErrNotFound
	}
	if err != nil {
		return CollectionShare{}, err
	}
	return sh, nil
}

// DeleteByTenant removes every collection link for a tenant — the org-erasure
// cascade hook (gdpr.go's tenantEraser).
func (s *PgCollectionShareStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM collection_shares WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgCollectionShareStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE collection_shares SET created_by = $2 WHERE created_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
