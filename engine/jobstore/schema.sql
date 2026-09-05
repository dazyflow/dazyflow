-- Dazyflow JobStore schema.
--
-- The store backs both the workqueue (via SELECT ... FOR UPDATE SKIP LOCKED)
-- and the scheduler's leader election (via pg_try_advisory_lock).
-- Tenants and workspaces live in their own table set; this file covers only
-- the job lifecycle.

CREATE TABLE IF NOT EXISTS jobs (
    id            TEXT PRIMARY KEY,
    kind          TEXT NOT NULL DEFAULT 'graph',
    graph_run_id  TEXT NOT NULL DEFAULT '',
    graph_id      TEXT NOT NULL,
    node_id       TEXT NOT NULL,
    tenant        TEXT NOT NULL,
    workspace     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    job           JSONB NOT NULL,
    graph_payload JSONB,
    result        JSONB,
    enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    available_at  TIMESTAMPTZ,
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    attempt       INTEGER NOT NULL DEFAULT 0,
    lease_until   TIMESTAMPTZ,
    worker_id     TEXT NOT NULL DEFAULT '',
    parent_node_rec_id TEXT NOT NULL DEFAULT '',
    -- Whether a person started this run from the app (and is therefore watching
    -- it fail). Suppresses the failure email; see core.JobRecord.Manual.
    manual BOOLEAN NOT NULL DEFAULT FALSE
);

-- Backfill for existing deployments (idempotent — schema is re-applied
-- on every OpenPostgres).
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS parent_node_rec_id TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS manual BOOLEAN NOT NULL DEFAULT FALSE;
-- The record's place in the queue: enqueue time, pushed later for a step whose
-- org already has steps waiting, so one org's burst is spread along the queue
-- instead of heading it (see Enqueue in postgres.go).
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS slot_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Workqueue index: node-kind rows in a claimable state, in queue order (the
-- claim's ORDER BY slot_at, enqueued_at). The predicate must be IMMUTABLE, so
-- it can't reference now() — the time-window filters (available_at <= now(),
-- lease_until < now()) live in the Claim query's WHERE clause and run against
-- this narrowed subset. Replaces jobs_queue_idx, which ordered by enqueued_at
-- alone; the oldest-queued metric sorts the few queued rows itself.
DROP INDEX IF EXISTS jobs_queue_idx;
CREATE INDEX IF NOT EXISTS jobs_slot_idx
    ON jobs (slot_at, enqueued_at)
    WHERE kind = 'node' AND status IN ('queued', 'running');

-- Per-graph-run index so workers can quickly find sibling/predecessor
-- records when dispatching downstream nodes.
CREATE INDEX IF NOT EXISTS jobs_graph_run_idx ON jobs (graph_run_id);

-- Per-graph history index for the timeline view in dzctl.
CREATE INDEX IF NOT EXISTS jobs_graph_idx ON jobs (graph_id, enqueued_at DESC);

-- Tenant-scoped index. Backs the tenant/workspace-filtered
-- ListGraphRuns/ListNodeRecords, per-tenant retention + GDPR-erasure sweeps,
-- DeleteByTenant, and — via slot_at — the enqueue's probe for an org's queue
-- tail (max slot_at over its queued rows). Without it every one of those
-- full-scans the jobs table. Replaces jobs_tenant_status_idx (tenant, status),
-- of which it is a superset.
DROP INDEX IF EXISTS jobs_tenant_status_idx;
CREATE INDEX IF NOT EXISTS jobs_tenant_queue_idx ON jobs (tenant, status, slot_at);

-- Graph-run sweep index. The concurrency promoter looks for queued graph runs
-- every couple of seconds on every replica, and the orphan reaper for running
-- ones every minute. Neither filters by tenant, so jobs_tenant_queue_idx can
-- only serve them by scanning its whole self and filtering — work that grows
-- with the table on a fixed-frequency sweep. Ordering by enqueued_at here also
-- drops the sort those queries would otherwise do.
CREATE INDEX IF NOT EXISTS jobs_graph_status_idx
    ON jobs (status, enqueued_at DESC)
    WHERE kind = 'graph';

-- Retention's predicate. Terminal rows are almost the whole table, so without
-- this the hourly sweep scans all of them to find the few old enough to
-- delete — and scans all of them to prove there are none, which is what every
-- pass ends with. A row enters this index once, when it finishes, and is never
-- updated in it again. Mirrors runner_tasks_prune_idx.
CREATE INDEX IF NOT EXISTS jobs_prune_idx
    ON jobs (finished_at)
    WHERE status IN ('succeeded', 'failed', 'cancelled', 'skipped');

-- Approval index. The approvals page reads two slices of await_approval
-- node-records — parked (the inbox) and decided (the history beneath it) —
-- and both are needle-in-haystack queries: an approval is a rounding error
-- against the succeeded steps of every run the workspace has ever executed.
-- The partial predicate is the identifying mark of an approval node
-- (`pending_url`, emitted at the pause and carried through the resume), so the
-- index holds only approval rows, and finished_at DESC serves the history's
-- ordering — newest DECISION first, which is not the same order as newest run.
-- Matches core.ListNodeRecordsOpts.HasOutputPort + NewestByFinished.
CREATE INDEX IF NOT EXISTS jobs_approval_idx
    ON jobs (tenant, workspace, finished_at DESC)
    WHERE kind = 'node' AND jsonb_exists(result->'output', 'pending_url');
