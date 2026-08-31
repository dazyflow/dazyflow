// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// waitForRunStatus polls the graph record until it reaches want, and returns
// whatever it actually settled on so failures can report it.
func waitForRunStatus(
	t *testing.T,
	jobs core.JobStore,
	runID string,
	want core.JobStatus,
	timeout time.Duration,
) core.JobStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last core.JobStatus
	for time.Now().Before(deadline) {
		rec, err := jobs.Get(t.Context(), runID)
		if err == nil {
			last = rec.Status
			if last == want {
				return last
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return last
}

// TestRunStatus_ParksAndResumes pins the run-level status a person reads off
// the runs list. A run sitting on an approver reported "Running" — indistinguishable
// from one burning CPU — and the list's Waiting filter matched nothing, because
// only the NODE record carried `awaiting`. The run now carries it too.
//
// The two-approvals case is the reason the resume path checks before
// un-parking: deciding one of them must not flip the run back to Running while
// the other approver is still being waited on.
func TestRunStatus_ParksAndResumes(t *testing.T) {
	h := newWorkerHarness(t, 1)

	// Two independent gates, so the run has two approvals open at once.
	g := core.Graph{
		ID: "two-gates", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "gate-a", Module: "await_approval", Params: map[string]any{"prompt": "A?"}},
			{ID: "gate-b", Module: "await_approval", Params: map[string]any{"prompt": "B?"}},
		},
	}
	runID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}

	// Both park → the run reports awaiting.
	if got := waitForRunStatus(t, h.jobs, runID, core.JobStatusAwaiting, 5*time.Second); got != core.JobStatusAwaiting {
		t.Fatalf("run status while parked = %q, want awaiting", got)
	}
	for _, id := range []string{"gate-a", "gate-b"} {
		rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(runID, id))
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if rec.Status != core.JobStatusAwaiting {
			t.Fatalf("%s status = %q, want awaiting", id, rec.Status)
		}
	}

	// Decide the first. The second approver is still waiting, so the run must
	// stay awaiting — this is the check that makes the status trustworthy
	// rather than "whichever pause moved last".
	if err := h.svc.Approve(t.Context(), runID, "gate-a", daemon.ApprovalDecision{
		Decision: "approve", Approver: "someone",
	}); err != nil {
		t.Fatalf("Approve gate-a: %v", err)
	}
	rec, err := h.jobs.Get(t.Context(), runID)
	if err != nil {
		t.Fatalf("Get run: %v", err)
	}
	if rec.Status != core.JobStatusAwaiting {
		t.Errorf("run status with one approval still open = %q, want awaiting", rec.Status)
	}

	// Decide the second: nothing is waiting on a person any more, so the run
	// stops reporting awaiting. With no downstream steps it finishes outright.
	if err := h.svc.Approve(t.Context(), runID, "gate-b", daemon.ApprovalDecision{
		Decision: "approve", Approver: "someone",
	}); err != nil {
		t.Fatalf("Approve gate-b: %v", err)
	}
	got := waitForRunStatus(t, h.jobs, runID, core.JobStatusSucceeded, 5*time.Second)
	if got == core.JobStatusAwaiting {
		t.Errorf("run still reports awaiting after every approval was decided")
	}
	if got != core.JobStatusSucceeded {
		t.Errorf("run status after both approvals = %q, want succeeded", got)
	}
}

// TestRunStatus_SubgraphPauseStaysRunning guards the line the run status draws.
// A subgraph node parks as `awaiting` too, but that run is not waiting on a
// person — its child graph is executing. Labelling it "Waiting for approval"
// would send someone looking for a decision to make that doesn't exist, so
// only a pause that emitted a pending_url counts.
func TestRunStatus_SubgraphPauseStaysRunning(t *testing.T) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil); err != nil {
		t.Fatalf("issue key: %v", err)
	}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	// A pause with no pending_url — the shape a subgraph node parks in.
	reg := engine.NewRegistry()
	if err := reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "silent_gate",
			Summary:  "Test fixture: parks with no pending_url, like a subgraph waiting on its child.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: j.ID, Status: core.StatusAwaiting, Output: map[string]core.Ref{}}, nil
		},
	}); err != nil {
		t.Fatalf("register silent_gate: %v", err)
	}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "child-waiter", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "sub", Module: "silent_gate"}},
	}
	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}

	// The NODE parks...
	nodeStatus := waitForRunStatus(t, jobs, daemon.NodeJobID(runID, "sub"), core.JobStatusAwaiting, 5*time.Second)
	if nodeStatus != core.JobStatusAwaiting {
		t.Fatalf("node status = %q, want awaiting", nodeStatus)
	}
	// ...but the RUN keeps reporting running: there is nothing to approve.
	// Given a moment, in case the park is carried up asynchronously.
	time.Sleep(150 * time.Millisecond)
	rec, err := jobs.Get(t.Context(), runID)
	if err != nil {
		t.Fatalf("Get run: %v", err)
	}
	if rec.Status != core.JobStatusRunning {
		t.Errorf("run status for a non-approval pause = %q, want running", rec.Status)
	}
}
