-- Hazy Flow JobStore schema.
--
-- The store backs both the workqueue (via SELECT ... FOR UPDATE SKIP LOCKED)
-- and the scheduler's leader election (via pg_try_advisory_lock).
-- Tenants and workspaces live in their own table set; this file covers only
-- the job lifecycle.

CREATE TABLE IF NOT EXISTS jobs (
    id            TEXT PRIMARY KEY,
    graph_id      TEXT NOT NULL,
    node_id       TEXT NOT NULL,
    tenant        TEXT NOT NULL,
    workspace     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    job           JSONB NOT NULL,
    result        JSONB,
    enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    attempt       INTEGER NOT NULL DEFAULT 0,
    lease_until   TIMESTAMPTZ,
    worker_id     TEXT NOT NULL DEFAULT ''
);

-- Workqueue index: queued OR running-but-expired ordered by enqueued_at.
CREATE INDEX IF NOT EXISTS jobs_queue_idx
    ON jobs (enqueued_at)
    WHERE status = 'queued'
       OR (status = 'running' AND lease_until < now());

-- Per-graph history index for the timeline view in hzctl.
CREATE INDEX IF NOT EXISTS jobs_graph_idx ON jobs (graph_id, enqueued_at DESC);
