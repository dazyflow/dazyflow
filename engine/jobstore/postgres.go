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

// Postgres is the production JobStore. It relies on SELECT ... FOR UPDATE
// SKIP LOCKED for the workqueue and pg_try_advisory_lock for scheduler
// leader election (called by the scheduler, not here).
//
// The implementation is exercised in this repo only when DATABASE_URL is
// set; CI environments should run the schema migration before invoking
// tests. See postgres_test.go for the integration gate.
type Postgres struct {
	pool *pgxpool.Pool
	// ownsPool is true only when OpenPostgres created the pool, so
	// Close() knows whether it may shut it down (shared pools are
	// owned by the daemon, not the JobStore).
	ownsPool bool
	// maxConcurrent caps per-tenant running node jobs (0 = unlimited).
	// Set once at startup before any worker claims, so no synchronization
	// is needed for the read in Claim.
	maxConcurrent int
	// burstSpacing spreads one org's burst along the queue; see Enqueue.
	burstSpacing time.Duration
}

// DefaultBurstSpacing is the queue distance between two steps an org has
// waiting at once: a burst of N steps from one org spans N×spacing of queue,
// so it competes with other orgs' steps as if it had arrived at one step per
// spacing (10/s here), and an org with nothing waiting lands ahead of all
// but its first. The order only matters when someone else is waiting: an org
// alone on the queue still gets every worker, whatever its slots say.
//
// Why not smaller: the tail advances one spacing per enqueue, but wall time
// advances too, and GREATEST(now, tail+spacing) only spreads a burst while
// the tail outruns the clock. Under load an enqueue takes a few ms, and
// several enqueues in flight for the same org read the same tail and land on
// the same slot — at 10ms both effects let a real burst degrade to FIFO,
// which is what the load rig showed. 100ms outpaces both by a wide margin.
const DefaultBurstSpacing = 100 * time.Millisecond

// SetBurstSpacing overrides DefaultBurstSpacing. Set once at startup.
func (s *Postgres) SetBurstSpacing(d time.Duration) { s.burstSpacing = d }

// SetMaxConcurrentPerTenant caps how many node jobs a single tenant may
// have running at once. Claim withholds new (queued) work from a tenant
// at the cap; reclaiming an expired lease is exempt. 0 = no cap.
//
// Fairness does not depend on it: the queue order already spreads one org's
// burst so it cannot starve the others (see Enqueue). The cap is a hard
// ceiling on top, for steps that hold a worker without running — waiting on
// the egress budget, say — where any share of the fleet is too much to hand
// one org.
//
// NOTE: this is a best-effort SOFT cap. The per-tenant running count is
// read in the same statement that claims, but it is not locked against
// other concurrent claimers, so a race between workers can briefly let a
// tenant reach cap+1. A hard cap would need per-tenant locking; the soft
// cap is sufficient as a fairness throttle. Set once at startup.
func (s *Postgres) SetMaxConcurrentPerTenant(n int) { s.maxConcurrent = n }

// OpenPostgres connects via the supplied connection string and applies the
// embedded schema. Production deployments should run migrations through
// their normal tooling instead, but this convenience matches the spec's
// "boot from zero" goal.
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

