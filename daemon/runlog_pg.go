// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"time"

	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PgRunLogStore struct {
	pool *pgxpool.Pool
}

const pgRunLogSchema = `
CREATE TABLE IF NOT EXISTS run_logs (
    seq     BIGSERIAL PRIMARY KEY,
    run_id  TEXT NOT NULL,
    ts      TIMESTAMPTZ NOT NULL DEFAULT now(),
    node_id TEXT NOT NULL DEFAULT '',
    kind    TEXT NOT NULL,
    stream  TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL
);
ALTER TABLE run_logs ADD COLUMN IF NOT EXISTS stream TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS run_logs_run_idx ON run_logs (run_id, seq);
-- Retention's predicate. run_logs_run_idx leads with run_id, so it cannot
-- serve a sweep that only knows a cutoff time.
CREATE INDEX IF NOT EXISTS run_logs_ts_idx ON run_logs (ts);
`

func NewPgRunLogStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunLogStore, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgRunLogSchema); err != nil {
		return nil, err
	}
	return &PgRunLogStore{pool: pool}, nil
}

func (s *PgRunLogStore) AppendRunLog(ctx context.Context, e RunLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO run_logs (run_id, ts, node_id, kind, stream, message)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		e.RunID, e.TS, e.NodeID, e.Kind, e.Stream, e.Message)
	return err
}

func (s *PgRunLogStore) ListRunLogs(ctx context.Context, runID string, afterSeq int64, limit int) ([]RunLogEntry, error) {
	if limit <= 0 {
		limit = defaultRunLogPage
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, run_id, ts, node_id, kind, stream, message
		FROM run_logs
		WHERE run_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT $3`,
		runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunLogEntry
	for rows.Next() {
		var e RunLogEntry
		if err := rows.Scan(&e.Seq, &e.RunID, &e.TS, &e.NodeID, &e.Kind, &e.Stream, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PgRunLogStore) DeleteRun(ctx context.Context, runID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM run_logs WHERE run_id = $1`, runID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgRunLogStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM run_logs WHERE run_id IN (SELECT id FROM jobs WHERE tenant = $1)`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgRunLogStore) Prune(ctx context.Context, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	cutoff := time.Now().Add(-olderThan)
	total := 0
	for {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM run_logs WHERE seq IN (
			     SELECT rl.seq FROM run_logs rl
			     JOIN jobs j ON j.id = rl.run_id
			      WHERE j.kind = 'graph'
			        AND j.finished_at IS NOT NULL AND j.finished_at < $1
			        AND j.status IN ('succeeded','failed','cancelled','skipped')
			      LIMIT $2)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			break
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}
	n, err := s.pruneOrphanLogs(ctx, cutoff, batch)
	return total + n, err
}

// pruneOrphanLogs deletes old lines whose run record is already gone. The
// retention sweep prunes jobs before run logs, so with the windows at their
// shared default the run row usually disappears first and this pass — not the
// join above — collects the log. A line is only ever written before its run
// finishes, so an orphan older than the cutoff belongs to a run that finished
// before it too.
func (s *PgRunLogStore) pruneOrphanLogs(ctx context.Context, cutoff time.Time, batch int) (int, error) {
	total := 0
	for {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM run_logs WHERE seq IN (
			     SELECT rl.seq FROM run_logs rl
			      WHERE rl.ts < $1
			        AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id = rl.run_id)
			      LIMIT $2)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			return total, nil
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}
}

// PruneTenant deletes a single tenant's finished runs' logs, in batches.
// run_logs has no tenant column, so it scopes through the jobs join (same as
// DeleteByTenant). The retention sweep uses it to apply a shorter per-tenant
// window than the global cap (free tenants keep less history than paying ones).
//
// Run-scoped like Prune. It cannot collect orphans — a line whose run record is
// gone has no tenant to match — so the global pass is what reaches those.
func (s *PgRunLogStore) PruneTenant(ctx context.Context, tenant string, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 || tenant == "" {
		return 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	cutoff := time.Now().Add(-olderThan)
	total := 0
	for {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM run_logs WHERE seq IN (
			     SELECT rl.seq FROM run_logs rl JOIN jobs j ON j.id = rl.run_id
			      WHERE j.tenant = $2
			        AND j.kind = 'graph'
			        AND j.finished_at IS NOT NULL AND j.finished_at < $1
			        AND j.status IN ('succeeded','failed','cancelled','skipped')
			      LIMIT $3)`, cutoff, tenant, batch)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			return total, nil
		}
	}
}

func (s *PgRunLogStore) RunLogTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT tenant FROM jobs WHERE tenant <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
