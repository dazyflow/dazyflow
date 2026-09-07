// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// reapGraph is the 2-node a→b graph the reaper tests run against.
func reapGraph() core.Graph {
	return core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "b", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{{From: "a", FromPort: "pass", To: "b", ToPort: "pass"}},
	}
}

// seedGraphRun writes a graph-record (running) plus node-records at the given
// statuses — directly, without a worker, so the test controls exactly which
// nodes are terminal. Mirrors the post-crash on-disk state.
func seedGraphRun(t *testing.T, jobs core.JobStore, runID string, g core.Graph, nodeStatus map[string]core.JobStatus) {
	t.Helper()
	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
		Tenant: g.Tenant, Workspace: g.Workspace,
		Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue graph rec: %v", err)
	}
	for _, n := range g.Nodes {
		st := nodeStatus[n.ID]
		rec := core.JobRecord{
			ID: daemon.NodeJobID(runID, n.ID), Kind: core.JobKindNode,
			GraphRunID: runID, GraphID: g.ID, NodeID: n.ID,
			Tenant: g.Tenant, Workspace: g.Workspace, Status: st,
		}
		if core.IsTerminalStatus(st) {
			rec.Result = &core.Result{Status: core.StatusOK}
		}
		if err := jobs.Enqueue(t.Context(), rec); err != nil {
			t.Fatalf("enqueue node %s: %v", n.ID, err)
		}
	}
}

// A graph run whose nodes are all terminal but whose graph-record is stuck
// "running" (worker died before the completion check) must be finalized by the
// reaper.
func TestReaper_RecoversOrphanedRun(t *testing.T) {
	t.Parallel()
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	d := daemon.NewDispatcher(jobs, bus, eng, nil)
	g := reapGraph()

	seedGraphRun(t, jobs, "run-orphan", g, map[string]core.JobStatus{
		"a": core.JobStatusSucceeded,
		"b": core.JobStatusSucceeded,
	})

	n, err := d.ReapStuckGraphRuns(t.Context())
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped = %d, want 1", n)
	}
	rec, err := jobs.Get(t.Context(), "run-orphan")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("graph run status = %q, want succeeded", rec.Status)
	}
}

// A run with a still-running node must be left untouched — the reaper only
// finalizes runs that are genuinely complete.
func TestReaper_LeavesHealthyRunRunning(t *testing.T) {
	t.Parallel()
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	d := daemon.NewDispatcher(jobs, bus, eng, nil)
	g := reapGraph()

	seedGraphRun(t, jobs, "run-healthy", g, map[string]core.JobStatus{
		"a": core.JobStatusSucceeded,
		"b": core.JobStatusRunning, // still in flight
	})

	n, err := d.ReapStuckGraphRuns(t.Context())
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 0 {
		t.Fatalf("reaped = %d, want 0 (run not done)", n)
	}
	rec, err := jobs.Get(t.Context(), "run-healthy")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != core.JobStatusRunning {
		t.Errorf("graph run status = %q, want still running", rec.Status)
	}
}

// A run with a terminal failed node (failure propagates by default) must be
// finalized as failed, not succeeded.
func TestReaper_RecoversFailedRun(t *testing.T) {
	t.Parallel()
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	d := daemon.NewDispatcher(jobs, bus, eng, nil)
	g := reapGraph()

	seedGraphRun(t, jobs, "run-failed", g, map[string]core.JobStatus{
		"a": core.JobStatusSucceeded,
		"b": core.JobStatusFailed,
	})

	if _, err := d.ReapStuckGraphRuns(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	rec, err := jobs.Get(t.Context(), "run-failed")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != core.JobStatusFailed {
		t.Errorf("graph run status = %q, want failed", rec.Status)
	}
}

// The zombie. Retention is run-scoped now, so an unfinished run is never
// pruned — right, since deleting a live run is a data-loss bug — but that
// makes a run which can NEVER finish immortal. It sits in the Runs list as
// running for ever, holds a concurrency slot for ever (so a capped tenant
// eventually admits every new run as pending and never starts one), and
// notifies nobody, because it never reaches a terminal state.
//
// The shape here is the one the OLD row-scoped retention produced: a live run
// whose node records were deleted out from under it. maybeCompleteGraph reads
// a missing record as "not done yet", so the completion check can never
// finalize it and the reaper could never close it.
func TestReaper_AbandonsARunThatCanNeverFinish(t *testing.T) {
	jobs := jobstore.NewMemory()
	g := reapGraph()
	// A graph record with NO node records at all: nothing pending, nothing
	// terminal, nothing that could ever advance it.
	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-48 * time.Hour)
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: "zombie", Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
		Tenant: g.Tenant, Workspace: g.Workspace,
		Status: core.JobStatusRunning, GraphPayload: payload,
		EnqueuedAt: long,
	}); err != nil {
		t.Fatal(err)
	}

	d := daemon.NewDispatcher(jobs, daemon.NewMemoryBus(), nil, nil)
	if _, err := d.ReapStuckGraphRuns(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}

	rec, err := jobs.Get(t.Context(), "zombie")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Status != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed — an unfinishable run must not be immortal", rec.Status)
	}
	if rec.Result == nil || rec.Result.Error == nil || rec.Result.Error.Code != "run_abandoned" {
		t.Errorf("no reason recorded: %+v", rec.Result)
	}
	// Terminal, so it stops counting against concurrency, the notification
	// sweep reports it, and retention can finally age it out.
	if rec.FinishedAt == nil {
		t.Error("no finish time: retention keys a run's age on it")
	}
}

