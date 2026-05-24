package jobstore

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/hazy-flow/core"
)

//go:embed schema.sql
var schemaSQL string

// Postgres is the production JobStore. It relies on SELECT ... FOR UPDATE
// SKIP LOCKED for the workqueue and pg_try_advisory_lock for scheduler
// leader election (called by the scheduler, not here).
//
// The implementation is exercised in this repo only when DATABASE_URL is
// set; CI environments should run the schema migration before invoking
// tests. See postgres_test.go for the integration gate.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects via the supplied connection string and applies the
// embedded schema. Production deployments should run migrations through
// their normal tooling instead, but this convenience matches the spec's
// "boot from zero" goal.
func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (s *Postgres) Close() { s.pool.Close() }

func (s *Postgres) Enqueue(ctx context.Context, rec core.JobRecord) error {
	jobJSON, err := json.Marshal(rec.Job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	const q = `
		INSERT INTO jobs (id, graph_id, node_id, tenant, workspace, status, job, enqueued_at)
		VALUES ($1, $2, $3, $4, $5, 'queued', $6::jsonb, COALESCE($7, now()))
	`
	var enqueued any
	if !rec.EnqueuedAt.IsZero() {
		enqueued = rec.EnqueuedAt
	}
	_, err = s.pool.Exec(ctx, q,
		rec.ID, rec.GraphID, rec.NodeID, rec.Tenant, rec.Workspace, jobJSON, enqueued)
	if err != nil {
		return wrapPgErr(err)
	}
	return nil
}

func (s *Postgres) Claim(ctx context.Context, worker string, lease time.Duration) (core.JobRecord, error) {
	const q = `
		UPDATE jobs
		   SET status = 'running',
		       worker_id = $1,
		       attempt = attempt + 1,
		       started_at = now(),
		       lease_until = now() + $2::interval
		 WHERE id = (
		     SELECT id FROM jobs
		      WHERE status = 'queued'
		         OR (status = 'running' AND lease_until < now())
		      ORDER BY enqueued_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING id, graph_id, node_id, tenant, workspace, status, job, result,
		           enqueued_at, started_at, finished_at, attempt, lease_until, worker_id
	`
	row := s.pool.QueryRow(ctx, q, worker, lease.String())
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.JobRecord{}, core.ErrNoJobs
	}
	return rec, err
}

func (s *Postgres) Renew(ctx context.Context, jobID, worker string, lease time.Duration) error {
	const q = `
		UPDATE jobs SET lease_until = now() + $3::interval
		 WHERE id = $1 AND worker_id = $2 AND status = 'running'
	`
	ct, err := s.pool.Exec(ctx, q, jobID, worker, lease.String())
	if err != nil {
		return wrapPgErr(err)
	}
	if ct.RowsAffected() == 0 {
		return core.ErrConflict
	}
	return nil
}

func (s *Postgres) Complete(ctx context.Context, jobID string, status core.JobStatus, result *core.Result) error {
	if status != core.JobStatusSucceeded && status != core.JobStatusFailed && status != core.JobStatusCancelled {
		return core.ErrConflict
	}
	var resJSON any
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("marshal result: %w", err)
		}
		resJSON = b
	}
	const q = `
		UPDATE jobs SET status = $2, result = $3::jsonb, finished_at = now(), lease_until = NULL
		 WHERE id = $1
	`
	ct, err := s.pool.Exec(ctx, q, jobID, string(status), resJSON)
	if err != nil {
		return wrapPgErr(err)
	}
	if ct.RowsAffected() == 0 {
		return core.ErrNotFound
	}
	return nil
}

func (s *Postgres) Get(ctx context.Context, jobID string) (core.JobRecord, error) {
	const q = `
		SELECT id, graph_id, node_id, tenant, workspace, status, job, result,
		       enqueued_at, started_at, finished_at, attempt, lease_until, worker_id
		  FROM jobs WHERE id = $1
	`
	rec, err := scanRecord(s.pool.QueryRow(ctx, q, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.JobRecord{}, core.ErrNotFound
	}
	return rec, err
}

func (s *Postgres) ListByGraph(ctx context.Context, graphID string) ([]core.JobRecord, error) {
	const q = `
		SELECT id, graph_id, node_id, tenant, workspace, status, job, result,
		       enqueued_at, started_at, finished_at, attempt, lease_until, worker_id
		  FROM jobs WHERE graph_id = $1 ORDER BY enqueued_at DESC
	`
	rows, err := s.pool.Query(ctx, q, graphID)
	if err != nil {
		return nil, wrapPgErr(err)
	}
	defer rows.Close()
	var out []core.JobRecord
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Row covers both pgx.Row (QueryRow) and pgx.Rows (Query) so scanRecord can
// serve both call sites.
type row interface {
	Scan(dest ...any) error
}

func scanRecord(r row) (core.JobRecord, error) {
	var (
		rec        core.JobRecord
		jobJSON    []byte
		resultJSON []byte
		started    *time.Time
		finished   *time.Time
		lease      *time.Time
	)
	if err := r.Scan(
		&rec.ID, &rec.GraphID, &rec.NodeID, &rec.Tenant, &rec.Workspace,
		&rec.Status, &jobJSON, &resultJSON,
		&rec.EnqueuedAt, &started, &finished, &rec.Attempt, &lease, &rec.WorkerID,
	); err != nil {
		return core.JobRecord{}, err
	}
	if err := json.Unmarshal(jobJSON, &rec.Job); err != nil {
		return core.JobRecord{}, fmt.Errorf("unmarshal job: %w", err)
	}
	if len(resultJSON) > 0 {
		var res core.Result
		if err := json.Unmarshal(resultJSON, &res); err != nil {
			return core.JobRecord{}, fmt.Errorf("unmarshal result: %w", err)
		}
		rec.Result = &res
	}
	rec.StartedAt = started
	rec.FinishedAt = finished
	rec.LeaseUntil = lease
	return rec, nil
}

func wrapPgErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("postgres: %w", err)
}
