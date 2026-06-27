// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
