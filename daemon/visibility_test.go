// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

type visibilityHarness struct {
	svc        *daemon.Service
	alice      core.Principal
	bob        core.Principal
	mallory    core.Principal
	editorRole core.Role
}

func newVisibilityHarness(t *testing.T) *visibilityHarness {
	t.Helper()
	wsStore, _ := workspace.OpenFS("")
	editor := core.Role{
		Name: "editor",
		Permissions: []core.Permission{
			core.PermGraphRun, core.PermGraphEdit,
		},
	}
	admin := core.Role{
		Name: "admin",
		Permissions: []core.Permission{
			core.PermGraphRun, core.PermGraphEdit, core.PermOrganizationAdmin,
		},
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
	}
	return &visibilityHarness{
		svc:        svc,
		alice:      core.Principal{Subject: "alice", Tenant: "t", Workspace: "ws", Roles: []core.Role{editor}},
		bob:        core.Principal{Subject: "bob", Tenant: "t", Workspace: "ws", Roles: []core.Role{editor}},
		mallory:    core.Principal{Subject: "mallory", Tenant: "t", Workspace: "ws", Roles: []core.Role{admin}},
		editorRole: editor,
	}
}

func TestVisibility_PrivateFlowHiddenFromOtherUsers(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "alice-secret", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("alice save: %v", err)
	}

	// Bob tries to load it — must come back as ErrNotFound, not a
	// permission error, so existence doesn't leak.
	_, err := h.svc.LoadGraph(ctx, h.bob, "t", "ws", "alice-secret", "")
	if err == nil {
		t.Fatal("bob loaded alice's private flow")
	}
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound (visibility should look like missing)", err)
	}

	ids, _ := h.svc.ListGraphs(ctx, h.bob, "t", "ws")
	for _, id := range ids {
		if id == "alice-secret" {
			t.Error("bob's list leaked alice-secret")
		}
	}

	g, err := h.svc.LoadGraph(ctx, h.alice, "t", "ws", "alice-secret", "")
	if err != nil {
		t.Fatalf("alice load own: %v", err)
	}
	if g.Owner != "alice" {
		t.Errorf("owner = %q, want alice", g.Owner)
	}
	if g.Visibility != core.VisibilityPrivate {
		t.Errorf("visibility = %q, want private", g.Visibility)
	}
}

func TestVisibility_TenantAdminBypassesPrivate(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "private-1", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := h.svc.LoadGraph(ctx, h.mallory, "t", "ws", "private-1", ""); err != nil {
		t.Errorf("mallory should see private flows: %v", err)
	}
	ids, _ := h.svc.ListGraphs(ctx, h.mallory, "t", "ws")
	found := false
	for _, id := range ids {
		if id == "private-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("mallory's list missing private-1: %v", ids)
	}
}

func TestVisibility_OrgFlowVisibleToAll(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "shared", Tenant: "t", Workspace: "ws",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := h.svc.LoadGraph(ctx, h.bob, "t", "ws", "shared", ""); err != nil {
		t.Errorf("bob should see org flow: %v", err)
	}
}

func TestVisibility_OwnerStampedOnCreate(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "no-owner-set", Tenant: "t", Workspace: "ws",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	g, _ := h.svc.LoadGraph(ctx, h.alice, "t", "ws", "no-owner-set", "")
	if g.Owner != "alice" {
		t.Errorf("owner = %q, want alice (stamped on create)", g.Owner)
	}
}

func TestVisibility_NonOwnerCantEditPrivate(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "alice-flow", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := h.svc.SaveGraph(ctx, h.bob, core.Graph{
		ID: "alice-flow", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityPrivate,
	})
	if err == nil {
		t.Fatal("bob successfully overwrote alice's private flow")
	}
}

func TestVisibility_NonOwnerCantEditOrg(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "shared", Tenant: "t", Workspace: "ws",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := h.svc.LoadGraph(ctx, h.bob, "t", "ws", "shared", ""); err != nil {
		t.Fatalf("bob load org: %v", err)
	}
	// But Bob can NOT save over it — sharing doesn't imply shared write.
	_, err := h.svc.SaveGraph(ctx, h.bob, core.Graph{
		ID: "shared", Tenant: "t", Workspace: "ws",
	})
	if err == nil {
		t.Error("bob overwrote alice's org flow despite not owning it")
	}
}

func TestVisibility_AdminCanEditOthersFlows(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "private-rec", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityPrivate,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := h.svc.SaveGraph(ctx, h.mallory, core.Graph{
		ID: "private-rec", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
		Owner:      "alice", // preserved by the save path
	}); err != nil {
		t.Fatalf("mallory should edit any flow: %v", err)
	}
	g, _ := h.svc.LoadGraph(ctx, h.alice, "t", "ws", "private-rec", "")
	if g.EffectiveVisibility() != core.VisibilityOrg {
		t.Errorf("after admin edit: visibility = %q, want org", g.Visibility)
	}
	if g.Owner != "alice" {
		t.Errorf("owner should remain alice after admin edit, got %q", g.Owner)
	}
}

func TestVisibility_NonAdminCantTransferOwner(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "shared", Tenant: "t", Workspace: "ws",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "shared", Tenant: "t", Workspace: "ws",
		Owner: "bob", Version: "v2",
	})
	if err != nil {
		t.Fatalf("alice editing own flow: %v", err)
	}
	g, _ := h.svc.LoadGraph(ctx, h.alice, "t", "ws", "shared", "")
	if g.Owner != "alice" {
		t.Errorf("owner = %q, want alice (non-admin can't transfer)", g.Owner)
	}
}
