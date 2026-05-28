// Package e2e wires the whole stack together — auth, workspace storage,
// JobStore, engine, native modules — and exercises real user flows
// against it. These tests are the only ones that would catch
// integration-level regressions like silently incompatible interfaces.
package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/integrations" // register sleep/merge/file_*
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

func setupStack(t *testing.T) (*daemon.Service, string, string) {
	t.Helper()

	ks := auth.NewMemKeyStore()
	editorRole := core.Role{
		Name: "editor",
		Permissions: []core.Permission{
			core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		},
	}
	otherRole := core.Role{
		Name:        "editor",
		Permissions: []core.Permission{core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin},
	}

	_, aliceKey, err := auth.IssueAPIKey(ks, t.Context(), "alice", "acme", "ws1", "alice@acme", []core.Role{editorRole}, nil)
	if err != nil {
		t.Fatalf("issue alice key: %v", err)
	}
	_, bobKey, err := auth.IssueAPIKey(ks, t.Context(), "bob", "globex", "ws1", "bob@globex", []core.Role{otherRole}, nil)
	if err != nil {
		t.Fatalf("issue bob key: %v", err)
	}

	acmeWS, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("acme workspace: %v", err)
	}
	globexWS, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("globex workspace: %v", err)
	}

	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth: auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{
			"acme/ws1":   acmeWS,
			"globex/ws1": globexWS,
		},
		Jobs:   jobs,
		Engine: eng,
		Bus:    bus,
	}

	// Run a worker in the background so SubmitGraph + WaitGraph actually
	// make progress. Tunables are tightened so tests don't wait long.
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	t.Cleanup(cancelWorker)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:              "test-worker",
		PollInterval:    5 * time.Millisecond,
		LeaseDuration:   5 * time.Second,
		LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)
	go func() {
		_ = w.Run(workerCtx)
	}()

	return svc, aliceKey, bobKey
}

func TestE2E_AuthSaveLoadRun(t *testing.T) {
	svc, aliceKey, _ := setupStack(t)
	ctx := t.Context()

	// 1. Authenticate via API key.
	alice, err := svc.Authenticate(ctx, aliceKey)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if alice.Tenant != "acme" {
		t.Fatalf("alice.Tenant = %q", alice.Tenant)
	}

	// 2. Save a graph.
	graph := core.Graph{
		ID:        "pipeline",
		Tenant:    "acme",
		Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "warmup", Module: "sleep", Params: map[string]any{"ms": 10}},
			{ID: "main", Module: "sleep", Params: map[string]any{"ms": 20}},
		},
		Edges: []core.Edge{
			{From: "warmup", FromPort: "out", To: "main", ToPort: "in"},
		},
	}
	commit, err := svc.SaveGraph(ctx, alice, graph)
	if err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}
	if commit == "" {
		t.Fatal("expected commit hash")
	}

	// 3. List graphs — should include "pipeline".
	ids, err := svc.ListGraphs(ctx, alice, "acme", "ws1")
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "pipeline" {
		t.Errorf("ListGraphs = %v, want [pipeline]", ids)
	}

	// 4. Load it back.
	loaded, err := svc.LoadGraph(ctx, alice, "acme", "ws1", "pipeline", "")
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}
	if len(loaded.Nodes) != 2 {
		t.Errorf("loaded.Nodes = %d, want 2", len(loaded.Nodes))
	}

	// 5. Run it.
	result, jobID, err := svc.RunGraph(ctx, alice, loaded, nil)
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if result.Status != core.StatusOK {
		t.Fatalf("graph result = %q (%+v)", result.Status, result.Error)
	}

	// 6. JobStore should hold a succeeded graph-record plus a node-record
	// per node, all reachable through ListJobsForGraph.
	rec, err := svc.GetJob(ctx, alice, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if rec.Kind != core.JobKindGraph {
		t.Errorf("graph-record Kind = %q, want graph", rec.Kind)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("graph status = %q, want succeeded", rec.Status)
	}

	// 7. ListJobsForGraph yields the graph-record + one per node.
	jobs, err := svc.ListJobsForGraph(ctx, alice, "pipeline")
	if err != nil {
		t.Fatalf("ListJobsForGraph: %v", err)
	}
	// 1 graph-record + 2 node-records (warmup, main)
	if len(jobs) != 3 {
		t.Errorf("len(jobs) = %d, want 3 (graph + 2 nodes)", len(jobs))
	}
	var nodeWorkers int
	for _, j := range jobs {
		if j.Kind == core.JobKindNode && j.WorkerID != "" {
			nodeWorkers++
		}
	}
	if nodeWorkers != 2 {
		t.Errorf("expected both node-records to have a worker; got %d/2", nodeWorkers)
	}
}

func TestE2E_CrossTenantIsolation(t *testing.T) {
	svc, aliceKey, bobKey := setupStack(t)
	ctx := t.Context()
	alice, _ := svc.Authenticate(ctx, aliceKey)
	bob, _ := svc.Authenticate(ctx, bobKey)

	// Alice saves a graph in acme.
	graph := core.Graph{
		ID: "secret-pipeline", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
	}
	if _, err := svc.SaveGraph(ctx, alice, graph); err != nil {
		t.Fatalf("SaveGraph as alice: %v", err)
	}

	// Bob tries to read it. Must fail.
	_, err := svc.LoadGraph(ctx, bob, "acme", "ws1", "secret-pipeline", "")
	if !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("bob loading acme graph: err = %v, want ErrUnauthorized", err)
	}

	// Bob tries to list acme's graphs. Must fail.
	if _, err := svc.ListGraphs(ctx, bob, "acme", "ws1"); !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("bob listing acme graphs: err = %v, want ErrUnauthorized", err)
	}

	// Bob tries to run a cross-tenant graph. Must fail.
	_, _, err = svc.RunGraph(ctx, bob, graph, nil)
	if !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("bob running acme graph: err = %v, want ErrUnauthorized", err)
	}
}

