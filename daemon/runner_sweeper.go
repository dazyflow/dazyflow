// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"time"
)

// RunnerTaskSweeper closes tasks nobody is waiting for any more.
//
// RunnerDispatcher.Dispatch's goroutine is otherwise the only thing that moves a
// task to a terminal state, and it does not survive a redeploy or an OOM kill.
// The row it leaves behind is not merely untidy: a QUEUED task stays claimable
// forever, so a runner switched on an hour later runs a script for a run that
// died; a RUNNING task whose agent vanished is never condemned; and since Prune
// collects only 'done' and 'failed', both accumulate inside the partial claim
// index and slow the query every agent polls.
//
// The sweep is idempotent and safe to run on several daemons at once: it closes
// each row through the same atomic, re-checking operations the dispatcher uses,
// so a task that finishes mid-listing simply reports "not closed by me".
type RunnerTaskSweeper struct {
	Tasks RunnerTaskStore
	// QueuedCeiling bounds a task that carries no timeout of its own. Rows that
	// do carry one are closed at their own timeout plus DispatchGrace, because
	// that is exactly when the step waiting on them gave up.
	QueuedCeiling time.Duration
	DispatchGrace time.Duration
	Batch         int
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

const orphanedTaskReason = "the daemon that queued this step stopped waiting for it, " +
	"most likely because it restarted; the script was not run"

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
