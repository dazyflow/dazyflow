package daemon

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Concurrency admission queue (promotion side).
//
// SubmitGraphWithSeed admits a top-level run immediately when the tenant is
// under its max_concurrency, else it persists the run as a PENDING graph record
// (status=queued) with its seeds but no runnable work. This file starts those
// pending runs as slots free up.
//
// Promotion is driven by a periodic sweep (startConcurrencyPromotion) rather
// than completion callbacks: it's robust to every way a slot can free (a run
// succeeds, fails, is cancelled, or is reaped after a crash) without wiring a
// hook into each path, at the cost of up to one sweep-interval of latency — a
// non-issue for a fairness throttle. MarkGraphRunning's conditional flip makes
// it safe to run on every replica at once.

// promotePendingRuns starts as many of a tenant's pending (queued) graph runs
// as its free concurrency slots allow, oldest first. Uncapped tenants (pro/
// comped/trial or no limit) drain their whole pending backlog. Safe to call
// concurrently across replicas: MarkGraphRunning lets only one promoter win
// each run.
func (s *Service) promotePendingRuns(ctx context.Context, tenant string) {
	starter, ok := s.Jobs.(core.GraphRunStarter)
	if !ok {
		return
	}
	limit, capped := s.concurrencyCapped(ctx, tenant)
	// Bound work per call so a large backlog can't monopolize a sweep; the
	// next sweep picks up where this left off.
	const maxPerCall = 50
	for range maxPerCall {
		if capped {
			running, err := s.runningGraphRuns(ctx, tenant, limit)
			if err != nil {
				return
			}
			if running >= limit {
				return
			}
		}
		run, ok := s.oldestPendingRun(ctx, tenant)
		if !ok {
			return // nothing pending
		}
		won, err := starter.MarkGraphRunning(ctx, run.ID)
		if err != nil {
			return
		}
		if !won {
			continue // another promoter took this one; try the next
		}
		s.startPendingRun(ctx, run)
	}
}

// oldestPendingRun returns the tenant's earliest-enqueued pending (queued)
// graph run. ListGraphRuns sorts newest-first, so we scan a batch and pick the
// minimum enqueue time for FIFO fairness.
func (s *Service) oldestPendingRun(ctx context.Context, tenant string) (core.JobRecord, bool) {
	recs, err := s.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
		Tenant: tenant, Status: core.JobStatusQueued, Limit: 50,
	})
	if err != nil || len(recs) == 0 {
		return core.JobRecord{}, false
	}
	oldest := recs[0]
	for _, r := range recs[1:] {
		if r.EnqueuedAt.Before(oldest.EnqueuedAt) {
			oldest = r
		}
	}
	return oldest, true
}

// startPendingRun dispatches a run that was just flipped from pending to
// running (by MarkGraphRunning in the caller): it reconstructs the seed set
// from the pre-completed node-records, enqueues the runnable roots, and arms
// the per-run watchdogs — the work SubmitGraphWithSeed does for an admitted
// run, minus the seed-persist (already done at submit).
func (s *Service) startPendingRun(ctx context.Context, run core.JobRecord) {
	var g core.Graph
	if len(run.GraphPayload) == 0 || json.Unmarshal(run.GraphPayload, &g) != nil {
		// No usable payload — finalize as failed rather than leaving it running
		// forever. Shouldn't happen (submit always stores the payload).
		_ = s.Jobs.Complete(ctx, run.ID, core.JobStatusFailed, &core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "no_payload", Message: "pending run had no graph payload"},
		})
		s.bus().Publish(run.ID, BusEvent{Terminal: &TerminalEvent{
			JobID: run.ID, Status: core.JobStatusFailed,
			Error: &core.JobError{Code: "no_payload", Message: "pending run had no graph payload"},
		}})
		return
	}

	if len(g.Nodes) == 0 {
		_ = s.Jobs.Complete(ctx, run.ID, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK})
		s.bus().Publish(run.ID, BusEvent{Terminal: &TerminalEvent{
			JobID: run.ID, Status: core.JobStatusSucceeded,
		}})
		return
	}

	// At start, the only succeeded node-records are the seeds persisted at
	// submit — reconstruct the seed set from them so dispatchRoots fans out
	// exactly as the immediate-start path would.
	seededSet := s.seededNodes(ctx, run.ID, len(g.Nodes))

	if errs := dispatchRoots(ctx, s.Jobs, g, run.ID, seededSet); len(errs) > 0 {
		merged := errs[0]
		_ = s.Jobs.Complete(ctx, run.ID, core.JobStatusFailed, &core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "enqueue_failed", Message: merged.Error()},
		})
		s.bus().Publish(run.ID, BusEvent{Terminal: &TerminalEvent{
			JobID: run.ID, Status: core.JobStatusFailed,
			Error: &core.JobError{Code: "enqueue_failed", Message: merged.Error()},
		}})
		return
	}

	// Seeds may already cover the whole graph (e.g. a one-node webhook run) —
	// finalize now instead of waiting for a node transition that never comes.
	if allNodesAccountedFor(ctx, s.Jobs, g, run.ID) {
		if cerr := s.Jobs.Complete(ctx, run.ID, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); cerr == nil {
			s.bus().Publish(run.ID, BusEvent{Terminal: &TerminalEvent{
				JobID: run.ID, Status: core.JobStatusSucceeded,
			}})
		}
		return
	}

	s.startGraphTimeoutWatchdog(run.ID, run.Tenant, run.Workspace, s.effectiveGraphTimeout(g))
	s.startFailureNotifier(g, run.ID)
}

// seededNodes returns the set of node IDs that already hold a succeeded
// node-record for a run — i.e. its seeds, when called before any real work has
// run.
func (s *Service) seededNodes(ctx context.Context, graphRunID string, graphSize int) map[string]struct{} {
	out := map[string]struct{}{}
	recs, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		GraphRunID: graphRunID, Status: core.JobStatusSucceeded, Limit: graphSize + 1,
	})
	if err != nil {
		return out
	}
	for _, r := range recs {
		out[r.NodeID] = struct{}{}
	}
	return out
}

// SweepPromotePending finds every tenant with at least one pending run and
// promotes what it can for each. One pass of the promotion sweep, called on an
// interval by the daemon's background promoter goroutine.
func (s *Service) SweepPromotePending(ctx context.Context) {
	if _, ok := s.Jobs.(core.GraphRunStarter); !ok {
		return
	}
	recs, err := s.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
		Status: core.JobStatusQueued, Limit: 200,
	})
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("concurrency promotion: list pending: %v", err)
		}
		return
	}
	seen := make(map[string]struct{}, len(recs))
	for _, r := range recs {
		if _, done := seen[r.Tenant]; done {
			continue
		}
		seen[r.Tenant] = struct{}{}
		s.promotePendingRuns(ctx, r.Tenant)
	}
}
