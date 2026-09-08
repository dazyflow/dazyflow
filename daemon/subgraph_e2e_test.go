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
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

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

func TestSubgraph_EndToEnd_OutputProjection(t *testing.T) {
	t.Parallel()
	h := newSubgraphHarness(t)

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

	after, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "after"))
	if err != nil || after.Status != core.JobStatusSucceeded {
		t.Fatalf("after status = %q (err=%v), want succeeded", after.Status, err)
	}
	call, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "call"))
	if call.Status != core.JobStatusSucceeded {
		t.Fatalf("call (subgraph) status = %q, want succeeded", call.Status)
	}
}

func TestSubgraph_ChildFailurePropagates(t *testing.T) {
	t.Parallel()
	h := newSubgraphHarness(t)

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

func TestSubgraph_DepthCapStopsRecursion(t *testing.T) {
	t.Parallel()
	h := newSubgraphHarness(t)

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

func TestSubgraph_MissingChildGraphFailsParent(t *testing.T) {
	t.Parallel()
	h := newSubgraphHarness(t)
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
