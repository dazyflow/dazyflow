package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// TestWorker_FailNodeWhenGraphUnloadable covers worker.failNode's nil-graph
// branch: a node job whose graph-run record carries a corrupt payload can't be
// loaded, so the node is failed and the run marked failed without a graph walk.
func TestWorker_FailNodeWhenGraphUnloadable(t *testing.T) {
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}

	graphRunID := "corrupt-run"
	// Graph-record with a payload that is NOT valid JSON for a graph.
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID:           graphRunID,
		Kind:         core.JobKindGraph,
		Status:       core.JobStatusRunning,
		GraphPayload: []byte(`{not valid json`),
	}); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	// A node job referencing that run.
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID:         daemon.NodeJobID(graphRunID, "a"),
		Kind:       core.JobKindNode,
		GraphRunID: graphRunID,
		GraphID:    "g",
		NodeID:     "a",
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond,
		LeaseDuration: time.Second, LeaseRenewEvery: 200 * time.Millisecond,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed", terminal.Status)
	}
	nodeRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "a"))
	if nodeRec.Status != core.JobStatusFailed {
		t.Fatalf("node status = %q, want failed", nodeRec.Status)
	}
	if nodeRec.Result == nil || nodeRec.Result.Error == nil || nodeRec.Result.Error.Code != "load_graph" {
		t.Fatalf("node error = %+v, want code=load_graph", nodeRec.Result.Error)
	}
}
