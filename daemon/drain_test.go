package daemon_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// A node already executing when the worker's context is cancelled (graceful
// shutdown) must run to completion and finalize its graph — not be abandoned
// mid-run. The worker derives the node's exec context via WithoutCancel, so the
// claim loop stops taking new work while the in-flight node finishes.
func TestWorker_DrainsInFlightNodeOnShutdown(t *testing.T) {
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}

	g := core.Graph{
		ID: "drain", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "s", Module: "delay", Params: map[string]any{"ms": 250}}},
	}
	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-drain"
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
		Tenant: g.Tenant, Workspace: g.Workspace,
		Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue graph: %v", err)
	}
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: daemon.NodeJobID(runID, "s"), Kind: core.JobKindNode,
		GraphRunID: runID, GraphID: g.ID, NodeID: "s",
		Tenant: g.Tenant, Workspace: g.Workspace,
	}); err != nil {
		t.Fatalf("enqueue node: %v", err)
	}

	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:              "drain-w",
		PollInterval:    5 * time.Millisecond,
		LeaseDuration:   5 * time.Second,
		LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	// Let the worker claim and start executing the 250ms node, then cancel
	// mid-run — simulating SIGTERM while a job is in flight.
	time.Sleep(60 * time.Millisecond)
	cancel()

	// The worker loop should exit (it stops claiming new work)...
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not exit after cancel")
	}

	// ...but the in-flight node must have finished and finalized the run.
	nodeRec, err := jobs.Get(context.Background(), daemon.NodeJobID(runID, "s"))
	if err != nil {
		t.Fatal(err)
	}
	if nodeRec.Status != core.JobStatusSucceeded {
		t.Errorf("in-flight node status = %q, want succeeded (it was abandoned, not drained)", nodeRec.Status)
	}
	graphRec, err := jobs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if graphRec.Status != core.JobStatusSucceeded {
		t.Errorf("graph run status = %q, want succeeded", graphRec.Status)
	}
}
