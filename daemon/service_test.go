// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

func TestGraphResultFromRecord_Cov(t *testing.T) {
	// Succeeded -> OK.
	if r := graphResultFromRecord(core.JobRecord{GraphID: "g", Status: core.JobStatusSucceeded}); r.Status != core.StatusOK {
		t.Errorf("succeeded -> %v", r.Status)
	}
	// Failed -> error.
	if r := graphResultFromRecord(core.JobRecord{GraphID: "g", Status: core.JobStatusFailed}); r.Status != core.StatusError {
		t.Errorf("failed -> %v", r.Status)
	}
	// Cancelled -> error.
	if r := graphResultFromRecord(core.JobRecord{GraphID: "g", Status: core.JobStatusCancelled}); r.Status != core.StatusError {
		t.Errorf("cancelled -> %v", r.Status)
	}
	// Result overrides status + carries error.
	je := &core.JobError{Code: "x"}
	r := graphResultFromRecord(core.JobRecord{
		GraphID: "g", Status: core.JobStatusSucceeded,
		Result: &core.Result{Status: core.StatusError, Error: je},
	})
	if r.Status != core.StatusError || r.Error != je {
		t.Errorf("result override = %+v", r)
	}
}

func TestServiceRunLog_Cov(t *testing.T) {
	jobs := jobstore.NewMemory()
	rlog := NewMemRunLogStore()
	svc := &Service{
		Jobs:    jobs,
		Bus:     NewMemoryBus(),
		Engine:  &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		RunLogs: rlog,
	}
	ctx := context.Background()
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws"}

	// Seed a run record + a couple log lines.
	rec := core.JobRecord{ID: "run1", Tenant: "t", Workspace: "ws", GraphID: "g", Status: core.JobStatusSucceeded, EnqueuedAt: time.Now()}
	if err := jobs.Enqueue(ctx, rec); err != nil {
		t.Fatalf("enqueue rec: %v", err)
	}
	_ = rlog.AppendRunLog(ctx, RunLogEntry{RunID: "run1", TS: time.Now(), Kind: "progress", Message: "a"})
	_ = rlog.AppendRunLog(ctx, RunLogEntry{RunID: "run1", TS: time.Now(), Kind: "progress", Message: "b"})

	page, err := svc.RunLogPage(ctx, p, "run1", 0, 0)
	if err != nil || len(page) != 2 {
		t.Fatalf("RunLogPage = %d / %v, want 2", len(page), err)
	}

	// Cross-tenant principal can't read.
	other := core.Principal{Subject: "o", Tenant: "other", Workspace: "ws"}
	if _, err := svc.RunLogPage(ctx, other, "run1", 0, 0); err == nil {
		t.Fatal("cross-tenant RunLogPage should fail")
	}

	// Delete.
	n, err := svc.DeleteRunLog(ctx, p, "run1")
	if err != nil || n != 2 {
		t.Fatalf("DeleteRunLog = %d / %v, want 2", n, err)
	}
	page, _ = svc.RunLogPage(ctx, p, "run1", 0, 0)
	if len(page) != 0 {
		t.Fatalf("after delete = %d lines, want 0", len(page))
	}

	// Nil run-log store -> errors.
	bare := &Service{Jobs: jobs, Engine: svc.Engine}
	if _, err := bare.RunLogPage(ctx, p, "run1", 0, 0); err == nil {
		t.Fatal("nil RunLogs RunLogPage should error")
	}
	if _, err := bare.DeleteRunLog(ctx, p, "run1"); err == nil {
		t.Fatal("nil RunLogs DeleteRunLog should error")
	}
}

// covAdminPrincipal has the full graph permission set + workspace binding
// matching the gateway harness ("t"/"ws").
var covAdminPrincipal = core.Principal{
	Subject: "u", Tenant: "t", Workspace: "ws",
	Roles: []core.Role{{Name: "admin", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}},
}

