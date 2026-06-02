package daemon_test

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
)

// reapGraph is the 2-node a→b graph the reaper tests run against.
func reapGraph() core.Graph {
	return core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
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
