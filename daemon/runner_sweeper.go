// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"time"
)

// RunnerTaskSweeper closes tasks nobody is waiting for any more.
//
// It exists because RunnerDispatcher.Dispatch's goroutine was the only thing
// that ever moved a task to a terminal state, and that goroutine does not
// survive a redeploy or an OOM kill. The row it leaves behind is not merely
// untidy:
//
//   - A QUEUED task stays claimable forever. The runner is switched on an hour
//     later, claims it, and runs a script for a run that died — which for a
//     script that sends invoices is, in CancelQueued's own words, "the same
//     harm as running it twice".
//   - A RUNNING task whose agent vanished is never condemned, so nothing ever
//     records what happened to it.
//   - Neither is terminal, and Prune only collects 'done' and 'failed', so both
//     accumulate permanently inside the partial index runner_tasks_claim_idx
//     and slow the claim query every agent polls.
//
// The sweep is idempotent and safe to run on several daemons at once: it closes
// each row through FailAbandoned or CancelQueued, which are the same atomic,
// re-checking operations the dispatcher uses. A task that finishes between the
// listing and the close simply reports "not closed by me", and the agent's real
// answer wins — the same race the dispatcher already handles.
type RunnerTaskSweeper struct {
	Tasks RunnerTaskStore
	// QueuedCeiling bounds a task that carries no timeout of its own. Rows that
	// do carry one are closed at their own timeout plus DispatchGrace, because
	// that is exactly when the step waiting on them gave up.
	QueuedCeiling time.Duration
	// DispatchGrace mirrors RunnerDispatcher's; zero means the constant.
	DispatchGrace time.Duration
	// Batch bounds one pass so a large backlog does not hold the pool.
	Batch int
}

// DefaultRunnerQueuedCeiling closes a task that carries no timeout of its own.
//
// Generous, because "no timeout" means the author asked for a script that may
// legitimately run for a long time, and closing one out from under a live agent
// would be the very double-answer the lease rules exist to avoid. An hour is
// well past any dispatch that is still being waited on: the ambient run context
// bounds the step long before this.
const DefaultRunnerQueuedCeiling = time.Hour

func (s *RunnerTaskSweeper) grace() time.Duration {
	if s.DispatchGrace > 0 {
		return s.DispatchGrace
	}
	return RunnerDispatchGrace
}

func (s *RunnerTaskSweeper) ceiling() time.Duration {
	if s.QueuedCeiling > 0 {
		return s.QueuedCeiling
	}
	return DefaultRunnerQueuedCeiling
}

func (s *RunnerTaskSweeper) batch() int {
	if s.Batch > 0 {
		return s.Batch
	}
	return 500
}

// orphanedTaskReason is what a swept queued task records. It says the daemon
// went away rather than blaming the runner, because the runner did nothing
// wrong — and a row that says "cancelled" with no reason is the kind of thing
// someone spends an afternoon on.
const orphanedTaskReason = "the daemon that queued this step stopped waiting for it, " +
	"most likely because it restarted; the script was not run"

// Sweep closes one batch of orphaned tasks and reports how many it closed.
func (s *RunnerTaskSweeper) Sweep(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.Tasks == nil {
		return 0, nil
	}
	rows, err := s.Tasks.OrphanedTasks(ctx, now, s.grace(), s.ceiling(), s.batch())
	if err != nil {
		return 0, err
	}
	closed := 0
	var firstErr error
	for _, t := range rows {
		if ctx.Err() != nil {
			break
		}
		var did bool
		var err error
		switch t.State {
		case TaskRunning:
			did, err = s.Tasks.FailAbandoned(ctx, t.Tenant, t.ID, now)
		case TaskQueued:
			did, err = s.Tasks.CancelQueued(ctx, t.Tenant, t.ID, cancelledResult(orphanedTaskReason), now)
		default:
			continue
		}
		if err != nil {
			// One unclosable row must not stop the batch — the rest are
			// independent, and a queued row left claimable is the dangerous
			// half. The first error is still reported, so the failure is not
			// silent.
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if did {
			closed++
		}
	}
	return closed, firstErr
}