func covSeedFlow(t *testing.T, h *gatewayHarness, id string) {
	t.Helper()
	g := core.Graph{ID: id, Tenant: "t", Workspace: "ws", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	if _, err := h.ws.Save(g, "u"); err != nil {
		t.Fatalf("seed flow: %v", err)
	}
}

func TestService_SetFlowEnabled(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()
	covSeedFlow(t, h, "f1")

	// Disable.
	if _, err := h.svc.SetFlowEnabled(ctx, covAdminPrincipal, "t", "ws", "f1", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	g, _ := h.ws.Load("f1")
	if !g.Disabled {
		t.Fatal("flow not disabled")
	}
	// Idempotent disable (already disabled -> no-op, empty commit).
	if c, err := h.svc.SetFlowEnabled(ctx, covAdminPrincipal, "t", "ws", "f1", false); err != nil || c != "" {
		t.Fatalf("idempotent disable = %q / %v", c, err)
	}
	// Re-enable.
	if _, err := h.svc.SetFlowEnabled(ctx, covAdminPrincipal, "t", "ws", "f1", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	g, _ = h.ws.Load("f1")
	if g.Disabled {
		t.Fatal("flow still disabled")
	}

	// Wrong tenant binding -> permission error.
	bad := covAdminPrincipal
	bad.Tenant = "other"
	if _, err := h.svc.SetFlowEnabled(ctx, bad, "t", "ws", "f1", false); err == nil {
		t.Fatal("expected tenant-mismatch error")
	}
}

func TestService_PublishUnpublishFlow(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()
	covSeedFlow(t, h, "pub")

	commit, err := h.svc.PublishFlow(ctx, covAdminPrincipal, "t", "ws", "pub", "", "v1.0")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if commit == "" {
		t.Fatal("empty published commit")
	}

	info, err := h.svc.PublishedInfo(ctx, covAdminPrincipal, "t", "ws", "pub")
	if err != nil {
		t.Fatalf("published info: %v", err)
	}
	if !info.Published || info.PublishedLabel != "v1.0" {
		t.Fatalf("info = %+v, want published with label v1.0", info)
	}

	// Unpublish.
	if err := h.svc.UnpublishFlow(ctx, covAdminPrincipal, "t", "ws", "pub"); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	info, _ = h.svc.PublishedInfo(ctx, covAdminPrincipal, "t", "ws", "pub")
	if info.Published {
		t.Fatal("still published after unpublish")
	}

	// Publish a missing flow -> error.
	if _, err := h.svc.PublishFlow(ctx, covAdminPrincipal, "t", "ws", "ghost", "", ""); err == nil {
		t.Fatal("publish missing flow should error")
	}

	// Without graph:admin -> permission error.
	editor := core.Principal{Subject: "e", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "viewer", Permissions: []core.Permission{core.PermGraphRun}}}}
	if _, err := h.svc.PublishFlow(ctx, editor, "t", "ws", "pub", "", ""); err == nil {
		t.Fatal("non-admin publish should be denied")
	}
	if err := h.svc.UnpublishFlow(ctx, editor, "t", "ws", "pub"); err == nil {
		t.Fatal("non-admin unpublish should be denied")
	}
}

// The editor's "your draft has changes that aren't live yet" prompt reads
// Dirty, and the diff view next to it ignores canvas cosmetics. So a moved
// step, a note, a bent wire, or a pause must not report Dirty — that is the
// state where the toolbar prompts to publish and the diff it links to says
// the draft matches the live version.
func TestService_PublishedInfo_DirtyIgnoresCosmetics(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()
	covSeedFlow(t, h, "cosm")

	if _, err := h.svc.PublishFlow(ctx, covAdminPrincipal, "t", "ws", "cosm", "", ""); err != nil {
		t.Fatalf("publish: %v", err)
	}
	info, err := h.svc.PublishedInfo(ctx, covAdminPrincipal, "t", "ws", "cosm")
	if err != nil {
		t.Fatalf("published info: %v", err)
	}
	if info.Dirty {
		t.Fatal("freshly published flow reported dirty")
	}

	// Canvas-only edits, saved the way the editor's autosave saves them.
	g, _ := h.ws.Load("cosm")
	g.Nodes[0].Position = &core.Position{X: 420, Y: 240}
	g.Frames = []core.Frame{{ID: "fr1", Title: "Intake", Width: 360, Height: 240}}
	g.Disabled = true
	if _, err := h.ws.Save(g, "u"); err != nil {
		t.Fatalf("save layout: %v", err)
	}
	info, _ = h.svc.PublishedInfo(ctx, covAdminPrincipal, "t", "ws", "cosm")
	if info.Dirty {
		t.Fatal("layout/pause-only edit reported as unpublished changes")
	}

	// A real change still does.
	g.Nodes[0].Params = map[string]any{"to": "ops@example.com"}
	if _, err := h.ws.Save(g, "u"); err != nil {
		t.Fatalf("save params: %v", err)
	}
	info, _ = h.svc.PublishedInfo(ctx, covAdminPrincipal, "t", "ws", "cosm")
	if !info.Dirty {
		t.Fatal("param edit not reported as unpublished changes")
	}
}

func TestService_RestoreAndPromoteFlow(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()
	covSeedFlow(t, h, "r1")

	// Save a second revision so there's history to restore from.
	v1, _ := h.ws.History("r1", 1)
	g2 := core.Graph{ID: "r1", Tenant: "t", Workspace: "ws", Nodes: []core.Node{
		{ID: "a", Module: "noop"}, {ID: "b", Module: "noop"},
	}}
	if _, err := h.ws.Save(g2, "u"); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	// Restore back to v1 (one node).
	_, head, err := h.svc.RestoreFlow(ctx, covAdminPrincipal, "t", "ws", "r1", v1[0].Commit)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(head.Nodes) != 1 {
		t.Fatalf("restored head has %d nodes, want 1", len(head.Nodes))
	}

	// Promote HEAD into a custom environment.
	if err := h.svc.PromoteGraph(ctx, covAdminPrincipal, "t", "ws", "r1", workspace.PublishedEnv, "HEAD"); err != nil {
		t.Fatalf("promote: %v", err)
	}

	// Restore an unknown ref -> error.
	if _, _, err := h.svc.RestoreFlow(ctx, covAdminPrincipal, "t", "ws", "r1", "deadbeef"); err == nil {
		t.Fatal("restore unknown ref should error")
	}
}

func TestService_ListJobsForGraphAndLimits(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()
	covSeedFlow(t, h, "j1")

	// Submit a run so there's a job record for the graph.
	g, _ := h.ws.Load("j1")
	g.Nodes = []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}}
	runID, err := h.svc.SubmitGraph(ctx, covAdminPrincipal, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	jobs, err := h.svc.ListJobsForGraph(ctx, covAdminPrincipal, "j1")
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	found := false
	for _, j := range jobs {
		if j.ID == runID {
			found = true
		}
	}
	if !found {
		t.Fatalf("run %s not in ListJobsForGraph result (%d records)", runID, len(jobs))
	}

	// A different tenant sees none of t's jobs.
	other := core.Principal{Subject: "o", Tenant: "other", Workspace: "ws"}
	if js, _ := h.svc.ListJobsForGraph(ctx, other, "j1"); len(js) != 0 {
		t.Fatalf("cross-tenant ListJobsForGraph returned %d, want 0", len(js))
	}

	// Effective-limits accessors don't panic and return a consistent value.
	lim := h.svc.EffectiveLimitsFor(ctx, "t")
	if h.svc.RunLogRetentionDays(ctx, "t") != lim.RetentionDays {
		t.Fatalf("RunLogRetentionDays != EffectiveLimitsFor.RetentionDays")
	}
}
