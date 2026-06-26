package daemon

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// countGraphRuns returns how many of a tenant's graph runs are in the status.
func countGraphRuns(t *testing.T, s *Service, tenant string, status core.JobStatus) int {
	t.Helper()
	recs, err := s.Jobs.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: tenant, Status: status, Limit: 100,
	})
	if err != nil {
		t.Fatalf("ListGraphRuns(%s): %v", status, err)
	}
	return len(recs)
}

// TestConcurrencyAdmissionAndPromotion verifies the true per-tenant concurrency
// cap: a free tenant over max_concurrency starts runs PENDING (queued), and the
// promotion sweep starts the next one only after a running slot frees.
func TestConcurrencyAdmissionAndPromotion(t *testing.T) {
	jobs := jobstore.NewMemory()
	svc := &Service{
		Jobs:               jobs,
		Bus:                NewMemoryBus(),
		Engine:             &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Plans:              NewMemPlanStore(), // every tenant free
		FreeMaxConcurrency: 2,
	}
	role := core.Role{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	// A one-node graph whose node never completes on its own (no worker runs in
	// this test), so an admitted run stays "running" and occupies a slot.
	mkGraph := func(id string) core.Graph {
		return core.Graph{
			ID: id, Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
		}
	}

	var ids []string
	for i := 0; i < 4; i++ {
		id, err := svc.SubmitGraphWithSeed(t.Context(), p, mkGraph("g"), nil)
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Cap is 2: first two admitted (running), last two held pending (queued).
	if got := countGraphRuns(t, svc, "t", core.JobStatusRunning); got != 2 {
		t.Fatalf("running after 4 submits = %d, want 2 (the cap)", got)
	}
	if got := countGraphRuns(t, svc, "t", core.JobStatusQueued); got != 2 {
		t.Fatalf("pending after 4 submits = %d, want 2", got)
	}

	// Free a slot: complete the first run. Then a sweep should promote exactly
	// one pending run (running back to 2, pending down to 1).
	if err := jobs.Complete(t.Context(), ids[0], core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("complete run 0: %v", err)
	}
	svc.SweepPromotePending(t.Context())

	if got := countGraphRuns(t, svc, "t", core.JobStatusRunning); got != 2 {
		t.Fatalf("running after promote = %d, want 2", got)
	}
	if got := countGraphRuns(t, svc, "t", core.JobStatusQueued); got != 1 {
		t.Fatalf("pending after promote = %d, want 1", got)
	}

	// The promoted run must have had its root node dispatched (a queued node
	// record), proving startPendingRun actually started it rather than just
	// flipping the status.
	nodes, err := svc.Jobs.ListNodeRecords(t.Context(), core.ListNodeRecordsOpts{
		Tenant: "t", Status: core.JobStatusQueued, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list node records: %v", err)
	}
	if len(nodes) < 3 {
		t.Fatalf("queued node records = %d, want >=3 (one root per started run)", len(nodes))
	}
}

// TestConcurrencyUncappedAdmitsAll verifies pro/unlimited tenants never queue.
func TestConcurrencyUncappedAdmitsAll(t *testing.T) {
	jobs := jobstore.NewMemory()
	svc := &Service{
		Jobs:               jobs,
		Bus:                NewMemoryBus(),
		Engine:             &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Plans:              fakePlans{p: TenantPlan{Tenant: "t", Plan: PlanPro}},
		FreeMaxConcurrency: 2, // ignored for a pro tenant
	}
	role := core.Role{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	for i := 0; i < 5; i++ {
		g := core.Graph{
			ID: "g", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
		}
		if _, err := svc.SubmitGraphWithSeed(t.Context(), p, g, nil); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	if got := countGraphRuns(t, svc, "t", core.JobStatusQueued); got != 0 {
		t.Fatalf("pro tenant queued %d runs, want 0 (uncapped)", got)
	}
	if got := countGraphRuns(t, svc, "t", core.JobStatusRunning); got != 5 {
		t.Fatalf("pro tenant running = %d, want 5", got)
	}
}
