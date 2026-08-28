// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// isPgUniqueViolation reports whether err is Postgres' unique_violation
// (SQLSTATE 23505). RedeemToken leans on it to turn an open token's INSERT into
// an already-registered name into ErrRunnerNameTaken, rather than reading the
// raw driver error.
func isPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// PgRunnerStore is the durable RunnerStore.
//
// A registration has to outlive the daemon. The agent keeps its credential on
// disk and presents it forever; if the daemon forgot every runner on restart,
// every agent's next poll would be answered "you are not registered" — which
// the agent correctly treats as terminal, because that is what deletion looks
// like. The in-memory store is therefore only usable for tests: a restart with
// it would decommission the whole fleet.
type PgRunnerStore struct {
	pool *pgxpool.Pool
}

func NewPgRunnerStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunnerStore, error) {
	if err := EnsurePgRunnerSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgRunnerStore{pool: pool}, nil
}

// runnerColumns is the select list every read shares, in Runner field order.
const runnerColumns = `tenant, name, labels, version, last_seen, created_by, created_at`

// scanRunner reads runnerColumns. last_seen is nullable — a runner exists
// between being registered and first checking in.
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
	// Sweep tokens that are long past usable while we are here. They are
	// write-once and read once, so nothing else would ever collect them, and a
	// token minted every time someone adds a machine accumulates forever. The
	// day of slack keeps a just-expired token around long enough to still
	// produce its "this has expired" answer rather than "unknown".
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM runner_tokens WHERE expires_at < now() - interval '1 day'`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runner_tokens (token_hash, tenant, created_by, name, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, hash, tenant, createdBy, name, expires)
	return err
}

// RedeemToken spends the token and writes the runner in one transaction.
//
// Both halves must land together. Spending without registering would burn
// someone's only token and leave them nothing; registering without spending
// would make the token reusable, which is the property that makes a token safe
// to paste into a terminal in the first place.
func (s *PgRunnerStore) RedeemToken(ctx context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Runner{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The UPDATE is the claim: `used_at IS NULL` in the WHERE clause means two
	// agents racing on one token cannot both win, because the second finds no
	// row to update. Expiry is checked here too, so an expired token and a
	// spent one are indistinguishable to the caller — deliberately, since
	// telling them apart helps someone probing for a live token.
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

	// Enforce the token's name scoping. A violation returns before commit, so
	// the deferred Rollback un-does the "used" mark above — a mistyped --name
	// or a collision leaves the token usable rather than spending it on a
	// mistake the operator can simply fix and retry.
	//
	// A pinned token registers only its own name. An open token (tokenName
	// == "") registers only a name no runner holds yet — enforced below by a
	// plain INSERT whose unique-violation on (tenant, name) is the collision,
	// rather than an upsert that would silently overwrite the live runner and
	// retire its credential.
	if tokenName != "" && r.Name != tokenName {
		return Runner{}, ErrRunnerNameMismatch
	}

	// The tenant comes from the token, never from the caller.
	r.Tenant = tenant
	r.CreatedBy = createdBy
	// A nil slice reaches Postgres as NULL, and the column is NOT NULL. Every
	// path through Runners.Register normalises labels first and so never sends
	// nil — but the store must not depend on its caller for that, and the
	// memory store accepts nil, so leaving it would make the two disagree.
	labels := r.Labels
	if labels == nil {
		labels = []string{}
	}

	// A pinned token may REPLACE the machine it names — that is how a rebuilt
	// host comes back. Overwriting cred_hash on the same row is what retires
	// the old credential: the unique index means one row holds one credential,
	// so there is no orphan left to clean up. created_at is left out of the SET
	// list so the machine keeps the date it first appeared.
	//
	// An open token may NOT replace: it inserts, and a name already taken is a
	// unique-violation the caller sees as ErrRunnerNameTaken. The two SQL
	// statements differ only in the ON CONFLICT clause, which is precisely the
	// permission that separates "bring a new machine in" from "take an existing
	// one over".
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
			// Returning before commit preserves the token for a retry under a
			// free name.
			return Runner{}, ErrRunnerNameTaken
		}
		return Runner{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Runner{}, err
	}
	return stored, nil
}

// RunnerByCredential resolves a credential and records the check-in in the same
// statement, because every poll does both and "online" is derived from it.
func (s *PgRunnerStore) RunnerByCredential(ctx context.Context, credHash []byte, seenAt time.Time) (Runner, error) {
	r, err := scanRunner(s.pool.QueryRow(ctx, `
		UPDATE tenant_runners
		   SET last_seen = $2
		 WHERE cred_hash = $1
		 RETURNING `+runnerColumns, credHash, seenAt))
	if err != nil {
		// A credential that resolves to nothing is one whose runner was
		// deleted. The agent is told to re-register rather than retry.
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

// SetLabels replaces the label array on one row.
//
// RETURNING rather than a second SELECT: the caller wants the updated runner,
// and doing it in one statement means the answer cannot be another admin's
// concurrent edit.
func (s *PgRunnerStore) SetLabels(ctx context.Context, tenant, name string, labels []string) (Runner, error) {
	// An empty array, not NULL: the column is NOT NULL DEFAULT '{}', and a
	// machine with no labels is the ordinary state of one targeted by name.
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

// Delete removes the runner, which is also the revocation: the credential is
// stored on the row, so deleting the row is what stops the agent — whether or
// not anyone remembers to shut it down.
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

// DeleteByTenant removes an org's runners and its unspent registration tokens
// in one transaction, returning the number of runners removed.
//
// One transaction because the two halves are one revocation: tokens gone but
// runners left is a fleet nobody can re-register, and runners gone but tokens
// left is a live credential for an erased org.
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

// AnonymizeSubject scrubs an erased person from both of this store's tables.
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

// nullTime keeps a zero time out of the database as NULL. "Never checked in"
// and "checked in at the zero instant" read the same in Go but not in SQL, and
// only NULL sorts and displays correctly.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// compile-time check that the durable store satisfies the interface the API
// and dispatcher use, so a drift in either is a build failure.
var _ RunnerStore = (*PgRunnerStore)(nil)
