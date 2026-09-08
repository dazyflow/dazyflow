// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

//go:embed schema.sql
var schemaSQL string

// Relies on SELECT ... FOR UPDATE SKIP LOCKED for the workqueue and
// pg_try_advisory_lock for scheduler leader election. Exercised only when
// DATABASE_URL is set.
type Postgres struct {
	pool          *pgxpool.Pool
	ownsPool      bool
	maxConcurrent int
	burstSpacing  time.Duration
}

// The queue distance between two steps one org has waiting, so a burst of N
// spans N×spacing and competes as if it had arrived one step per spacing. Not
// smaller: GREATEST(now, tail+spacing) only spreads a burst while the tail
// outruns the clock, and at 10ms a real burst degraded to FIFO on the load rig.
const DefaultBurstSpacing = 100 * time.Millisecond

func (s *Postgres) SetBurstSpacing(d time.Duration) { s.burstSpacing = d }

// SOFT cap: the running count is read in the claiming statement but not locked,
// so a race can briefly reach cap+1. Expired-lease reclaims are exempt. Set once
// at startup.
func (s *Postgres) SetMaxConcurrentPerTenant(n int) { s.maxConcurrent = n }

func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	store, err := NewPostgresFromPool(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	store.ownsPool = true
	return store, nil
}

func NewPostgresFromPool(ctx context.Context, pool *pgxpool.Pool) (*Postgres, error) {
	if pool == nil {
		return nil, fmt.Errorf("nil pool")
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Postgres{pool: pool, burstSpacing: DefaultBurstSpacing}, nil
}

func (s *Postgres) Close() {
	if s.ownsPool {
		s.pool.Close()
	}
}

// slot_at places a queued step one spacing behind the last its org has waiting,
// so an org queueing a thousand at once does not put all of them ahead of
// everyone else's next step.
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
	var resJSON any
	if rec.Result != nil {
		b, merr := json.Marshal(rec.Result)
		if merr != nil {
			return fmt.Errorf("marshal result: %w", merr)
		}
		resJSON = b
	}
	var finished, started any
	if core.IsTerminalStatus(status) {
		now := time.Now().UTC()
		finished = now
		started = now
	} else if status == core.JobStatusRunning {
		started = time.Now().UTC()
	}
	const q = `
		INSERT INTO jobs (id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result, enqueued_at, started_at, finished_at, parent_node_rec_id, manual, slot_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, COALESCE($12, now()), $13, $14, $15, $16,
		        CASE WHEN $2 = 'node' AND $8 = 'queued' AND $17::interval > '0s'::interval
		            THEN GREATEST(COALESCE($12, now()),
		                          (SELECT max(b.slot_at) FROM jobs b WHERE b.kind = 'node' AND b.status = 'queued' AND b.tenant = $6) + $17::interval)
		            ELSE COALESCE($12, now()) END)
	`
	var enqueued any
	if !rec.EnqueuedAt.IsZero() {
		enqueued = rec.EnqueuedAt
	}
	_, err = s.pool.Exec(ctx, q,
		rec.ID, string(kind), rec.GraphRunID, rec.GraphID, rec.NodeID,
		rec.Tenant, rec.Workspace, string(status), jobJSON, graphPayload, resJSON, enqueued, started, finished, rec.ParentNodeRecID, rec.Manual,
		s.burstSpacing.String())
	if err != nil {
		return wrapPgErr(err)
	}
	return nil
}

