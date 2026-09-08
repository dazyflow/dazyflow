// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres' unique_violation, which is how a name race surfaces.
func isPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

type PgRunnerStore struct {
	pool *pgxpool.Pool
}

func NewPgRunnerStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunnerStore, error) {
	if err := EnsurePgRunnerSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgRunnerStore{pool: pool}, nil
}

const runnerColumns = `tenant, name, labels, version, last_seen, created_by, created_at`

func scanRunner(row pgx.Row) (Runner, error) {
	var r Runner
	var lastSeen *time.Time
	if err := row.Scan(&r.Tenant, &r.Name, &r.Labels, &r.Version, &lastSeen,
		&r.CreatedBy, &r.CreatedAt); err != nil {
		return Runner{}, err
	}
	if lastSeen != nil {
		r.LastSeen = *lastSeen
	}
	return r, nil
}

func (s *PgRunnerStore) MintToken(ctx context.Context, tenant, createdBy, name string, hash []byte, expires time.Time) error {
	// Swept opportunistically: no janitor goroutine.
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM runner_tokens WHERE expires_at < now() - interval '1 day'`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runner_tokens (token_hash, tenant, created_by, name, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, hash, tenant, createdBy, name, expires)
	return err
}

// One transaction, or a spent token could leave no runner behind.
func (s *PgRunnerStore) RedeemToken(ctx context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Runner{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The UPDATE is the claim: `used_at IS NULL` in the WHERE means two concurrent
	// redemptions cannot both win.
	var tenant, createdBy, tokenName string
	err = tx.QueryRow(ctx, `
		UPDATE runner_tokens
		   SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		 RETURNING tenant, created_by, name`, tokenHash).Scan(&tenant, &createdBy, &tokenName)
	if err != nil {
		if isPgNoRows(err) {
			return Runner{}, ErrBadRunnerToken
		}
		return Runner{}, err
	}

	// Returns before commit, so a rejected redemption does not spend the token.
	if tokenName != "" && r.Name != tokenName {
		return Runner{}, ErrRunnerNameMismatch
	}

	r.Tenant = tenant
	r.CreatedBy = createdBy
	// A nil slice reaches Postgres as NULL, and the column is NOT NULL.
	labels := r.Labels
	if labels == nil {
		labels = []string{}
	}

	// How a rebuilt machine re-registers under its own name.
	const insertReplacing = `
		INSERT INTO tenant_runners
		    (tenant, name, labels, cred_hash, version, last_seen, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant, name) DO UPDATE
		   SET labels     = EXCLUDED.labels,
		       cred_hash  = EXCLUDED.cred_hash,
		       version    = EXCLUDED.version,
		       last_seen  = EXCLUDED.last_seen,
		       created_by = EXCLUDED.created_by
		RETURNING ` + runnerColumns
	const insertNew = `
		INSERT INTO tenant_runners
		    (tenant, name, labels, cred_hash, version, last_seen, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + runnerColumns
	query := insertNew
	if tokenName != "" {
		query = insertReplacing
	}
	stored, err := scanRunner(tx.QueryRow(ctx, query,
		r.Tenant, r.Name, labels, credHash, r.Version, nullTime(r.LastSeen), r.CreatedBy))
	if err != nil {
		if isPgUniqueViolation(err) {
			// Open token, name already registered: reject rather than clobber.
			return Runner{}, ErrRunnerNameTaken
		}
		return Runner{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Runner{}, err
	}
	return stored, nil
}

func (s *PgRunnerStore) RunnerByCredential(ctx context.Context, credHash []byte, seenAt time.Time) (Runner, error) {
	r, err := scanRunner(s.pool.QueryRow(ctx, `
		UPDATE tenant_runners
		   SET last_seen = $2
		 WHERE cred_hash = $1
		 RETURNING `+runnerColumns, credHash, seenAt))
	if err != nil {
		if isPgNoRows(err) {
			return Runner{}, ErrBadRunnerCredential
		}
		return Runner{}, err
	}
	return r, nil
}

func (s *PgRunnerStore) List(ctx context.Context, tenant string) ([]Runner, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runnerColumns+`
		  FROM tenant_runners WHERE tenant = $1 ORDER BY name`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Runner{}
	for rows.Next() {
		r, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PgRunnerStore) Get(ctx context.Context, tenant, name string) (Runner, error) {
	r, err := scanRunner(s.pool.QueryRow(ctx, `
		SELECT `+runnerColumns+`
		  FROM tenant_runners WHERE tenant = $1 AND name = $2`, tenant, name))
	if err != nil {
		if isPgNoRows(err) {
			return Runner{}, ErrRunnerNotFound
		}
		return Runner{}, err
	}
	return r, nil
}

// REPLACES the array; it does not merge.
func (s *PgRunnerStore) SetLabels(ctx context.Context, tenant, name string, labels []string) (Runner, error) {
	if labels == nil {
		labels = []string{}
	}
	r, err := scanRunner(s.pool.QueryRow(ctx, `
		UPDATE tenant_runners SET labels = $3
		 WHERE tenant = $1 AND name = $2
		RETURNING `+runnerColumns, tenant, name, labels))
	if err != nil {
		if isPgNoRows(err) {
			return Runner{}, ErrRunnerNotFound
		}
		return Runner{}, err
	}
	return r, nil
}

func (s *PgRunnerStore) Delete(ctx context.Context, tenant, name string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_runners WHERE tenant = $1 AND name = $2`, tenant, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRunnerNotFound
	}
	return nil
}

// Unspent tokens go too, or a deleted org's token still registers a machine.
func (s *PgRunnerStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM tenant_runners WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM runner_tokens WHERE tenant = $1`, tenant); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgRunnerStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	total := 0
	for _, q := range []string{
		`UPDATE tenant_runners SET created_by = $2 WHERE created_by = $1`,
		`UPDATE runner_tokens  SET created_by = $2 WHERE created_by = $1`,
	} {
		tag, err := tx.Exec(ctx, q, ident, core.ErasedIdentity)
		if err != nil {
			return 0, err
		}
		total += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return total, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

var _ RunnerStore = (*PgRunnerStore)(nil)
