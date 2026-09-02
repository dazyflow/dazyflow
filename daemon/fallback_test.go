// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// fallbackHarness reuses the skip-test pattern (boom + source + built-ins).
// We just share the existing newSkipHarness builder — fallback runs against
// the same module set.

func TestFallback_ActivatesOnFailure(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// boom fails; "handler" sleep is its fallback. Handler must run and
	// the graph as a whole must succeed.
	g := core.Graph{
		ID: "fb-activate", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "primary", Module: "boom"},
			{ID: "handler", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "primary", FromPort: "out", To: "handler", ToPort: "in", OnError: core.OnErrorFallback},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}
	primary, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "primary"))
	if primary.Status != core.JobStatusFailed {
		t.Errorf("primary status = %q, want failed", primary.Status)
	}
	handler, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "handler"))
	if handler.Status != core.JobStatusSucceeded {
		t.Errorf("handler status = %q, want succeeded (should have activated)", handler.Status)
	}
}

func TestFallback_DormantOnSuccess(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// source succeeds; the fallback "handler" should NOT run, but should
	// be recorded as Skipped so the graph completes.
	g := core.Graph{
		ID: "fb-dormant", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "primary", Module: "source"},
			{ID: "handler", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "primary", FromPort: "out", To: "handler", ToPort: "in", OnError: core.OnErrorFallback},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded", terminal.Status)
	}
	handler, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "handler"))
	if handler.Status != core.JobStatusSkipped {
		t.Errorf("handler status = %q, want skipped (fallback dormant on success)", handler.Status)
	}
}

func TestFallback_AbsorbsSiblingAbortEdge(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// boom fails. It has TWO outgoing edges:
	//   - default-abort → "lost" sleep (would normally propagate, lost gets Skipped)
	//   - fallback     → "handler" sleep (activates)
	// The graph should succeed because the fallback rescues it.
	g := core.Graph{
		ID: "fb-absorb", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "primary", Module: "boom"},
			{ID: "lost", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "handler", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "primary", FromPort: "out", To: "lost", ToPort: "in"},
			{From: "primary", FromPort: "out", To: "handler", ToPort: "in", OnError: core.OnErrorFallback},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (fallback absorbs abort sibling)", terminal.Status)
	}
	lost, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "lost"))
	if err != nil {
		t.Fatalf("lost record missing: %v", err)
	}
	if lost.Status != core.JobStatusSkipped {
		t.Errorf("lost status = %q, want skipped (blocked by abort but graph survives)", lost.Status)
	}
	handler, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "handler"))
	if handler.Status != core.JobStatusSucceeded {
		t.Errorf("handler status = %q, want succeeded", handler.Status)
	}
}

func TestFallback_CascadesSkipToDownstream(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// boom fails; "handler" is its fallback. "downstream" depends on
	// "lost" (which was blocked) via a default edge — it should cascade
	// to Skipped without ever running.
	g := core.Graph{
		ID: "fb-cascade", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "primary", Module: "boom"},
			{ID: "lost", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "downstream", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "handler", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "primary", FromPort: "out", To: "lost", ToPort: "in"},
			{From: "lost", FromPort: "out", To: "downstream", ToPort: "in"},
			{From: "primary", FromPort: "out", To: "handler", ToPort: "in", OnError: core.OnErrorFallback},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded", terminal.Status)
	}
	downstream, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "downstream"))
	if err != nil {
		t.Fatalf("downstream record missing: %v", err)
	}
	if downstream.Status != core.JobStatusSkipped {
		t.Errorf("downstream status = %q, want skipped (cascaded via lost)", downstream.Status)
	}
}

func TestFallback_MixedInputs(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// "merge" has two preds:
	//   - "alive" succeeds via default edge → contributes input
	//   - "primary" fails; fallback to merge → activates, no input
	// merge runs with alive's contribution only. Graph succeeds.
	g := core.Graph{
		ID: "fb-mixed", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "primary", Module: "boom"},
			{ID: "alive", Module: "source"},
			{ID: "join", Module: "merge"},
		},
		Edges: []core.Edge{
			{From: "alive", FromPort: "out", To: "join", ToPort: "items"},
			{From: "primary", FromPort: "out", To: "join", ToPort: "items", OnError: core.OnErrorFallback},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded", terminal.Status)
	}
	join, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "join"))
	if join.Status != core.JobStatusSucceeded {
		t.Fatalf("join status = %q", join.Status)
	}
	refs, _ := join.Result.Output["out"].Inline.([]core.Ref)
	if len(refs) != 1 {
		t.Errorf("merge collected %d refs, want 1 (fallback edge contributes nothing)", len(refs))
	}
}

func TestFallback_NoFallbackPathStillAborts(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// Same as the abort case from skip-tests, but using a default edge.
	// This exists to confirm the new logic didn't accidentally turn all
	// failures into non-propagating ones.
	g := core.Graph{
		ID: "fb-baseline-abort", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "primary", Module: "boom"},
			{ID: "next", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "primary", FromPort: "out", To: "next", ToPort: "in"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Errorf("graph status = %q, want failed (no fallback path)", terminal.Status)
	}
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "next")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("'next' should not have been recorded (graph aborted before dispatch); err = %v", err)
	}
}

// Sanity check at the AssembleInput level — a fallback edge from a
// succeeded source must not push the source's ref into the destination's
// port. We can validate this indirectly through the merge example above
// (only one ref collected), but a focused engine-level test is cheap.
func TestEngine_FallbackEdgeDoesNotProvideInput(t *testing.T) {
	t.Parallel()
	// Compose a graph and a tiny harness directly so we can assert on
	// the captured Job.Input.
	captured := make(map[string]map[string]core.Ref)
	reg := engine.NewRegistry()
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{ID: "src", Summary: "Test src.", Examples: []core.ParamsExample{{Title: "default"}}, Outputs: []core.Port{{Port: "out"}}},
		Execute: func(_ context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {Ref: "src-data"}}}, nil
		},
	})
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{ID: "sink", Summary: "Test sink.", Examples: []core.ParamsExample{{Title: "default"}}, Inputs: []core.Port{{Port: "in"}}},
		Execute: func(_ context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			captured[j.NodeID] = j.Input
			return core.Result{Status: core.StatusOK}, nil
		},
	})

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
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
		ID: "w", PollInterval: 5 * time.Millisecond,
		MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "fb-no-input", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "src"},
			{ID: "sink", Module: "sink"},
		},
		Edges: []core.Edge{
			// Fallback edge from a succeeded source must NOT push src-data
			// into sink's "in" port.
			{From: "src", FromPort: "out", To: "sink", ToPort: "in", OnError: core.OnErrorFallback},
		},
	}
	graphRunID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q", terminal.Status)
	}
	// sink should have been skipped (dormant fallback edge from succeeded src).
	sinkRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "sink"))
	if sinkRec.Status != core.JobStatusSkipped {
		t.Errorf("sink status = %q, want skipped", sinkRec.Status)
	}
	if _, ran := captured["sink"]; ran {
		t.Errorf("sink should not have been executed: captured input = %+v", captured["sink"])
	}
}