// A run WAITING on something real must never be abandoned, however old. This
// is the case that makes "old" the wrong test on its own: an approval parked
// for three weeks and a delay counting down 90 days are both correct
// behaviour, and both look ancient.
func TestReaper_LeavesAWaitingRunAlone(t *testing.T) {
	for _, pending := range []core.JobStatus{
		core.JobStatusAwaiting, core.JobStatusQueued, core.JobStatusRunning,
	} {
		t.Run(string(pending), func(t *testing.T) {
			jobs := jobstore.NewMemory()
			g := reapGraph()
			// Old enough to be past the abandonment window, with step "b"
			// still pending.
			payload, err := json.Marshal(g)
			if err != nil {
				t.Fatal(err)
			}
			if err := jobs.Enqueue(t.Context(), core.JobRecord{
				ID: "waiting", Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
				Tenant: g.Tenant, Workspace: g.Workspace,
				Status: core.JobStatusRunning, GraphPayload: payload,
				EnqueuedAt: time.Now().Add(-48 * time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			for id, st := range map[string]core.JobStatus{
				"a": core.JobStatusSucceeded, "b": pending,
			} {
				rec := core.JobRecord{
					ID: daemon.NodeJobID("waiting", id), Kind: core.JobKindNode,
					GraphRunID: "waiting", GraphID: g.ID, NodeID: id,
					Tenant: g.Tenant, Workspace: g.Workspace, Status: st,
				}
				if core.IsTerminalStatus(st) {
					rec.Result = &core.Result{Status: core.StatusOK}
				}
				if err := jobs.Enqueue(t.Context(), rec); err != nil {
					t.Fatal(err)
				}
			}

			d := daemon.NewDispatcher(jobs, daemon.NewMemoryBus(), nil, nil)
			if _, err := d.ReapStuckGraphRuns(t.Context()); err != nil {
				t.Fatalf("reap: %v", err)
			}
			rec, err := jobs.Get(t.Context(), "waiting")
			if err != nil {
				t.Fatal(err)
			}
			if core.IsTerminalStatus(rec.Status) {
				t.Fatalf("a run with a %s step was abandoned (status %q); it is waiting, not stuck",
					pending, rec.Status)
			}
		})
	}
}

// A young run with nothing pending is mid-transition, not abandoned: a node
// has gone terminal and its successor is not enqueued yet. The window exists
// for exactly that instant.
func TestReaper_LeavesAYoungRunAlone(t *testing.T) {
	jobs := jobstore.NewMemory()
	g := reapGraph()
	payload, _ := json.Marshal(g)
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: "young", Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
		Tenant: g.Tenant, Workspace: g.Workspace,
		Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	d := daemon.NewDispatcher(jobs, daemon.NewMemoryBus(), nil, nil)
	if _, err := d.ReapStuckGraphRuns(t.Context()); err != nil {
		t.Fatalf("reap: %v", err)
	}
	rec, _ := jobs.Get(t.Context(), "young")
	if core.IsTerminalStatus(rec.Status) {
		t.Errorf("a run seconds old was abandoned (status %q)", rec.Status)
	}
}