// NewPostgresFromPool builds a JobStore on an already-open pgxpool,
// applying the schema. Lets the daemon share one pool across the
// JobStore, the secret store, and the auth stores (one connection
// budget instead of N). The caller retains ownership of the pool —
// Close() here is a no-op so closing the JobStore doesn't yank the
// pool out from under the other stores.
func NewPostgresFromPool(ctx context.Context, pool *pgxpool.Pool) (*Postgres, error) {
	if pool == nil {
		return nil, fmt.Errorf("nil pool")
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Postgres{pool: pool, burstSpacing: DefaultBurstSpacing}, nil
}

// Close releases the pool only when this store opened it (OpenPostgres).
// When the pool was injected via NewPostgresFromPool the owner closes it.
func (s *Postgres) Close() {
	if s.ownsPool {
		s.pool.Close()
	}
}

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
	// Persist Result at enqueue time. Most records are enqueued queued
	// (no result), but seeded records (SubmitGraphWithSeed pre-completing
	// a webhook_input/trigger node) arrive status=succeeded WITH a result.
	// Dropping it here left the trigger succeeded-but-result-less, so a
	// downstream node's load_predecessors failed with: predecessor
	// "trigger" has no result yet. The in-memory store kept the whole
	// record, which is why this only bit Postgres.
	var resJSON any
	if rec.Result != nil {
		b, merr := json.Marshal(rec.Result)
		if merr != nil {
			return fmt.Errorf("marshal result: %w", merr)
		}
		resJSON = b
	}
	// A record enqueued already-terminal (a seed) is finished now; mirror
	// complete() so run duration and the stuck-run reaper see a finish time.
	// Seeds and graph-records (enqueued already-running) never pass through
	// Claim, so stamp started_at here too — otherwise webhook-triggered
	// runs render with no start time/duration.
	var finished, started any
	if core.IsTerminalStatus(status) {
		now := time.Now().UTC()
		finished = now
		started = now
	} else if status == core.JobStatusRunning {
		started = time.Now().UTC()
	}
	// slot_at is the record's place in the queue. FIFO would be enqueued_at;
	// instead a queued step is placed one spacing behind the last step its org
	// already has waiting, so an org that queues a thousand steps at once
	// spreads them along the queue rather than putting all of them ahead of
	// everyone else's next step — which lands at the front, having nothing
	// waiting. The org's queue tail is one index probe (jobs_tenant_queue_idx),
	// at the end where nothing has been claimed yet; counting its backlog
	// instead cost a heap visit per waiting row, inside the hot statement.
	// Records that never wait (graph records, seeds) take their enqueue time.
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

// claimQuery picks the first claimable node job (queued, or running with an
// expired lease) in queue order and marks it running under the caller's
// worker. Queue order is slot_at — assigned at enqueue, see Enqueue — which is
// FIFO except that one org's burst is spread out, so the fairness decision
// costs nothing here: one index scan, skipping past rows other workers hold.
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

// claimCappedQuery is claimQuery plus a per-tenant concurrency cap ($3):
// a queued job is only claimable if its tenant currently has fewer than
// $3 live-running node jobs. Expired-lease reclaims bypass the cap (they
// recover existing work). The count is a correlated subquery, not locked
// against concurrent claimers — hence the soft cap documented on
// SetMaxConcurrentPerTenant.
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

// CountsByStatus implements core.JobCounter via a single GROUP BY over
// the node-kind rows.
// PruneTerminal deletes finished job rows whose finished_at is older
// than the cutoff, in bounded batches so a large backlog doesn't lock
// the table in one statement. Only terminal-status rows are removed;
// queued/running rows (and any row without a finished_at) are never
// touched, so an in-flight or reaper-recoverable run is safe. Returns
// the total number of rows deleted. olderThan <= 0 is a no-op so callers
// can pass a disabled-retention value straight through.
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
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM jobs WHERE id IN (
			     SELECT id FROM jobs
			      WHERE finished_at IS NOT NULL AND finished_at < $1
			        AND status IN ('succeeded','failed','cancelled','skipped')
			      LIMIT $2)`, cutoff, batch)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		if n < batch {
			return total, nil
		}
		// Yield between batches: bail promptly on shutdown rather than
		// holding a connection through a long backlog drain.
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
	}
}

// DeleteByTenant hard-deletes every job record (graph + node, terminal or
// in-flight) owned by a tenant. Unlike PruneTerminal this ignores status,
// because it backs the GDPR erasure cascade (Art. 17): the tenant is being
// removed, so any in-flight run goes with it. Callers should cancel active
// runs first. Returns the number of rows removed.
func (s *Postgres) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// OldestQueuedEnqueuedAt returns the enqueue time of the oldest
// claimable (queued, available) node job, so metrics can expose queue
// latency — the age of this row is how long the most-delayed work has
// waited for a worker. The bool is false when nothing is queued. Uses
// the same workqueue index as Claim, so it's a cheap index probe.
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

// CompleteOwned implements core.OwnedCompleter: Complete, but only if
// worker still owns the record (ErrConflict otherwise).
func (s *Postgres) CompleteOwned(ctx context.Context, jobID, worker string, status core.JobStatus, result *core.Result) error {
	return s.complete(ctx, jobID, worker, status, result)
}

// CompleteAndEnqueue implements core.CompleteEnqueuer as one statement: a
// data-modifying CTE completes the node, reads its run's status and inserts
// the dependents, so all of it is one round trip and one commit. The
// fencing rules are complete()'s; a dependent that already exists is skipped
// by ON CONFLICT rather than aborting the whole write, and none are inserted
// when the run record is already terminal.
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
	// The dependents take queue slots behind the org's queue tail, as
	// Enqueue does, and one spacing apart from each other.
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

// complete is the shared body. worker == "" skips the ownership fence
// (the plain Complete used by non-lease callers).
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
	// Awaiting parks the record without finishing it — finished_at stays
	// NULL so the resume path can mark it later. Terminal writes set it.
	finishedClause := "finished_at = now()"
	if status == core.JobStatusAwaiting {
		finishedClause = "finished_at = finished_at"
	}
	// Refuse to overwrite an already-terminal record. Awaiting and skipped
	// are NOT included in the guard so the resume path (awaiting → succeeded)
	// works. When worker is set, also fence on lease ownership.
	//
	// Parking is additionally fenced against a record that is ALREADY parked:
	// awaiting → awaiting is a re-park, and the only way to reach one is a
	// second execution of a node that already parked (an expired lease
	// reclaimed by another worker while the first was still running). That
	// write used to succeed, so both executions announced a pause that only
	// happened once — and the daemon's park hook mailed the approvers on
	// each. Rejecting it here gives the park path the at-most-once guarantee
	// its notification hook assumes; the worker already treats a fenced park
	// as "someone else owns this now" and abandons without notifying.
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
		       enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
		  FROM jobs WHERE id = $1
	`
	rec, err := scanRecord(s.pool.QueryRow(ctx, q, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.JobRecord{}, core.ErrNotFound
	}
	return rec, err
}

// MarkGraphRunning implements core.GraphRunStarter: flip a pending (queued)
// graph record to running. The WHERE status='queued' is the admission guard —
// only one promoter's UPDATE affects a row, so concurrent sweeps on multiple
// nodes can't double-start a run. Returns true when this call did the flip.
func (s *Postgres) MarkGraphRunning(ctx context.Context, jobID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status = 'running', started_at = COALESCE(started_at, now())
		   WHERE id = $1 AND kind = 'graph' AND status = 'queued'`, jobID)
	if err != nil {
		return false, wrapPgErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

// SetGraphRunParked implements core.GraphRunParker. Both directions are
// conditional on the current status, so a second park (a run with two steps
// awaiting at once) and a resume that isn't the last one are no-ops rather
// than errors — the caller doesn't have to track how many are outstanding.
// Terminal records are never touched: a cancelled run must not be dragged
// back to awaiting by a park that lost the race.
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

func (s *Postgres) ListGraphRuns(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	// Build the SELECT with optional predicates. The kind='graph' clause
	// is non-negotiable — that's the whole point of this method vs
	// ListByGraph.
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
	             enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
	        FROM jobs WHERE kind = 'graph'`
	args := []any{}
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
	args = append(args, limit)
	// id DESC is a deterministic tiebreaker: enqueued_at ties are common (a
	// scheduler/webhook fan-out submits many runs in the same instant), and
	// without a unique secondary key LIMIT/OFFSET pagination can repeat or skip
	// a row across page boundaries. id is random, not chronological, but it
	// gives a stable total order, which is all pagination needs.
	q += fmt.Sprintf(" ORDER BY enqueued_at DESC, id DESC LIMIT $%d", len(args))
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

func (s *Postgres) ListNodeRecords(ctx context.Context, opts core.ListNodeRecordsOpts) ([]core.JobRecord, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, kind, graph_run_id, graph_id, node_id, tenant, workspace, status, job, graph_payload, result,
	             enqueued_at, available_at, started_at, finished_at, attempt, lease_until, worker_id, parent_node_rec_id, manual
	        FROM jobs WHERE kind = 'node'`
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
		// jsonb_exists, not the `?` operator: `?` is a placeholder in most
		// drivers and reads as one to every human skimming the query, and the
		// function form is what the matching partial index in schema.sql is
		// declared with.
		q += fmt.Sprintf(" AND jsonb_exists(result->'output', $%d)", len(args))
	}
	args = append(args, limit)
	// id DESC is a deterministic tiebreaker: enqueued_at ties are common (a
	// scheduler/webhook fan-out submits many runs in the same instant), and
	// without a unique secondary key LIMIT/OFFSET pagination can repeat or skip
	// a row across page boundaries. id is random, not chronological, but it
	// gives a stable total order, which is all pagination needs.
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

// pgUniqueViolation is SQLSTATE 23505 — unique_violation. Enqueue's only
// realistic constraint failure is the jobs primary key, i.e. "this job ID is
// already enqueued", which is exactly core.ErrConflict.
const pgUniqueViolation = "23505"

// wrapPgErr normalizes a pgx error into the store-agnostic sentinels callers
// branch on, falling back to an opaque wrap.
//
// Mapping unique_violation is what keeps the two JobStore backends
// behaviourally identical: Memory.Enqueue returns core.ErrConflict for a
// duplicate ID, so without this errors.Is(err, core.ErrConflict) was true on
// the in-memory store and false on Postgres for the same operation — a
// divergence the conformance suite missed because it only asserted err != nil.
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
