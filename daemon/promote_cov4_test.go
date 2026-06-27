package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

func promoteSvc() *Service {
	return &Service{
		Jobs:   jobstore.NewMemory(),
		Bus:    NewMemoryBus(),
		Engine: &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
	}
}

// TestStartPendingRun_NoPayload covers the no-usable-payload leg: the run is
// finalized failed.
func TestStartPendingRun_NoPayload(t *testing.T) {
	s := promoteSvc()
	run := core.JobRecord{ID: "r1", Kind: core.JobKindGraph, Status: core.JobStatusRunning}
	_ = s.Jobs.Enqueue(context.Background(), run)
	s.startPendingRun(context.Background(), run)

	got, _ := s.Jobs.Get(context.Background(), "r1")
	if got.Status != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Result == nil || got.Result.Error == nil || got.Result.Error.Code != "no_payload" {
		t.Fatalf("error = %+v, want no_payload", got.Result.Error)
	}
}

// TestStartPendingRun_EmptyGraph covers the zero-node short-circuit: succeeds
// immediately.
func TestStartPendingRun_EmptyGraph(t *testing.T) {
	s := promoteSvc()
	payload, _ := json.Marshal(core.Graph{ID: "g", Tenant: "t", Workspace: "ws"})
	run := core.JobRecord{ID: "r2", Kind: core.JobKindGraph, Status: core.JobStatusRunning, GraphPayload: payload}
	_ = s.Jobs.Enqueue(context.Background(), run)
	s.startPendingRun(context.Background(), run)

	got, _ := s.Jobs.Get(context.Background(), "r2")
	if got.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", got.Status)
	}
}

// TestStartPendingRun_DispatchesRoots covers the normal leg: a runnable root is
// enqueued and the watchdog/notifier are armed (run stays running).
func TestStartPendingRun_DispatchesRoots(t *testing.T) {
	s := promoteSvc()
	g := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	payload, _ := json.Marshal(g)
	run := core.JobRecord{
		ID: "r3", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}
	_ = s.Jobs.Enqueue(context.Background(), run)
	s.startPendingRun(context.Background(), run)

	// The root node-record was enqueued (queued, awaiting a worker).
	nodeRec, err := s.Jobs.Get(context.Background(), NodeJobID("r3", "n"))
	if err != nil {
		t.Fatalf("root node not enqueued: %v", err)
	}
	if nodeRec.NodeID != "n" {
		t.Fatalf("node = %q, want n", nodeRec.NodeID)
	}
	// The run itself stays running (no worker in this test to advance it).
	got, _ := s.Jobs.Get(context.Background(), "r3")
	if got.Status != core.JobStatusRunning {
		t.Fatalf("run status = %q, want running", got.Status)
	}
}
