// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
)

// PgGitMirrorStore is the durable GitMirrorStore — one row per
// (tenant, workspace), matching the one-mirror-per-workspace rule.
//
// Nothing here is secret. The remote URL and the credential's ACCOUNT NAME
// are stored in the clear; the SSH private key that actually authenticates
// the push stays in the per-tenant encrypted store and is resolved at push
// time (see MirrorPusher.doPush). That split is deliberate: this table is
// read on the save path and shown in the UI, so it must not hold key
// material, and rotating a key must not require touching mirror config.
type PgGitMirrorStore struct {
	pool *pgxpool.Pool
}

const pgGitMirrorSchema = `
CREATE TABLE IF NOT EXISTS git_mirrors (
    tenant          TEXT NOT NULL,
    workspace       TEXT NOT NULL,
    remote_url      TEXT NOT NULL,
    account         TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    push_on         TEXT NOT NULL DEFAULT 'publish',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by      TEXT NOT NULL DEFAULT '',
    last_attempt_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_commit     TEXT NOT NULL DEFAULT '',
    last_error      TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant, workspace)
);
`

const gitMirrorColumns = `tenant, workspace, remote_url, account, enabled, push_on,
    updated_at, updated_by, last_attempt_at, last_success_at, last_commit, last_error`

func NewPgGitMirrorStore(ctx context.Context, pool *pgxpool.Pool) (*PgGitMirrorStore, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgGitMirrorSchema); err != nil {
		return nil, err
	}
	return &PgGitMirrorStore{pool: pool}, nil
}

func scanGitMirror(row pgx.Row) (GitMirror, error) {
	var m GitMirror
	err := row.Scan(&m.Tenant, &m.Workspace, &m.RemoteURL, &m.Account, &m.Enabled, &m.PushOn,
		&m.UpdatedAt, &m.UpdatedBy, &m.LastAttemptAt, &m.LastSuccessAt, &m.LastCommit, &m.LastError)
	if err != nil {
		return GitMirror{}, err
	}
	return m, nil
}

func (s *PgGitMirrorStore) Get(ctx context.Context, tenant, workspace string) (GitMirror, error) {
	m, err := scanGitMirror(s.pool.QueryRow(ctx,
		`SELECT `+gitMirrorColumns+` FROM git_mirrors WHERE tenant = $1 AND workspace = $2`,
		tenant, workspace))
	if errors.Is(err, pgx.ErrNoRows) {
		return GitMirror{}, core.ErrNotFound
	}
	return m, err
}

// Upsert writes config only. The status columns are left alone so saving a
// config change doesn't erase the last push's outcome — an admin editing the
// remote URL should still see why the previous push failed.
func (s *PgGitMirrorStore) Upsert(ctx context.Context, m GitMirror) error {
	if m.Tenant == "" || m.Workspace == "" {
		return errors.New("tenant and workspace required")
	}
	updated := m.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	pushOn := m.PushOn
	if pushOn == "" {
		pushOn = PushOnPublish
	}
	const q = `
        INSERT INTO git_mirrors (tenant, workspace, remote_url, account, enabled, push_on, updated_at, updated_by)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT (tenant, workspace) DO UPDATE SET
            remote_url = EXCLUDED.remote_url,
            account    = EXCLUDED.account,
            enabled    = EXCLUDED.enabled,
            push_on    = EXCLUDED.push_on,
            updated_at = EXCLUDED.updated_at,
            updated_by = EXCLUDED.updated_by`
	_, err := s.pool.Exec(ctx, q, m.Tenant, m.Workspace, m.RemoteURL, m.Account,
		m.Enabled, pushOn, updated, m.UpdatedBy)
	return err
}

// Delete removes the mirror entirely, status included — "stop mirroring and
// forget where it went". Idempotent.
func (s *PgGitMirrorStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE git_mirrors SET updated_by = $2 WHERE updated_by = $1`, ident, core.ErasedIdentity)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgGitMirrorStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM git_mirrors WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgGitMirrorStore) Delete(ctx context.Context, tenant, workspace string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM git_mirrors WHERE tenant = $1 AND workspace = $2`, tenant, workspace)
	return err
}

// RecordAttempt writes status only, and only for an existing row — a push
// can never create a mirror or change where one points.
//
// last_success_at and last_commit advance only on success, so a failing
// mirror keeps showing when it last actually worked. That pairing is what
// tells an admin whether they've been unmirrored for ten minutes or three
// weeks.
func (s *PgGitMirrorStore) RecordAttempt(ctx context.Context, tenant, workspace string, st MirrorAttempt) error {
	at := st.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	ok := st.Err == ""
	const q = `
        UPDATE git_mirrors SET
            last_attempt_at = $3,
            last_error      = $4,
            last_success_at = CASE WHEN $5 THEN $3 ELSE last_success_at END,
            last_commit     = CASE WHEN $5 AND $6 <> '' THEN $6 ELSE last_commit END
        WHERE tenant = $1 AND workspace = $2`
	_, err := s.pool.Exec(ctx, q, tenant, workspace, at, truncateMirrorError(st.Err), ok, st.Commit)
	return err
}

// maxMirrorErrorLen bounds what we store from a git error. Transport
// failures can carry a whole remote-side banner; the first part names the
// cause and the rest would bloat every row and the UI panel.
const maxMirrorErrorLen = 2000

func truncateMirrorError(s string) string {
	if len(s) <= maxMirrorErrorLen {
		return s
	}
	return s[:maxMirrorErrorLen] + "…"
}
