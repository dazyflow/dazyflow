// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// fireGraphSvc assembles a Service whose scheduler can be driven through the
// skip/error legs of fireGraph directly (white-box).
func fireGraphSvc(t *testing.T) (*Service, *workspace.Store, core.JobStore) {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "ed", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        NewMemoryBus(),
	}
	return svc, wsStore, jobs
}

// TestFireGraph_TriggerQuotaSkip drives the plan-gate (checkTriggerQuota) skip
// leg of fireGraph: with the free-polling gate on and a free tenant, the fire
// returns early before opening the workspace, so no run is submitted.
func TestFireGraph_TriggerQuotaSkip(t *testing.T) {
	svc, _, jobs := fireGraphSvc(t)
	svc.FreePollingDisabled = true
	plans := NewMemPlanStore()
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "acme", Plan: PlanFree})
	svc.Plans = plans

	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "g", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	if runs, _ := jobs.ListByGraph(t.Context(), "g"); len(runs) != 0 {
		t.Fatalf("trigger-gated fire produced %d runs, want 0", len(runs))
	}
}

// TestFireGraph_RunCapSkip drives the ErrPlanLimit-from-SubmitGraph leg of
// fireGraph: the trigger gate passes, but the monthly run cap is already hit,
// so SubmitGraph returns ErrPlanLimit — AddSkippedRun is counted, the Runs-list
// marker is written, and no real run is submitted.
func TestFireGraph_RunCapSkip(t *testing.T) {
	svc, ws, jobs := fireGraphSvc(t)
	// Free tenant with a 1-run/month cap, already consumed.
	plans := NewMemPlanStore()
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "acme", Plan: PlanFree})
	svc.Plans = plans
	svc.FreeRunsPerMonth = 1
	usage := NewMemUsageStore()
	svc.Usage = usage

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_ = usage.AddRun(t.Context(), "acme", now) // consume the only allowed run

	g := core.Graph{
		ID: "capped", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	commit, _ := ws.Save(g, "u")
	_ = ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit)

	sched := NewScheduler(svc)
	sched.SetClock(func() time.Time { return now })
	e := &scheduledGraph{graphID: "capped", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	// A skipped-run marker should have been written to the Runs list.
	recs, err := jobs.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: "acme", Status: core.JobStatusSkipped, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListGraphRuns: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("skipped markers = %d, want 1", len(recs))
	}
	// AddSkippedRun counted the skip.
	buckets, _ := usage.Usage(t.Context(), "acme", 1)
	if len(buckets) == 0 || buckets[0].SkippedRuns == 0 {
		t.Errorf("skipped-run counter not incremented: %+v", buckets)
	}
}

// TestFireGraph_NotPublishedSkip covers the belt-and-braces not-published gate
// inside fireGraph: a saved-but-unpublished flow never fires.
func TestFireGraph_NotPublishedSkip(t *testing.T) {
	svc, ws, jobs := fireGraphSvc(t)
	// Save to HEAD but do NOT promote/publish.
	_, _ = ws.Save(core.Graph{
		ID: "draft", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}, "u")

	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "draft", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	runs, _ := jobs.ListByGraph(t.Context(), "draft")
	if len(runs) != 0 {
		t.Fatalf("unpublished flow fired %d runs, want 0", len(runs))
	}
}

// TestFireGraph_OpenWorkspaceError covers fireGraph's open-workspace failure
// leg: an entry for a tenant/workspace with no store does nothing (logged).
func TestFireGraph_OpenWorkspaceError(t *testing.T) {
	svc, _, jobs := fireGraphSvc(t)
	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "g", tenant: "ghost", workspace: "nope"}
	sched.fireGraph(context.Background(), e)
	runs, _ := jobs.ListByGraph(t.Context(), "g")
	if len(runs) != 0 {
		t.Fatalf("missing-workspace fire produced %d runs, want 0", len(runs))
	}
}

// TestFireGraph_HappyPath fires a published graph directly and confirms a run
// is submitted.
func TestFireGraph_HappyPath(t *testing.T) {
	svc, ws, jobs := fireGraphSvc(t)
	g := core.Graph{
		ID: "live", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	commit, err := ws.Save(g, "u")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "live", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	runs, _ := jobs.ListByGraph(t.Context(), "live")
	if len(runs) == 0 {
		t.Fatal("published fire produced no run")
	}
}
