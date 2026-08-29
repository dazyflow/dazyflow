// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// TestDeleteGraph_RemovesGitCache verifies a flow's auto-assigned
// git_checkout cache (gitcache/<flow>) is removed when the flow is deleted,
// while unrelated workspace files are left untouched.
func TestDeleteGraph_RemovesGitCache(t *testing.T) {
	base := t.TempDir()
	sb, err := NewFSSandbox(base)
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	ws, _ := workspace.OpenFS("")
	svc := &Service{
		Workspaces: MapWorkspaces{"acme/main": ws},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Sandbox: sb, Resolver: &engine.NodeResolver{Native: engine.Default}},
	}
	p := core.Principal{Subject: "u", Tenant: "acme", Workspace: "main", Roles: []core.Role{{
		Name: "editor", Permissions: []core.Permission{core.PermGraphEdit, core.PermGraphRun, core.PermGraphAdmin},
	}}}

	g := core.Graph{ID: "flow1", Tenant: "acme", Workspace: "main", Nodes: []core.Node{{ID: "co", Module: "git_checkout"}}}
	if _, err := ws.Save(g, "u"); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	root, _ := sb.Root("acme", "main")
	// The flow's clone cache + an unrelated user file.
	cacheDir := filepath.Join(root, filepath.FromSlash(core.GitCacheGraphRel("flow1")), "co")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "user-data.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteGraph(t.Context(), p, "acme", "main", "flow1"); err != nil {
		t.Fatalf("DeleteGraph: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "gitcache", "flow1")); !os.IsNotExist(err) {
		t.Errorf("gitcache/flow1 should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated workspace file must survive, got: %v", err)
	}
}

// newGitCacheService builds the minimal Service + principal the gitcache
// tests need, returning the sandbox root alongside them.
func newGitCacheService(t *testing.T) (*Service, core.Principal, *workspace.Store, string) {
	t.Helper()
	sb, err := NewFSSandbox(t.TempDir())
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	ws, _ := workspace.OpenFS("")
	svc := &Service{
		Workspaces: MapWorkspaces{"acme/main": ws},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Sandbox: sb, Resolver: &engine.NodeResolver{Native: engine.Default}},
	}
	p := core.Principal{Subject: "u", Tenant: "acme", Workspace: "main", Roles: []core.Role{{
		Name: "editor", Permissions: []core.Permission{core.PermGraphEdit, core.PermGraphRun, core.PermGraphAdmin},
	}}}
	root, _ := sb.Root("acme", "main")
	return svc, p, ws, root
}

// seedCache plants a fake checkout for (flow, node) and returns its dir.
func seedCache(t *testing.T, root, flow, node string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(core.GitCheckoutRel(flow, node)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSaveGraph_PrunesOrphanedGitCache is the regression guard for the
// per-node cache leak: deleting a git_checkout step (or rebuilding it under
// a new node ID) used to strand its clone forever, since only flow deletion
// reclaimed anything. Saving the flow must now reclaim the orphan while
// leaving every live step's clone — and unrelated flows — untouched.
func TestSaveGraph_PrunesOrphanedGitCache(t *testing.T) {
	svc, p, _, root := newGitCacheService(t)

	keep := seedCache(t, root, "flow1", "co")
	orphan := seedCache(t, root, "flow1", "old-co")
	otherFlow := seedCache(t, root, "flow2", "co")

	g := core.Graph{ID: "flow1", Tenant: "acme", Workspace: "main",
		Nodes: []core.Node{{ID: "co", Module: "git_checkout"}}}
	if _, err := svc.SaveGraph(t.Context(), p, g); err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphaned node cache should be pruned, stat err = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("live node cache must survive: %v", err)
	}
	if _, err := os.Stat(otherFlow); err != nil {
		t.Errorf("another flow's cache must survive: %v", err)
	}
}

// TestSaveGraph_PrunesKeepsNonCheckoutNodes pins the conservative rule: a
// directory survives on node-ID membership alone, so a step that is no
// longer a git_checkout (or was never one) never has its folder reclaimed
// out from under it while the step still exists.
func TestSaveGraph_PrunesKeepsNonCheckoutNodes(t *testing.T) {
	svc, p, _, root := newGitCacheService(t)
	dir := seedCache(t, root, "flow1", "step")

	g := core.Graph{ID: "flow1", Tenant: "acme", Workspace: "main",
		Nodes: []core.Node{{ID: "step", Module: "shell"}}}
	if _, err := svc.SaveGraph(t.Context(), p, g); err != nil {
		t.Fatalf("SaveGraph: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("cache of a still-present node must survive: %v", err)
	}
}

// TestPruneGitCache_NoCacheDir: the common case (a flow that never ran a
// checkout) is a no-op, not an error.
func TestPruneGitCache_NoCacheDir(t *testing.T) {
	svc, _, _, _ := newGitCacheService(t)
	svc.pruneGitCache("acme", "main", core.Graph{ID: "nope"})
}
