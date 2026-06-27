package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// TestPublicWorkspaceOverview_NextRun checks the TV-board wiring: a published,
// cron-triggered flow is "live" and surfaces next_run_at; an unpublished one is
// "needs publish" (the scheduler won't fire it) and surfaces nothing.
func TestPublicWorkspaceOverview_NextRun(t *testing.T) {
	h := newShareHarness(t)
	ctx := context.Background()

	cron := []core.Node{{ID: "c", Module: "cron_trigger", Params: map[string]any{"cron": "*/5 * * * *"}}}
	for _, g := range []core.Graph{
		{ID: "sched", Tenant: "t", Workspace: "ws", Name: "Scheduled", Nodes: cron},
		{ID: "unpub", Tenant: "t", Workspace: "ws", Name: "Unpublished", Nodes: cron},
	} {
		if _, err := h.svc.SaveGraph(ctx, h.editor, g); err != nil {
			t.Fatalf("save %s: %v", g.ID, err)
		}
	}
	// Publish only "sched" → it becomes live; "unpub" stays needs-publish.
	store, err := h.svc.Workspaces.Open("t", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteToEnvironment("sched", workspace.PublishedEnv, "HEAD"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	sh, err := h.svc.CreateWorkspaceShare(ctx, h.editor, "t", "ws")
	if err != nil {
		t.Fatal(err)
	}
	data, err := h.svc.PublicWorkspaceOverview(ctx, sh.Token, time.Now())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	var live, np *daemon.PublicFlowState
	for i := range data.Flows {
		switch data.Flows[i].Name {
		case "Scheduled":
			live = &data.Flows[i]
		case "Unpublished":
			np = &data.Flows[i]
		}
	}
	if live == nil || np == nil {
		t.Fatalf("expected both tiles, got %+v", data.Flows)
	}
	if live.RunStatus != core.FlowLive {
		t.Errorf("published cron flow run_status = %q, want live", live.RunStatus)
	}
	if live.NextRunAt == nil {
		t.Error("live cron flow should surface next_run_at")
	} else if !live.NextRunAt.After(time.Now()) {
		t.Errorf("next_run_at = %v, want a future time", live.NextRunAt)
	}
	if np.RunStatus != core.FlowNeedsPublish {
		t.Errorf("unpublished cron flow run_status = %q, want needs_publish", np.RunStatus)
	}
	if np.NextRunAt != nil {
		t.Errorf("unpublished flow should have no next_run_at, got %v", np.NextRunAt)
	}
}
