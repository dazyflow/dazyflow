// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// TestPgRunLogPrune_ParkedRun pins the run-scoped contract: a run that has not
// finished keeps its whole log however old the lines are. Verified to fail
// against the per-line predicate this replaced (both lines were deleted).
func TestPgRunLogPrune_ParkedRun(t *testing.T) {
	pool, ctx := covPGPool(t)

	js, err := jobstore.NewPostgresFromPool(ctx, pool)
	if err != nil {
		t.Fatalf("jobstore schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
		t.Fatalf("truncate jobs: %v", err)
	}
	store, err := NewPgRunLogStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunLogStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE run_logs"); err != nil {
		t.Fatalf("truncate run_logs: %v", err)
	}

	// Parked on an approval since 72h ago; a finished run from the same day.
	old := time.Now().Add(-72 * time.Hour).UTC()
	for _, r := range []struct {
		id     string
		status core.JobStatus
	}{{"run-parked", core.JobStatusRunning}, {"run-done", core.JobStatusSucceeded}} {
		if err := js.Enqueue(ctx, core.JobRecord{
			ID: r.id, Kind: core.JobKindGraph, Tenant: "acme", Workspace: "ws",
			GraphID: "g", NodeID: "*", Status: r.status,
			Job: core.Job{ID: r.id, GraphID: "g"},
		}); err != nil {
			t.Fatalf("enqueue %s: %v", r.id, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`UPDATE jobs SET status = 'succeeded', finished_at = $1 WHERE id = 'run-done'`, old); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	for _, e := range []RunLogEntry{
		{RunID: "run-parked", TS: old, Kind: "progress", Message: "step 1 ok"},
		{RunID: "run-parked", TS: old, Kind: "progress", Message: "waiting for approval"},
		{RunID: "run-done", TS: old, Kind: "terminal", Message: "finished"},
	} {
		if err := store.AppendRunLog(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	n, err := store.Prune(ctx, 24*time.Hour, 0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d lines, want 1 (the finished run's)", n)
	}
	if left, _ := store.ListRunLogs(ctx, "run-parked", 0, 0); len(left) != 2 {
		t.Errorf("a run that has not finished lost %d of its 2 log lines", 2-len(left))
	}
	if gone, _ := store.ListRunLogs(ctx, "run-done", 0, 0); len(gone) != 0 {
		t.Errorf("finished run kept %d lines, want none", len(gone))
	}
}

// TestPgRunLogPrune_Orphans covers the pass that does the real work once the
// jobs sweep (which runs first, on the same default window) has already taken
// the run record.
func TestPgRunLogPrune_Orphans(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := jobstore.NewPostgresFromPool(ctx, pool); err != nil {
		t.Fatalf("jobstore schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
		t.Fatalf("truncate jobs: %v", err)
	}
	store, err := NewPgRunLogStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunLogStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE run_logs"); err != nil {
		t.Fatalf("truncate run_logs: %v", err)
	}

	// No jobs row at all: the run was pruned before its log was.
	_ = store.AppendRunLog(ctx, RunLogEntry{
		RunID: "run-gone", TS: time.Now().Add(-72 * time.Hour).UTC(),
		Kind: "progress", Message: "orphan",
	})
	// A recent orphan stays: inside the window, whatever its run's fate.
	_ = store.AppendRunLog(ctx, RunLogEntry{
		RunID: "run-gone-recent", TS: time.Now().UTC(),
		Kind: "progress", Message: "recent orphan",
	})

	n, err := store.Prune(ctx, 24*time.Hour, 0)
	if err != nil || n != 1 {
		t.Fatalf("prune = %d / %v, want 1", n, err)
	}
	if left, _ := store.ListRunLogs(ctx, "run-gone-recent", 0, 0); len(left) != 1 {
		t.Errorf("recent orphan was pruned: %+v", left)
	}
}

// TestMemRunLogPrune_RunScoped pins the same contract on the in-process store
// once it is given a job store to ask.
func TestMemRunLogPrune_RunScoped(t *testing.T) {
	ctx := context.Background()
	jobs := jobstore.NewMemory()
	store := NewMemRunLogStore()
	store.Jobs = jobs

	old := time.Now().Add(-72 * time.Hour)
	mustEnqueue := func(id string, status core.JobStatus) {
		t.Helper()
		if err := jobs.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindGraph, Tenant: "acme", Workspace: "ws",
			GraphID: "g", NodeID: "*", Status: status,
			Job: core.Job{ID: id, GraphID: "g"},
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	mustEnqueue("parked", core.JobStatusRunning)
	mustEnqueue("done", core.JobStatusRunning)
	if err := jobs.Complete(ctx, "done", core.JobStatusSucceeded, nil); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "parked", TS: old, Message: "early"})
	_ = store.AppendRunLog(ctx, RunLogEntry{RunID: "done", TS: old, Message: "early"})

	// "done" finished just now, so it is inside the window and stays too.
	if n, err := store.Prune(ctx, 24*time.Hour, 0); err != nil || n != 0 {
		t.Fatalf("prune = %d / %v, want 0", n, err)
	}
	if left, _ := store.ListRunLogs(ctx, "parked", 0, 0); len(left) != 1 {
		t.Errorf("parked run lost its log: %+v", left)
	}
	if left, _ := store.ListRunLogs(ctx, "done", 0, 0); len(left) != 1 {
		t.Errorf("run finished inside the window lost its log: %+v", left)
	}
}