// One statement and so one commit — see core.NodeBatchEnqueuer. Deliberately NOT
// ON CONFLICT DO NOTHING: a duplicate must fail the whole statement so the caller
// falls back and finds out which record it was.
func (s *Postgres) EnqueueNodes(ctx context.Context, recs []core.JobRecord) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	ids := make([]string, len(recs))
	runs := make([]string, len(recs))
	graphs := make([]string, len(recs))
	nodes := make([]string, len(recs))
	works := make([]string, len(recs))
	jobs := make([]string, len(recs))
	for i, r := range recs {
		b, err := json.Marshal(r.Job)
		if err != nil {
			return 0, fmt.Errorf("marshal job: %w", err)
		}
		ids[i], runs[i], graphs[i], nodes[i], works[i], jobs[i] =
			r.ID, r.GraphRunID, r.GraphID, r.NodeID, r.Workspace, string(b)
	}
	const q = `
		WITH tail AS (
			SELECT max(b.slot_at) AS at FROM jobs b
			 WHERE b.kind = 'node' AND b.status = 'queued' AND b.tenant = $1
		), ins AS (
			INSERT INTO jobs (id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, enqueued_at, slot_at)
			SELECT d.id, 'node', d.run, d.graph, d.node, $1, d.workspace, 'queued', d.job::jsonb, now(),
			       CASE WHEN $8::interval > '0s'::interval
			            THEN GREATEST(now(), (SELECT at FROM tail) + $8::interval) + $8::interval * (d.ord - 1)
			            ELSE now() END
			  FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[])
			       WITH ORDINALITY AS d(id, run, graph, node, workspace, job, ord)
			RETURNING id
		)
		SELECT count(*) FROM ins
	`
	var n int
	if err := s.pool.QueryRow(ctx, q,
		recs[0].Tenant, ids, runs, graphs, nodes, works, jobs, s.burstSpacing.String(),
	).Scan(&n); err != nil {
		return 0, wrapPgErr(err)
	}
	return n, nil
}

func (s *Postgres) Claim(ctx context.Context, worker string, lease time.Duration) (core.JobRecord, error) {
	var row pgx.Row
	if s.maxConcurrent > 0 {
		row = s.pool.QueryRow(ctx, claimCappedQuery, worker, lease.String(), s.maxConcurrent)
	} else {
		row = s.pool.QueryRow(ctx, claimQuery, worker, lease.String())
	}
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.JobRecord{}, core.ErrNoJobs
	}
	return rec, err
}

const claimReturning = `id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
		           enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual`

const claimQuery = `
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
		      ORDER BY slot_at, enqueued_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING ` + claimReturning

// A queued job is claimable only if its tenant has fewer than $3 running.
// Expired-lease reclaims bypass the cap, being recovery of existing work.
const claimCappedQuery = `
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
		              (status = 'queued' AND (available_at IS NULL OR available_at <= now())
		                AND (SELECT count(*) FROM jobs r
		                      WHERE r.tenant = jobs.tenant
		                        AND r.kind = 'node'
		                        AND r.status = 'running'
		                        AND r.lease_until > now()) < $3)
		           OR (status = 'running' AND lease_until < now())
		            )
		      ORDER BY slot_at, enqueued_at
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1
		 )
		 RETURNING ` + claimReturning

// Deletes a RUN whole, once the RUN has been finished for longer than the window
// — an unfinished run is never touched, however old its steps. Keying on each
// row's own finished_at deleted the succeeded steps out from under a run parked
// on an approval. olderThan <= 0 is a no-op.
func (s *Postgres) PruneTerminal(ctx context.Context, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	cutoff := time.Now().Add(-olderThan)
	total := 0
	for {
		ids, err := s.oldTerminalRunIDs(ctx, cutoff, batch)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}
		n, err := s.deleteRuns(ctx, ids)
		total += n
		if err != nil {
			return total, err
		}
		if len(ids) < batch {
			break
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}
	n, err := s.pruneOrphanNodes(ctx, cutoff, batch)
	return total + n, err
}

func (s *Postgres) oldTerminalRunIDs(ctx context.Context, cutoff time.Time, batch int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM jobs
		  WHERE kind = 'graph'
		    AND finished_at IS NOT NULL AND finished_at < $1
		    AND status IN ('succeeded','failed','cancelled','skipped')
		  LIMIT $2`, cutoff, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// One statement, so a run is never left half-deleted. Node-record status is
// deliberately unfiltered: the run is terminal, so a non-terminal row under it is
// a remnant.
func (s *Postgres) deleteRuns(ctx context.Context, ids []string) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM jobs WHERE id = ANY($1) OR graph_run_id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Node-records whose graph-record is gone have nothing to key retention on but
// their own finished_at. One whose parent still exists is never touched here.
func (s *Postgres) pruneOrphanNodes(ctx context.Context, cutoff time.Time, batch int) (int, error) {
	total := 0
	for {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM jobs WHERE id IN (
			     SELECT j.id FROM jobs j
			      WHERE j.kind = 'node'
			        AND j.finished_at IS NOT NULL AND j.finished_at < $1
			        AND j.status IN ('succeeded','failed','cancelled','skipped')
			        AND NOT EXISTS (SELECT 1 FROM jobs p WHERE p.id = j.graph_run_id)
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

// Claims and reads in ONE statement, so two replicas sweeping the same instant
// cannot both hand back the same run.
func (s *Postgres) ClaimUnnotified(ctx context.Context, lookback time.Duration, maxAttempts, limit int) ([]core.JobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	since := time.Now().Add(-lookback)
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM jobs
			 WHERE kind = 'graph'
			   AND notified_at IS NULL
			   AND status IN ('failed','cancelled')
			   AND finished_at IS NOT NULL
			   AND finished_at >= $1
			   AND notify_attempts < $2
			 ORDER BY finished_at
			 LIMIT $3
			 FOR UPDATE SKIP LOCKED
		), claimed AS (
			UPDATE jobs SET notified_at = now(), notify_attempts = notify_attempts + 1
			 WHERE id IN (SELECT id FROM due)
			 RETURNING `+claimReturning+`
		)
		SELECT * FROM claimed`, since, maxAttempts, limit)
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

