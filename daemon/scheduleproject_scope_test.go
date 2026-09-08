// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// Pins the scope rule: a workspace whose listing FAILED contributes no live
// flows, and the prune must not read that silence as "every flow here was
// deleted". Without the scope the reconcile deletes the whole workspace's
// projection and its scheduled flows stop firing until a later pass rebuilds
// them.
func TestReconcileSchedules_UnreadableWorkspaceKeepsItsRows(t *testing.T) {
	healthy, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("open healthy: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	broken, err := workspace.OpenFS(dir)
	if err != nil {
		t.Fatalf("open broken: %v", err)
	}

	store := NewMemScheduleStore()
	svc := &Service{
		Workspaces: MapWorkspaces{"t/ws": healthy, "t/broken": broken},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        NewMemoryBus(),
		Schedules:  store,
	}
	ctx := context.Background()

	commit, err := healthy.Save(core.Graph{
		ID: "keeper", Tenant: "t", Workspace: "ws",
		Nodes:    []core.Node{{ID: "a", Module: "noop"}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "*/5 * * * *"}},
	}, "u")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := healthy.PromoteToEnvironment("keeper", workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	seed := []ScheduleSpec{
		{Tenant: "t", Workspace: "broken", GraphID: "b1",
			EntryKey: "t/broken/b1#0", SpecKey: "cron:0 * * * *", Cron: "0 * * * *"},
	}
	if err := store.ReplaceFlowSchedules(ctx, "t", "broken", "b1", seed); err != nil {
		t.Fatalf("seed broken: %v", err)
	}
	if err := store.ReplaceFlowSchedules(ctx, "t", "ws", "deleted-flow", []ScheduleSpec{
		{Tenant: "t", Workspace: "ws", GraphID: "deleted-flow",
			EntryKey: "t/ws/deleted-flow#0", SpecKey: "cron:0 * * * *", Cron: "0 * * * *"},
	}); err != nil {
		t.Fatalf("seed deleted: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := broken.ListGraphs(); err == nil {
		t.Fatal("precondition: broken workspace should fail to list")
	}

	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	all, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var brokenRows, deletedRows, keeperRows int
	for _, s := range all {
		switch {
		case s.Workspace == "broken":
			brokenRows++
		case s.GraphID == "deleted-flow":
			deletedRows++
		case s.GraphID == "keeper":
			keeperRows++
		}
	}
	if brokenRows != 1 {
		t.Errorf("unreadable workspace lost its schedules: %d rows, want 1", brokenRows)
	}
	// The prune must still do its job where the listing succeeded.
	if deletedRows != 0 {
		t.Errorf("a genuinely deleted flow kept %d schedule row(s)", deletedRows)
	}
	if keeperRows == 0 {
		t.Error("the live flow lost its schedule")
	}
}

func TestReconcileSchedules_VanishedWorkspaceKeepsItsRows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws")
	gone, err := workspace.OpenFS(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store := NewMemScheduleStore()
	svc := &Service{
		Workspaces: MapWorkspaces{"t/gone": gone},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        NewMemoryBus(),
		Schedules:  store,
	}
	ctx := context.Background()

	if err := store.ReplaceFlowSchedules(ctx, "t", "gone", "g1", []ScheduleSpec{
		{Tenant: "t", Workspace: "gone", GraphID: "g1",
			EntryKey: "t/gone/g1#0", SpecKey: "cron:0 * * * *", Cron: "0 * * * *"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	all, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("a vanished workspace lost its schedules: %d rows, want 1", len(all))
	}
}
