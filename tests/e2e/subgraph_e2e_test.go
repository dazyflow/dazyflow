package e2e

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/integrations"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// subgraphHarness builds a stack with a subgraph-aware worker (wired to
// the Service's SubmitChild method) and a workspace that holds both the
// parent and child graph definitions.
type subgraphHarness struct {
	svc   *daemon.Service
	store core.JobStore
	bus   *daemon.MemoryBus
	ws    *workspace.Store
	t     *testing.T
}

func newSubgraphHarness(t *testing.T) *subgraphHarness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	store := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
		Jobs:       store,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, store, eng, bus)
	w.SubGraphRunner = svc
	go func() { _ = w.Run(wctx) }()
	return &subgraphHarness{svc: svc, store: store, bus: bus, ws: wsStore, t: t}
}

// TestSubgraph_E2E_HappyPath runs the headline scenario:
//
//	Parent graph: prep → call_child → downstream
//	Child graph:  receive → emit
//
// The parent's "in" port routes into the child's `receive` node; the
// child's `emit.out` returns to the parent's `result` port; downstream
// runs only after the child terminates.
func TestSubgraph_E2E_HappyPath(t *testing.T) {
	h := newSubgraphHarness(t)

	// Child graph — stores `receive` input and re-emits as `out` from `emit`.
	// Since sleep passes its `in` through to `out`, a child of
	// receive → emit will forward the parent's input back to the parent.
	child := core.Graph{
		ID: "child-flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "receive", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "emit", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "receive", FromPort: "out", To: "emit", ToPort: "in"},
		},
	}
	if _, err := h.ws.Save(child, "test"); err != nil {
		t.Fatalf("save child: %v", err)
	}

	parent := core.Graph{
		ID: "parent-flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "prep", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "call_child", Module: "subgraph", Params: map[string]any{
				"graph_id":  "child-flow",
				"input_map": map[string]any{"in": "receive"},
				"output_map": map[string]any{
					"result": map[string]any{"node": "emit", "port": "out"},
				},
			}},
			{ID: "after", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "prep", FromPort: "out", To: "call_child", ToPort: "in"},
			{From: "call_child", FromPort: "result", To: "after", ToPort: "in"},
		},
	}
	if _, err := h.ws.Save(parent, "test"); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}
	runID, err := h.svc.SubmitGraph(t.Context(), p, parent)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	terminal := waitForFire(t, h.bus, runID, 5*time.Second)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", terminal)
	}

	// The downstream `after` node must have run.
	after, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "after"))
	if after.Status != core.JobStatusSucceeded {
		t.Errorf("after = %q, want succeeded", after.Status)
	}

	// The parent subgraph node resumed with the projected output.
	parentRec, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "call_child"))
	if parentRec.Status != core.JobStatusSucceeded {
		t.Errorf("call_child = %q, want succeeded", parentRec.Status)
	}
	if _, ok := parentRec.Result.Output["result"]; !ok {
		t.Errorf("call_child.result missing from output: %+v", parentRec.Result.Output)
	}
}

// TestSubgraph_E2E_ChildFailurePropagatesToParent asserts that when a
// child graph fails, the parent's subgraph node reports a child_failed
// error and its graph aborts (unless downstream tolerates it via skip).
func TestSubgraph_E2E_ChildFailurePropagatesToParent(t *testing.T) {
	h := newSubgraphHarness(t)

	// Child graph that always fails: a sleep with ms=-1 trips the
	// negative-ms guard in the sleep module.
	child := core.Graph{
		ID: "child-broken", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "broken", Module: "sleep", Params: map[string]any{"ms": -1}},
		},
	}
	if _, err := h.ws.Save(child, "test"); err != nil {
		t.Fatalf("save child: %v", err)
	}

	parent := core.Graph{
		ID: "parent-failing", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "call_child", Module: "subgraph", Params: map[string]any{
				"graph_id": "child-broken",
			}},
		},
	}
	if _, err := h.ws.Save(parent, "test"); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}
	runID, err := h.svc.SubmitGraph(t.Context(), p, parent)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	terminal := waitForFire(t, h.bus, runID, 5*time.Second)
	if terminal != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed", terminal)
	}

	parentRec, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "call_child"))
	if parentRec.Status != core.JobStatusFailed {
		t.Fatalf("call_child = %q", parentRec.Status)
	}
	if parentRec.Result == nil || parentRec.Result.Error == nil ||
		parentRec.Result.Error.Code != "child_failed" {
		t.Errorf("error = %+v, want child_failed", parentRec.Result.Error)
	}
}

// TestSubgraph_E2E_UnknownChildFailsParent verifies that submitting a
// non-existent child surfaces as a subgraph_submit error rather than
// hanging the parent forever.
func TestSubgraph_E2E_UnknownChildFailsParent(t *testing.T) {
	h := newSubgraphHarness(t)

	parent := core.Graph{
		ID: "parent-bad-ref", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "call_child", Module: "subgraph", Params: map[string]any{
				"graph_id": "no-such-graph",
			}},
		},
	}
	if _, err := h.ws.Save(parent, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}
	runID, err := h.svc.SubmitGraph(t.Context(), p, parent)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	terminal := waitForFire(t, h.bus, runID, 5*time.Second)
	if terminal != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed", terminal)
	}
	parentRec, _ := h.store.Get(t.Context(), daemon.NodeJobID(runID, "call_child"))
	if parentRec.Status != core.JobStatusFailed {
		t.Errorf("call_child = %q", parentRec.Status)
	}
	if parentRec.Result == nil || parentRec.Result.Error == nil ||
		parentRec.Result.Error.Code != "subgraph_submit" {
		t.Errorf("error = %+v, want subgraph_submit", parentRec.Result.Error)
	}
}
