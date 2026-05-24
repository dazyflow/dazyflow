-- Hazy Flow JobStore schema.
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
    parent_node_rec_id TEXT NOT NULL DEFAULT ''
);

-- Backfill for existing deployments (idempotent — schema is re-applied
-- on every OpenPostgres).
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS parent_node_rec_id TEXT NOT NULL DEFAULT '';

-- Workqueue index: only node-kind, queued (and available) or
-- running-but-expired.
CREATE INDEX IF NOT EXISTS jobs_queue_idx
    ON jobs (enqueued_at)
    WHERE kind = 'node'
      AND (
            (status = 'queued' AND (available_at IS NULL OR available_at <= now()))
         OR (status = 'running' AND lease_until < now())
          );

-- Per-graph-run index so workers can quickly find sibling/predecessor
-- records when dispatching downstream nodes.
CREATE INDEX IF NOT EXISTS jobs_graph_run_idx ON jobs (graph_run_id);

-- Per-graph history index for the timeline view in hzctl.
CREATE INDEX IF NOT EXISTS jobs_graph_idx ON jobs (graph_id, enqueued_at DESC);
