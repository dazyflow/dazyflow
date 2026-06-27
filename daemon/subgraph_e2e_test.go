// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// subgraphHarness wires a Service + a worker whose SubGraphRunner points back
// at the Service, so a parked subgraph node actually submits and resumes a
// child graph end-to-end. It uses the real `subgraph` drop from the default
// registry.
type subgraphHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       *daemon.MemoryBus
	ws        *workspace.Store
	principal core.Principal
}

func newSubgraphHarness(t *testing.T) *subgraphHarness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
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
		ID: "sgw", PollInterval: 5 * time.Millisecond,
		LeaseDuration: 2 * time.Second, LeaseRenewEvery: 500 * time.Millisecond,
	}, jobs, eng, bus)
	w.SubGraphRunner = svc
	go func() { _ = w.Run(wctx) }()

	return &subgraphHarness{svc: svc, jobs: jobs, bus: bus, ws: ws, principal: p}
}

// TestSubgraph_EndToEnd_OutputProjection covers SubmitChild,
// submitGraphWithParent, maybeSubmitChild, maybeResumeParent and
// projectChildOutputs: a parent subgraph node seeds a child node's input and
// projects a child node's output back up to the parent's port.
func TestSubgraph_EndToEnd_OutputProjection(t *testing.T) {
	h := newSubgraphHarness(t)

	// Child graph: a seeded start node feeding a downstream echo node. The
	// subgraph seeds "start"'s input and projects "echo"'s pass output up.
	child := core.Graph{
		ID: "child", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "start", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "echo", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "start", FromPort: "pass", To: "echo", ToPort: "pass"},
		},
	}
	if _, err := h.ws.Save(child, "u"); err != nil {
		t.Fatalf("save child: %v", err)
	}

	parent := core.Graph{
		ID: "parent", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "seed", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "call", Module: "subgraph", Params: map[string]any{
				"graph_id":   "child",
				"input_map":  map[string]any{"in": "start"},
				"output_map": map[string]any{"out": map[string]any{"node": "echo", "port": "pass"}},
			}},
			{ID: "after", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "seed", FromPort: "pass", To: "call", ToPort: "in"},
			{From: "call", FromPort: "out", To: "after", ToPort: "pass"},
		},
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, parent)
	if err != nil {
		t.Fatalf("Submit parent: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 8*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("parent status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}

	// The "after" node ran, meaning the parent's subgraph node resumed
	// successfully and dispatched downstream.
	after, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "after"))
	if err != nil || after.Status != core.JobStatusSucceeded {
		t.Fatalf("after status = %q (err=%v), want succeeded", after.Status, err)
	}
	// The subgraph node ("call") itself succeeded.
	call, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "call"))
	if call.Status != core.JobStatusSucceeded {
		t.Fatalf("call (subgraph) status = %q, want succeeded", call.Status)
	}
}

// TestSubgraph_ChildFailurePropagates covers maybeResumeParent's child-failure
// leg: a child node fails, the parent subgraph node is failed with
// child_failed, and the parent graph fails.
func TestSubgraph_ChildFailurePropagates(t *testing.T) {
	h := newSubgraphHarness(t)

	// Child graph whose only node references a nonexistent module -> fails.
	child := core.Graph{
		ID: "badchild", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "boom", Module: "nonexistent"}},
	}
	if _, err := h.ws.Save(child, "u"); err != nil {
		t.Fatalf("save child: %v", err)
	}

	parent := core.Graph{
		ID: "parent-fail", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": "badchild"}},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, parent)
	if err != nil {
		t.Fatalf("Submit parent: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 8*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("parent status = %q, want failed", terminal.Status)
	}
	call, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "call"))
	if call.Status != core.JobStatusFailed {
		t.Fatalf("call status = %q, want failed", call.Status)
	}
	if call.Result == nil || call.Result.Error == nil || call.Result.Error.Code != "child_failed" {
		t.Fatalf("call error = %+v, want code=child_failed", call.Result.Error)
	}
}

// TestSubgraph_DepthCapStopsRecursion covers subgraphDepth and the
// nesting-cap leg of SubmitChild: a flow that references itself via a subgraph
// node recurses until the depth cap, then the deepest submit is refused — so
// the top-level run fails rather than spawning children forever.
func TestSubgraph_DepthCapStopsRecursion(t *testing.T) {
	h := newSubgraphHarness(t)

	// A graph whose only node is a subgraph node pointing at ITSELF.
	selfRef := core.Graph{
		ID: "loopy", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": "loopy"}},
		},
	}
	if _, err := h.ws.Save(selfRef, "u"); err != nil {
		t.Fatalf("save self-ref: %v", err)
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, selfRef)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 10*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("self-referencing run status = %q, want failed (depth cap)", terminal.Status)
	}
}

// TestSubgraph_MissingChildGraphFailsParent covers SubmitChild's load-error
// path (and worker.maybeSubmitChild's submit-failure handling): the referenced
// child graph does not exist, so the parent node fails with subgraph_submit.
func TestSubgraph_MissingChildGraphFailsParent(t *testing.T) {
	h := newSubgraphHarness(t)
	// Save *some* graph so the workspace has a HEAD to load from; the child
	// we reference ("ghost") is absent.
	_, _ = h.ws.Save(core.Graph{ID: "present", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}}}, "u")

	parent := core.Graph{
		ID: "parent-missing", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "call", Module: "subgraph", Params: map[string]any{"graph_id": "ghost"}},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, parent)
	if err != nil {
		t.Fatalf("Submit parent: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 8*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("parent status = %q, want failed", terminal.Status)
	}
	call, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "call"))
	if call.Result == nil || call.Result.Error == nil || call.Result.Error.Code != "subgraph_submit" {
		t.Fatalf("call error = %+v, want code=subgraph_submit", call.Result.Error)
	}
}
