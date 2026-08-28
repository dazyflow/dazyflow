// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"
	"time"
)

// The scenario the sweeper exists for: the daemon that queued a task is
// redeployed while the step waits. Nothing else ever closes that row, so it
// stays CLAIMABLE — a machine switched on later runs a script for a run that
// died, which is the same harm as running it twice.
func TestSweep_ClosesAQueuedTaskNobodyIsWaitingFor(t *testing.T) {
	q := NewMemRunnerTaskStore()
	created := time.Now().Add(-10 * time.Minute)
	mustEnqueue(t, q, RunnerTask{
		ID: "orphan", Tenant: "acme", Tags: []string{"box"}, Script: "./send-invoices.sh",
		Timeout: 30 * time.Second, State: TaskQueued, CreatedAt: created,
	})
	s := &RunnerTaskSweeper{Tasks: q}

	n, err := s.Sweep(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("closed %d task(s), want 1", n)
	}
	got, err := q.Get(t.Context(), "acme", "orphan")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != TaskFailed {
		t.Errorf("state = %q, want failed", got.State)
	}
	// The row has to say why, and it must not blame the runner — the runner
	// did nothing.
	if got.Result == nil || !strings.Contains(got.Result.Error, "restarted") {
		t.Errorf("result = %+v, want an error explaining the daemon went away", got.Result)
	}
	// And it is no longer claimable, which is the whole point.
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"}, time.Now(), TaskLease); err == nil {
		t.Error("a swept task was still handed out to a runner")
	}
}

// A task still inside its own deadline is somebody's live work. Closing it
// would fail a step that is about to succeed.
func TestSweep_LeavesATaskSomeoneIsStillWaitingFor(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{
		ID: "live", Tenant: "acme", Tags: []string{"box"}, Script: "x",
		Timeout: 10 * time.Minute, State: TaskQueued, CreatedAt: time.Now(),
	})
	s := &RunnerTaskSweeper{Tasks: q}
	if n, err := s.Sweep(t.Context(), time.Now()); err != nil || n != 0 {
		t.Fatalf("closed %d task(s) (err %v), want none", n, err)
	}
	got, _ := q.Get(t.Context(), "acme", "live")
	if got.State != TaskQueued {
		t.Errorf("state = %q, want it left queued", got.State)
	}
}

// A running task whose lease lapsed is an agent that vanished. The dispatcher
// only notices while it is still waiting; after a restart nobody is.
func TestSweep_CondemnsARunningTaskWhoseAgentVanished(t *testing.T) {
	q := NewMemRunnerTaskStore()
	now := time.Now()
	mustEnqueue(t, q, RunnerTask{ID: "held", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"}, now, TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	s := &RunnerTaskSweeper{Tasks: q}

	if n, err := s.Sweep(t.Context(), now.Add(time.Second)); err != nil || n != 0 {
		t.Fatalf("condemned a task whose lease was good: n=%d err=%v", n, err)
	}
	n, err := s.Sweep(t.Context(), now.Add(TaskLease+time.Second))
	if err != nil || n != 1 {
		t.Fatalf("Sweep: n=%d err=%v", n, err)
	}
	got, _ := q.Get(t.Context(), "acme", "held")
	if got.State != TaskFailed {
		t.Errorf("state = %q, want failed", got.State)
	}
	// FailAbandoned's wording names the machine, which is the actionable part.
	if got.Result == nil || !strings.Contains(got.Result.Error, "box") {
		t.Errorf("result = %+v, want an error naming the runner", got.Result)
	}
}

// A task carrying no timeout of its own falls back to the ceiling rather than
// being closed the moment the sweep first sees it.
func TestSweep_UntimedTaskUsesTheCeiling(t *testing.T) {
	q := NewMemRunnerTaskStore()
	created := time.Now().Add(-30 * time.Minute)
	mustEnqueue(t, q, RunnerTask{
		ID: "untimed", Tenant: "acme", Tags: []string{"box"}, Script: "x",
		State: TaskQueued, CreatedAt: created,
	})
	s := &RunnerTaskSweeper{Tasks: q}
	if n, err := s.Sweep(t.Context(), time.Now()); err != nil || n != 0 {
		t.Fatalf("closed an untimed task after 30m: n=%d err=%v", n, err)
	}
	if n, err := s.Sweep(t.Context(), time.Now().Add(DefaultRunnerQueuedCeiling)); err != nil || n != 1 {
		t.Fatalf("an untimed task outlived the ceiling: n=%d err=%v", n, err)
	}
}

// Terminal rows are Prune's business, not the sweeper's. Touching one would
// overwrite the agent's real answer with a guess.
func TestSweep_IgnoresFinishedTasks(t *testing.T) {
	q := NewMemRunnerTaskStore()
	old := time.Now().Add(-24 * time.Hour)
	mustEnqueue(t, q, RunnerTask{
		ID: "done", Tenant: "acme", Tags: []string{"box"}, Script: "x",
		State: TaskDone, CreatedAt: old,
		Result: &RunnerTaskResult{Stdout: "the real answer"},
	})
	s := &RunnerTaskSweeper{Tasks: q}
	if n, err := s.Sweep(t.Context(), time.Now()); err != nil || n != 0 {
		t.Fatalf("swept a finished task: n=%d err=%v", n, err)
	}
	got, _ := q.Get(t.Context(), "acme", "done")
	if got.State != TaskDone || got.Result.Stdout != "the real answer" {
		t.Errorf("task = %+v, want it untouched", got)
	}
}
