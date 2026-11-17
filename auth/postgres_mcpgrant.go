// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

const pgMCPGrantSchema = `
CREATE TABLE IF NOT EXISTS mcp_oauth_grants (
    id                 TEXT PRIMARY KEY,
    subject            TEXT NOT NULL,
    tenant             TEXT NOT NULL,
    workspace          TEXT NOT NULL,
    roles              JSONB NOT NULL DEFAULT '[]',
    client_name        TEXT NOT NULL DEFAULT '',
    redirect_host      TEXT NOT NULL DEFAULT '',
    scope              TEXT NOT NULL DEFAULT '',
    access_hash        TEXT NOT NULL,
    access_expires_at  TIMESTAMPTZ NOT NULL,
    refresh_hash       TEXT NOT NULL,
    prev_refresh_hash  TEXT NOT NULL DEFAULT '',
    refresh_expires_at TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL,
    refreshed_at       TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS mcp_oauth_grants_subject_idx ON mcp_oauth_grants (subject);
`

type PgMCPGrantStore struct {
	pool *pgxpool.Pool
}

func NewPgMCPGrantStore(ctx context.Context, pool *pgxpool.Pool) (*PgMCPGrantStore, error) {
	if err := ensureSchema(ctx, pool, pgMCPGrantSchema); err != nil {
		return nil, err
	}
	return &PgMCPGrantStore{pool: pool}, nil
}

const pgMCPGrantColumns = `id, subject, tenant, workspace, roles, client_name, redirect_host, scope,
	access_hash, access_expires_at, refresh_hash, prev_refresh_hash, refresh_expires_at, created_at, refreshed_at`

func (s *PgMCPGrantStore) PutGrant(ctx context.Context, g MCPGrant) error {
	roles, err := marshalRoles(g.Roles)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO mcp_oauth_grants (`+pgMCPGrantColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		g.ID, g.Subject, g.Tenant, g.Workspace, roles, g.ClientName, g.RedirectHost, g.Scope,
		g.AccessHash, g.AccessExpiresAt, g.RefreshHash, g.PrevRefreshHash, g.RefreshExpiresAt, g.CreatedAt, nullTime(g.RefreshedAt))
	return err
}

func (s *PgMCPGrantStore) GetGrant(ctx context.Context, id string) (MCPGrant, error) {
	g, err := scanMCPGrant(s.pool.QueryRow(ctx, `SELECT `+pgMCPGrantColumns+` FROM mcp_oauth_grants WHERE id=$1`, id))
	return g, noRows(err, ErrInvalidCredential)
}

func (s *PgMCPGrantStore) SwapGrant(ctx context.Context, g MCPGrant, oldRefreshHash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE mcp_oauth_grants SET
		access_hash=$2, access_expires_at=$3, refresh_hash=$4, prev_refresh_hash=$5,
		refresh_expires_at=$6, refreshed_at=$7
		WHERE id=$1 AND refresh_hash=$8`,
		g.ID, g.AccessHash, g.AccessExpiresAt, g.RefreshHash, g.PrevRefreshHash,
		g.RefreshExpiresAt, nullTime(g.RefreshedAt), oldRefreshHash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidCredential
	}
	return nil
}

func (s *PgMCPGrantStore) DeleteGrant(ctx context.Context, id string) error {
	_, err := deleteWhere(ctx, s.pool, "mcp_oauth_grants", "id", id)
	return err
}

func (s *PgMCPGrantStore) ListGrantsBySubject(ctx context.Context, subject string) ([]MCPGrant, error) {
	return queryRows(ctx, s.pool, scanMCPGrant, `SELECT `+pgMCPGrantColumns+` FROM mcp_oauth_grants
		WHERE subject=$1 ORDER BY created_at DESC`, subject)
}

func (s *PgMCPGrantStore) RevokeSubjectGrants(ctx context.Context, subject string) (int, error) {
	return deleteWhere(ctx, s.pool, "mcp_oauth_grants", "subject", subject)
}

// nullTime stores a zero time as NULL: a grant never refreshed has no
// refreshed_at, rather than one in year 1.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// scanMCPGrant scans one mcp_oauth_grants row (in pgMCPGrantColumns order).
func scanMCPGrant(row rowScanner) (MCPGrant, error) {
	var (
		g         MCPGrant
		rolesRaw  []byte
		refreshed *time.Time
	)
	if err := row.Scan(&g.ID, &g.Subject, &g.Tenant, &g.Workspace, &rolesRaw, &g.ClientName, &g.RedirectHost, &g.Scope,
		&g.AccessHash, &g.AccessExpiresAt, &g.RefreshHash, &g.PrevRefreshHash, &g.RefreshExpiresAt, &g.CreatedAt, &refreshed); err != nil {
		return MCPGrant{}, err
	}
	roles, err := jsonOrZero[[]core.Role](rolesRaw)
	if err != nil {
		return MCPGrant{}, err
	}
	g.Roles = roles
	if refreshed != nil {
		g.RefreshedAt = *refreshed
	}
	return g, nil
}