func (s *Postgres) ReleaseNotifyClaim(ctx context.Context, jobID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET notified_at = NULL WHERE id = $1`, jobID)
	return err
}

// Ignores status, unlike PruneTerminal: it backs the GDPR erasure cascade, so an
// in-flight run goes with the tenant. Callers should cancel active runs first.
func (s *Postgres) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Postgres) OldestQueuedEnqueuedAt(ctx context.Context) (time.Time, bool, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT enqueued_at FROM jobs
		  WHERE kind = 'node' AND status = 'queued'
		    AND (available_at IS NULL OR available_at <= now())
		  ORDER BY enqueued_at
		  LIMIT 1`).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

func (s *Postgres) CountsByStatus(ctx context.Context) (map[core.JobStatus]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM jobs WHERE kind = 'node' GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[core.JobStatus]int)
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[core.JobStatus(status)] = n
	}
	return out, rows.Err()
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
	return s.complete(ctx, jobID, "", status, result)
}

func (s *Postgres) CompleteOwned(ctx context.Context, jobID, worker string, status core.JobStatus, result *core.Result) error {
	return s.complete(ctx, jobID, worker, status, result)
}

// One statement: a data-modifying CTE completes the node, reads its run's status
// and inserts the dependents. An existing dependent is skipped by ON CONFLICT,
// and none are inserted when the run record is already terminal.
func (s *Postgres) CompleteAndEnqueue(ctx context.Context, jobID, worker string, status core.JobStatus, result *core.Result, deps []core.JobRecord) (core.Advance, error) {
	if !core.IsTerminalStatus(status) && status != core.JobStatusAwaiting {
		return core.Advance{}, core.ErrConflict
	}
	var resJSON any
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return core.Advance{}, fmt.Errorf("marshal result: %w", err)
		}
		resJSON = b
	}
	n := len(deps)
	ids, runs, graphs, nodes, tenants, workspaces, jobs := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, d := range deps {
		jobJSON, err := json.Marshal(d.Job)
		if err != nil {
			return core.Advance{}, fmt.Errorf("marshal job: %w", err)
		}
		ids[i], runs[i], graphs[i], nodes[i], tenants[i], workspaces[i], jobs[i] = d.ID, d.GraphRunID, d.GraphID, d.NodeID, d.Tenant, d.Workspace, string(jobJSON)
	}
	finishedClause := "finished_at = now()"
	terminalGuard := "'succeeded','failed','cancelled'"
	if status == core.JobStatusAwaiting {
		finishedClause = "finished_at = finished_at"
		terminalGuard += ",'awaiting'"
	}
	fence := ""
	args := []any{jobID, string(status), resJSON, ids, runs, graphs, nodes, tenants, workspaces, jobs, s.burstSpacing.String()}
	if worker != "" {
		fence = " AND worker_id = $12"
		args = append(args, worker)
	}
	q := `
		WITH done AS (
			UPDATE jobs SET status = $2, result = $3::jsonb, ` + finishedClause + `, lease_until = NULL
			 WHERE id = $1 AND status NOT IN (` + terminalGuard + `)` + fence + `
			RETURNING graph_run_id, tenant
		), run AS (
			SELECT g.status FROM jobs g JOIN done ON g.id = done.graph_run_id
		), tail AS (
			SELECT max(b.slot_at) AS at FROM jobs b JOIN done ON b.tenant = done.tenant
			 WHERE b.kind = 'node' AND b.status = 'queued'
		), ins AS (
			INSERT INTO jobs (id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, slot_at)
			SELECT d.id, 'node', d.run, d.graph, d.node, d.tenant, d.workspace, 'queued', d.job::jsonb,
			       CASE WHEN $11::interval > '0s'::interval
			            THEN GREATEST(now(), (SELECT at FROM tail) + $11::interval) + $11::interval * (d.ord - 1)
			            ELSE now() END
			  FROM unnest($4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[], $10::text[])
			       WITH ORDINALITY AS d(id, run, graph, node, tenant, workspace, job, ord)
			 WHERE EXISTS (SELECT 1 FROM done)
			   AND NOT EXISTS (SELECT 1 FROM run WHERE run.status IN ('succeeded','failed','cancelled'))
			ON CONFLICT (id) DO NOTHING
			RETURNING id
		)
		SELECT (SELECT count(*) FROM done), (SELECT status FROM run), (SELECT count(*) FROM ins)
	`
	var completed, enqueued int
	var runStatus *string
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&completed, &runStatus, &enqueued); err != nil {
		return core.Advance{}, wrapPgErr(err)
	}
	if completed == 0 {
		var exists bool
		_ = s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1)", jobID).Scan(&exists)
		if !exists {
			return core.Advance{}, core.ErrNotFound
		}
		return core.Advance{}, core.ErrConflict
	}
	adv := core.Advance{Enqueued: enqueued}
	if runStatus != nil {
		adv.RunStatus = core.JobStatus(*runStatus)
	}
	return adv, nil
}

