// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"

	"github.com/dazyflow/dazyflow/core"
)

// Oldest-first listings and the capped promotion, for the background sweeps.
// ListGraphRuns is newest-first for the Runs page, and a sweep reading its first
// page never reaches the oldest pending or stuck runs once a backlog outgrows
// it. jobs_graph_status_idx (status, enqueued_at DESC) serves these read
// backwards.

func (s *Postgres) ListGraphRunsOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	return s.listGraphRuns(ctx, opts, oldestFirst)
}

func (s *Postgres) ListGraphRunSummariesOldest(ctx context.Context, opts core.ListGraphRunsOpts) ([]core.RunSummary, error) {
	return s.listGraphRunSummaries(ctx, opts, oldestFirst)
}

// GraphRunTenants lists every tenant with a graph run in status, so a sweep
// visits all of them rather than whichever filled one page of runs.
func (s *Postgres) GraphRunTenants(ctx context.Context, status core.JobStatus) ([]string, error) {
	return queryRows(ctx, s.pool, scanString,
		`SELECT DISTINCT tenant FROM jobs WHERE kind = 'graph' AND status = $1`, string(status))
}

// MarkGraphRunningCapped is MarkGraphRunning under the tenant's concurrency
// cap, checked and applied in one transaction behind a per-tenant advisory
// lock, so promoters on several replicas cannot each see a free slot and
// together overshoot it. atCap reports that nothing was started because the
// tenant already has limit runs running.
func (s *Postgres) MarkGraphRunningCapped(ctx context.Context, jobID, tenant string, limit int) (started, atCap bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, false, wrapPgErr(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('dazyflow-promote:' || $1, 0))`, tenant); err != nil {
		return false, false, wrapPgErr(err)
	}
	var running int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM (SELECT 1 FROM jobs
		   WHERE kind = 'graph' AND tenant = $1 AND status = 'running' LIMIT $2) capped`,
		tenant, limit).Scan(&running); err != nil {
		return false, false, wrapPgErr(err)
	}
	if running >= limit {
		return false, true, nil
	}
	tag, err := tx.Exec(ctx,
		`UPDATE jobs SET status = 'running', started_at = COALESCE(started_at, now())
		   WHERE id = $1 AND kind = 'graph' AND status = 'queued'`, jobID)
	if err != nil {
		return false, false, wrapPgErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, wrapPgErr(err)
	}
	return tag.RowsAffected() == 1, false, nil
}