func TestE2E_GraphHistoryAndPromotion(t *testing.T) {
	svc, aliceKey, _ := setupStack(t)
	ctx := t.Context()
	alice, _ := svc.Authenticate(ctx, aliceKey)

	g := core.Graph{
		ID: "etl", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "v1", Module: "sleep", Params: map[string]any{"ms": 1}}},
	}
	c1, err := svc.SaveGraph(ctx, alice, g)
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}

	g.Nodes = []core.Node{{ID: "v2", Module: "sleep", Params: map[string]any{"ms": 1}}}
	c2, err := svc.SaveGraph(ctx, alice, g)
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if c1 == c2 {
		t.Fatal("commits should differ across saves")
	}

	// HEAD points at v2.
	head, err := svc.LoadGraph(ctx, alice, "acme", "ws1", "etl", "")
	if err != nil {
		t.Fatalf("LoadGraph head: %v", err)
	}
	if head.Nodes[0].ID != "v2" {
		t.Errorf("HEAD = %+v, want v2", head.Nodes)
	}

	// Load v1 by ref.
	older, err := svc.LoadGraph(ctx, alice, "acme", "ws1", "etl", c1)
	if err != nil {
		t.Fatalf("LoadGraph at c1: %v", err)
	}
	if older.Nodes[0].ID != "v1" {
		t.Errorf("@c1 = %+v, want v1", older.Nodes)
	}

	// Promote v1 to production.
	if err := svc.PromoteGraph(ctx, alice, "acme", "ws1", "etl", "production", c1); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Loading by env tag returns v1.
	prod, err := svc.LoadGraph(ctx, alice, "acme", "ws1", "etl", "refs/tags/graphs/etl/production")
	if err != nil {
		t.Fatalf("LoadGraph at production tag: %v", err)
	}
	if prod.Nodes[0].ID != "v1" {
		t.Errorf("production = %+v, want v1", prod.Nodes)
	}
}

func TestE2E_InvalidGraphRejected(t *testing.T) {
	svc, aliceKey, _ := setupStack(t)
	ctx := t.Context()
	alice, _ := svc.Authenticate(ctx, aliceKey)

	// Cyclic graph.
	g := core.Graph{
		ID: "bad", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "b", FromPort: "out", To: "a", ToPort: "in"},
		},
	}
	if _, err := svc.SaveGraph(ctx, alice, g); err == nil {
		t.Fatal("expected cycle to be rejected")
	}
}

func TestE2E_PrincipalLacksPermission(t *testing.T) {
	svc, aliceKey, _ := setupStack(t)
	ctx := t.Context()
	alice, _ := svc.Authenticate(ctx, aliceKey)

	// Strip editor permission to verify SaveGraph enforces graph:edit.
	alice.Roles = []core.Role{{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}}

	g := core.Graph{
		ID: "x", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
	}
	if _, err := svc.SaveGraph(ctx, alice, g); !errors.Is(err, core.ErrUnauthorized) {
		t.Errorf("save without graph:edit: err = %v, want ErrUnauthorized", err)
	}
}

func TestE2E_FailingGraphRecordedAsFailed(t *testing.T) {
	svc, aliceKey, _ := setupStack(t)
	ctx := t.Context()
	alice, _ := svc.Authenticate(ctx, aliceKey)

	// Reference an unknown module so validation fails.
	g := core.Graph{
		ID: "broken", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "x", Module: "nonexistent"}},
	}
	// Save bypasses module validation (only structural). Run should fail.
	if _, err := svc.SaveGraph(ctx, alice, g); err != nil {
		t.Fatalf("SaveGraph (structural ok): %v", err)
	}
	loaded, _ := svc.LoadGraph(ctx, alice, "acme", "ws1", "broken", "")
	_, jobID, err := svc.RunGraph(ctx, alice, loaded, nil)
	if err == nil {
		t.Fatal("expected RunGraph to fail on unknown module")
	}
	if jobID == "" {
		t.Fatal("jobID should still be returned for the audit record")
	}
	rec, err := svc.GetJob(ctx, alice, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if rec.Status != core.JobStatusFailed {
		t.Errorf("job status = %q, want failed", rec.Status)
	}
}

func TestE2E_ProgressStreaming(t *testing.T) {
	svc, aliceKey, _ := setupStack(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	alice, _ := svc.Authenticate(ctx, aliceKey)

	g := core.Graph{
		ID: "watched", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 100}}},
	}
	if _, err := svc.SaveGraph(ctx, alice, g); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := svc.LoadGraph(ctx, alice, "acme", "ws1", "watched", "")

	progress := make(chan engine.GraphProgress, 32)
	result, _, err := svc.RunGraph(ctx, alice, loaded, progress)
	if err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if result.Status != core.StatusOK {
		t.Fatalf("status = %q", result.Status)
	}
	var events int
	for range progress {
		events++
	}
	if events == 0 {
		t.Error("expected at least one progress event from the sleep module")
	}
}