// Refuses to overwrite a terminal record, and fences on lease ownership when
// worker is set. Parking is fenced against an ALREADY parked record too, or the
// park hook mails the approvers twice for one pause.
func (s *Postgres) complete(ctx context.Context, jobID, worker string, status core.JobStatus, result *core.Result) error {
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
	finishedClause := "finished_at = now()"
	if status == core.JobStatusAwaiting {
		finishedClause = "finished_at = finished_at"
	}
	terminalGuard := "'succeeded','failed','cancelled'"
	if status == core.JobStatusAwaiting {
		terminalGuard += ",'awaiting'"
	}
	q := `
		UPDATE jobs SET status = $2, result = $3::jsonb, ` + finishedClause + `, lease_until = NULL
		 WHERE id = $1 AND status NOT IN (` + terminalGuard + `)
	`
	args := []any{jobID, string(status), resJSON}
	if worker != "" {
		q += " AND worker_id = $4"
		args = append(args, worker)
	}
	ct, err := s.pool.Exec(ctx, q, args...)
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

func (s *Postgres) Get(ctx context.Context, jobID string) (core.JobRecord, error) {
	const q = `
		SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
		       enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
		  FROM jobs WHERE id = $1
	`
	rec, err := scanRecord(s.pool.QueryRow(ctx, q, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.JobRecord{}, core.ErrNotFound
	}
	return rec, err
}

func (s *Postgres) Outcomes(ctx context.Context, jobIDs []string) (map[string]core.NodeOutcome, error) {
	if len(jobIDs) == 0 {
		return nil, nil
	}
	const q = `SELECT id, status, result FROM jobs WHERE id = ANY($1)`
	rows, err := s.pool.Query(ctx, q, jobIDs)
	if err != nil {
		return nil, wrapPgErr(err)
	}
	defer rows.Close()
	out := make(map[string]core.NodeOutcome, len(jobIDs))
	for rows.Next() {
		var (
			id         string
			status     string
			resultJSON []byte
		)
		if err := rows.Scan(&id, &status, &resultJSON); err != nil {
			return nil, err
		}
		oc := core.NodeOutcome{Status: core.JobStatus(status)}
		if len(resultJSON) > 0 {
			var res core.Result
			if err := json.Unmarshal(resultJSON, &res); err != nil {
				return nil, fmt.Errorf("unmarshal result %s: %w", id, err)
			}
			oc.Result = &res
		}
		out[id] = oc
	}
	return out, rows.Err()
}

func (s *Postgres) MarkGraphRunning(ctx context.Context, jobID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status = 'running', started_at = COALESCE(started_at, now())
		   WHERE id = $1 AND kind = 'graph' AND status = 'queued'`, jobID)
	if err != nil {
		return false, wrapPgErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Postgres) SetGraphRunParked(ctx context.Context, graphRunID string, parked bool) (bool, error) {
	from, to := core.JobStatusRunning, core.JobStatusAwaiting
	if !parked {
		from, to = core.JobStatusAwaiting, core.JobStatusRunning
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status = $2 WHERE id = $1 AND kind = 'graph' AND status = $3`,
		graphRunID, string(to), string(from))
	if err != nil {
		return false, wrapPgErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Postgres) ListByGraph(ctx context.Context, graphID string) ([]core.JobRecord, error) {
	const q = `
		SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
		       enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
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

func graphRunPredicates(q string, args []any, opts core.ListGraphRunsOpts) (string, []any) {
	if opts.Tenant != "" {
		args = append(args, opts.Tenant)
		q += fmt.Sprintf(" AND tenant = $%d", len(args))
	}
	if opts.Workspace != "" {
		args = append(args, opts.Workspace)
		q += fmt.Sprintf(" AND workspace = $%d", len(args))
	}
	if opts.GraphID != "" {
		args = append(args, opts.GraphID)
		q += fmt.Sprintf(" AND graph_id = $%d", len(args))
	}
	if opts.Status != "" {
		args = append(args, string(opts.Status))
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if !opts.Since.IsZero() {
		args = append(args, opts.Since)
		q += fmt.Sprintf(" AND enqueued_at >= $%d", len(args))
	}
	if !opts.Until.IsZero() {
		args = append(args, opts.Until)
		q += fmt.Sprintf(" AND enqueued_at < $%d", len(args))
	}
	return q, args
}

func graphRunOrderLimit(q string, args []any, opts core.ListGraphRunsOpts) (string, []any) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY enqueued_at DESC, id DESC LIMIT $%d", len(args))
	if opts.Offset > 0 {
		args = append(args, opts.Offset)
		q += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	return q, args
}

func (s *Postgres) ListGraphRuns(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	q := `SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
	             enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
	        FROM jobs WHERE kind = 'graph'`
	q, args := graphRunPredicates(q, []any{}, opts)
	q, args = graphRunOrderLimit(q, args, opts)
	rows, err := s.pool.Query(ctx, q, args...)
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

func (s *Postgres) ListGraphRunSummaries(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.RunSummary, error) {
	q, args := graphRunPredicates(summarySelect, []any{}, opts)
	q, args = graphRunOrderLimit(q, args, opts)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, wrapPgErr(err)
	}
	defer rows.Close()
	var out []core.RunSummary
	for rows.Next() {
		sum, err := scanSummary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, rows.Err()
}

const summaryCols = `SELECT id, graph_id, tenant, workspace, status, enqueued_at, started_at, finished_at,
	             result->'error'
	        FROM jobs WHERE `
const summarySelect = summaryCols + `kind = 'graph'`
const summaryByID = summaryCols + `id = $1`

func isJSONNull(b []byte) bool { return len(b) == 0 || string(b) == "null" }

func scanSummary(r row) (core.RunSummary, error) {
	var (
		sum     core.RunSummary
		errJSON []byte
	)
	if err := r.Scan(&sum.ID, &sum.GraphID, &sum.Tenant, &sum.Workspace, &sum.Status,
		&sum.EnqueuedAt, &sum.StartedAt, &sum.FinishedAt, &errJSON); err != nil {
		return core.RunSummary{}, err
	}
	if !isJSONNull(errJSON) {
		var je core.JobError
		if err := json.Unmarshal(errJSON, &je); err != nil {
			return core.RunSummary{}, fmt.Errorf("unmarshal run error: %w", err)
		}
		sum.Error = &je
	}
	return sum, nil
}

func (s *Postgres) GetGraphRunSummary(ctx context.Context, jobID string) (core.RunSummary, error) {
	sum, err := scanSummary(s.pool.QueryRow(ctx, summaryByID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.RunSummary{}, core.ErrNotFound
	}
	if err != nil {
		return core.RunSummary{}, wrapPgErr(err)
	}
	return sum, nil
}

func (s *Postgres) CountGraphRuns(ctx context.Context, opts core.ListGraphRunsOpts) (int, error) {
	q := `SELECT 1 FROM jobs WHERE kind = 'graph'`
	q, args := graphRunPredicates(q, []any{}, opts)
	if opts.Limit > 0 {
		args = append(args, opts.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (`+q+`) capped`, args...).Scan(&n); err != nil {
		return 0, wrapPgErr(err)
	}
	return n, nil
}

func (s *Postgres) ListNodeRuns(ctx context.Context, graphRunID string, limit int) ([]core.NodeRun, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `SELECT node_id, status, job -> 'input', result, started_at, finished_at, attempt, available_at
	             FROM jobs
	            WHERE kind = 'node' AND graph_run_id = $1
	            ORDER BY enqueued_at DESC, id DESC
	            LIMIT $2`
	rows, err := s.pool.Query(ctx, q, graphRunID, limit)
	if err != nil {
		return nil, wrapPgErr(err)
	}
	defer rows.Close()
	out := make([]core.NodeRun, 0, 32)
	for rows.Next() {
		var (
			n          core.NodeRun
			inputJSON  []byte
			resultJSON []byte
		)
		if err := rows.Scan(&n.NodeID, &n.Status, &inputJSON, &resultJSON,
			&n.StartedAt, &n.FinishedAt, &n.Attempt, &n.AvailableAt); err != nil {
			return nil, wrapPgErr(err)
		}
		if !isJSONNull(inputJSON) {
			if err := json.Unmarshal(inputJSON, &n.Inputs); err != nil {
				return nil, fmt.Errorf("unmarshal node input: %w", err)
			}
		}
		if !isJSONNull(resultJSON) {
			var res core.Result
			if err := json.Unmarshal(resultJSON, &res); err != nil {
				return nil, fmt.Errorf("unmarshal result: %w", err)
			}
			n.Result = &res
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgErr(err)
	}
	return out, nil
}

func (s *Postgres) ListNodeRecords(ctx context.Context, opts core.ListNodeRecordsOpts) ([]core.JobRecord, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	q, args := nodeRecordWhere(`SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
	             enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
	        FROM jobs WHERE kind = 'node'`, opts)
	args = append(args, limit)
	order := "enqueued_at DESC, id DESC"
	if opts.NewestByFinished {
		order = "finished_at DESC NULLS LAST, id DESC"
	}
	q += fmt.Sprintf(" ORDER BY %s LIMIT $%d", order, len(args))
	if opts.Offset > 0 {
		args = append(args, opts.Offset)
		q += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	rows, err := s.pool.Query(ctx, q, args...)
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

func nodeRecordWhere(q string, opts core.ListNodeRecordsOpts) (string, []any) {
	args := []any{}
	if opts.Tenant != "" {
		args = append(args, opts.Tenant)
		q += fmt.Sprintf(" AND tenant = $%d", len(args))
	}
	if opts.Workspace != "" {
		args = append(args, opts.Workspace)
		q += fmt.Sprintf(" AND workspace = $%d", len(args))
	}
	if opts.Status != "" {
		args = append(args, string(opts.Status))
		q += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if opts.GraphRunID != "" {
		args = append(args, opts.GraphRunID)
		q += fmt.Sprintf(" AND graph_run_id = $%d", len(args))
	}
	if opts.GraphID != "" {
		args = append(args, opts.GraphID)
		q += fmt.Sprintf(" AND graph_id = $%d", len(args))
	}
	if opts.HasOutputPort != "" {
		args = append(args, opts.HasOutputPort)
		q += fmt.Sprintf(" AND jsonb_exists(result->'output', $%d)", len(args))
	}
	return q, args
}

func (s *Postgres) CountNodeRecords(ctx context.Context, opts core.ListNodeRecordsOpts) (int, error) {
	q, args := nodeRecordWhere(`SELECT 1 FROM jobs WHERE kind = 'node'`, opts)
	if opts.Offset > 0 {
		args = append(args, opts.Offset)
		q += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	if opts.Limit > 0 {
		args = append(args, opts.Limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (`+q+`) AS capped`, args...).Scan(&n)
	if err != nil {
		return 0, wrapPgErr(err)
	}
	return n, nil
}

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
		&rec.Manual,
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

const pgUniqueViolation = "23505"

func wrapPgErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return fmt.Errorf("postgres: %w: %v", core.ErrConflict, err)
	}
	return fmt.Errorf("postgres: %w", err)
}
