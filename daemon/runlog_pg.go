package daemon

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgRunLogStore is the durable RunLogStore — one row per log entry,
// BIGSERIAL seq for ordering/resume. Append-only at runtime; retention
// is an operator sweep (see the TODO note), not a per-write concern.
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
`

func NewPgRunLogStore(ctx context.Context, pool *pgxpool.Pool) (*PgRunLogStore, error) {
	if err := applyPgSchema(ctx, pool, pgRunLogSchema); err != nil {
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

// DeleteRun removes every log line for one run — backs the per-run
// log-deletion endpoint (GDPR P2.1) and the erasure cascade.
func (s *PgRunLogStore) DeleteRun(ctx context.Context, runID string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM run_logs WHERE run_id = $1`, runID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DeleteByTenant removes the logs of every run owned by a tenant. run_logs
// has no tenant column, so it joins the jobs table (same database) to scope
// by run ownership. Part of the org/account erasure cascade (Art. 17).
func (s *PgRunLogStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM run_logs WHERE run_id IN (SELECT id FROM jobs WHERE tenant = $1)`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Prune deletes entries older than the cutoff in batches, returning the
// total removed. Same shape as the jobs/audit retention pruners; wired
// into dzd's hourly retention sweep behind DAZYFLOW_RUN_LOG_RETENTION.
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
			     SELECT seq FROM run_logs WHERE ts < $1 LIMIT $2)`, cutoff, batch)
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

// PruneTenant deletes a single tenant's run-log entries older than the cutoff,
// in batches. run_logs has no tenant column, so it scopes through the jobs
// join (same as DeleteByTenant). The retention sweep uses it to apply a
// shorter per-tenant window than the global cap (free tenants keep less
// history than paying ones).
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
			     WHERE rl.ts < $1 AND j.tenant = $2 LIMIT $3)`, cutoff, tenant, batch)
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

// RunLogTenants lists the distinct tenants that own jobs (and therefore may
// own run logs). The per-tenant retention sweep iterates it; a tenant with no
// old logs simply prunes nothing.
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
