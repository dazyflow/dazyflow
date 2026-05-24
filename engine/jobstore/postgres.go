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
	kind := rec.Kind
	if kind == "" {
		kind = core.JobKindGraph
	}
	status := rec.Status
	if status == "" {
		status = core.JobStatusQueued
	}
	var graphPayload any
	if len(rec.GraphPayload) > 0 {
		graphPayload = rec.GraphPayload
	}
	const q = `
		INSERT INTO jobs (id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, enqueued_at, parent_node_rec_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, COALESCE($11, now()), $12)
	`
	var enqueued any
	if !rec.EnqueuedAt.IsZero() {
		enqueued = rec.EnqueuedAt
	}
	_, err = s.pool.Exec(ctx, q,
		rec.ID, string(kind), rec.GraphRunID, rec.GraphID, rec.NodeID,
		rec.Tenant, rec.Workspace, string(status), jobJSON, graphPayload, enqueued, rec.ParentNodeRecID)
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
		      WHERE kind = 'node'
		        AND (
		              (status = 'queued' AND (available_at IS NULL OR available_at <= now()))
		           OR (status = 'running' AND lease_until < now())
		            )
		      ORDER BY enqueued_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
		           enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id
	`
	row := s.pool.QueryRow(ctx, q, worker, lease.String())
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.JobRecord{}, core.ErrNoJobs
	}
	return rec, err
}

func (s *Postgres) Requeue(ctx context.Context, jobID string, availableAt time.Time) error {
	const q = `
		UPDATE jobs
		   SET status = 'queued',
		       available_at = $2,
		       lease_until = NULL,
		       result = NULL
		 WHERE id = $1
		   AND status NOT IN ('succeeded','failed','cancelled')
	`
	ct, err := s.pool.Exec(ctx, q, jobID, availableAt)
	if err != nil {
		return wrapPgErr(err)
	}
	if ct.RowsAffected() == 0 {
		var exists bool
		_ = s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1)", jobID).Scan(&exists)
		if !exists {
			return core.ErrNotFound
		}
		return core.ErrConflict
	}
	return nil
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
	if !core.IsTerminalStatus(status) && status != core.JobStatusAwaiting {
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
	// Awaiting parks the record without finishing it — finished_at stays
	// NULL so the resume path can mark it later. Terminal writes set it.
	finishedClause := "finished_at = now()"
	if status == core.JobStatusAwaiting {
		finishedClause = "finished_at = finished_at"
	}
	// Refuse to overwrite an already-terminal record. Awaiting and skipped
	// are NOT included in the guard so the resume path (awaiting → succeeded)
	// works.
	q := `
		UPDATE jobs SET status = $2, result = $3::jsonb, ` + finishedClause + `, lease_until = NULL
		 WHERE id = $1 AND status NOT IN ('succeeded','failed','cancelled')
	`
	ct, err := s.pool.Exec(ctx, q, jobID, string(status), resJSON)
	if err != nil {
		return wrapPgErr(err)
	}
	if ct.RowsAffected() == 0 {
		// Either the record doesn't exist or it's already terminal.
		var exists bool
		_ = s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1)", jobID).Scan(&exists)
		if !exists {
			return core.ErrNotFound
		}
		return core.ErrConflict
	}
	return nil
}

func (s *Postgres) Get(ctx context.Context, jobID string) (core.JobRecord, error) {
	const q = `
		SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
		       enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id
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
		SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
		       enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id
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
		kind       string
		jobJSON    []byte
		graphJSON  []byte
		resultJSON []byte
		available  *time.Time
		started    *time.Time
		finished   *time.Time
		lease      *time.Time
	)
	if err := r.Scan(
		&rec.ID, &kind, &rec.GraphRunID, &rec.GraphID, &rec.NodeID, &rec.Tenant, &rec.Workspace,
		&rec.Status, &jobJSON, &graphJSON, &resultJSON,
		&rec.EnqueuedAt, &available, &started, &finished, &rec.Attempt, &lease, &rec.WorkerID,
		&rec.ParentNodeRecID,
	); err != nil {
		return core.JobRecord{}, err
	}
	rec.AvailableAt = available
	rec.Kind = core.JobKind(kind)
	if err := json.Unmarshal(jobJSON, &rec.Job); err != nil {
		return core.JobRecord{}, fmt.Errorf("unmarshal job: %w", err)
	}
	if len(graphJSON) > 0 {
		rec.GraphPayload = graphJSON
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
