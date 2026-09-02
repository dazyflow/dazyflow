// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

func dupTestService(t *testing.T) (*Service, core.Principal) {
	t.Helper()
	ws, _ := workspace.OpenFS("")
	svc := &Service{
		Workspaces: MapWorkspaces{"acme/main": ws},
		Jobs:       jobstore.NewMemory(),
	}
	p := core.Principal{Subject: "alice", Tenant: "acme", Workspace: "main", Roles: []core.Role{{
		Name: "editor", Permissions: []core.Permission{core.PermGraphEdit, core.PermGraphRun},
	}}}
	return svc, p
}

// TestDuplicateGraph_CopiesAsDisabledDraft verifies the core contract: the
// copy gets a fresh ID, starts disabled, is owned by the duplicator, carries
// the source's nodes + metadata, and leaves the source untouched.
func TestDuplicateGraph_CopiesAsDisabledDraft(t *testing.T) {
	t.Parallel()
	svc, p := dupTestService(t)
	ws := svc.Workspaces.(MapWorkspaces)["acme/main"]

	src := core.Graph{
		ID: "flow1", Tenant: "acme", Workspace: "main",
		Name: "Weather", Description: "desc", Icon: "cloud", Owner: "bob",
		Nodes: []core.Node{{ID: "n1", Module: "value"}},
	}
	if _, err := ws.Save(src, "bob"); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	newID, g, commit, err := svc.DuplicateGraph(t.Context(), p, "acme", "main", "flow1", "")
	if err != nil {
		t.Fatalf("DuplicateGraph: %v", err)
	}
	if newID != "flow1-copy" {
		t.Errorf("new id = %q, want flow1-copy", newID)
	}
	if commit == "" {
		t.Error("commit hash should be non-empty")
	}
	if !g.Disabled {
		t.Error("duplicate must start disabled so copied triggers don't fire pre-review")
	}
	if g.Owner != "alice" {
		t.Errorf("owner = %q, want alice (the duplicator)", g.Owner)
	}
	if g.Name != "Copy of Weather" {
		t.Errorf("name = %q, want \"Copy of Weather\"", g.Name)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].ID != "n1" {
		t.Errorf("nodes not carried over: %+v", g.Nodes)
	}
	if g.Description != "desc" || g.Icon != "cloud" {
		t.Errorf("display metadata not carried over: desc=%q icon=%q", g.Description, g.Icon)
	}

	// The copy is persisted and loadable under its new ID.
	stored, err := ws.Load("flow1-copy")
	if err != nil {
		t.Fatalf("load copy: %v", err)
	}
	if !stored.Disabled || stored.Owner != "alice" {
		t.Errorf("stored copy: disabled=%v owner=%q", stored.Disabled, stored.Owner)
	}

	// Source is untouched: still enabled, still owned by its original author.
	orig, err := ws.Load("flow1")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	if orig.Disabled {
		t.Error("source must not be disabled by a duplicate")
	}
	if orig.Owner != "bob" {
		t.Errorf("source owner changed to %q, want bob", orig.Owner)
	}
}

// TestDuplicateGraph_UniqueIDs verifies a second copy of the same source
// doesn't collide with the first.
func TestDuplicateGraph_UniqueIDs(t *testing.T) {
	t.Parallel()
	svc, p := dupTestService(t)
	ws := svc.Workspaces.(MapWorkspaces)["acme/main"]
	if _, err := ws.Save(core.Graph{ID: "flow1", Tenant: "acme", Workspace: "main", Name: "F"}, "bob"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	first, _, _, err := svc.DuplicateGraph(t.Context(), p, "acme", "main", "flow1", "")
	if err != nil {
		t.Fatalf("first duplicate: %v", err)
	}
	second, _, _, err := svc.DuplicateGraph(t.Context(), p, "acme", "main", "flow1", "")
	if err != nil {
		t.Fatalf("second duplicate: %v", err)
	}
	if first != "flow1-copy" {
		t.Errorf("first = %q, want flow1-copy", first)
	}
	if second != "flow1-copy-2" {
		t.Errorf("second = %q, want flow1-copy-2", second)
	}
}

// TestDuplicateGraph_CustomName honors a caller-supplied name.
func TestDuplicateGraph_CustomName(t *testing.T) {
	t.Parallel()
	svc, p := dupTestService(t)
	ws := svc.Workspaces.(MapWorkspaces)["acme/main"]
	if _, err := ws.Save(core.Graph{ID: "flow1", Tenant: "acme", Workspace: "main", Name: "F"}, "bob"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, g, _, err := svc.DuplicateGraph(t.Context(), p, "acme", "main", "flow1", "My copy")
	if err != nil {
		t.Fatalf("DuplicateGraph: %v", err)
	}
	if g.Name != "My copy" {
		t.Errorf("name = %q, want \"My copy\"", g.Name)
	}
}

// TestDuplicateGraph_MissingSource returns ErrNotFound (which the handler maps
// to a 404) rather than creating an empty flow.
func TestDuplicateGraph_MissingSource(t *testing.T) {
	t.Parallel()
	svc, p := dupTestService(t)
	_, _, _, err := svc.DuplicateGraph(t.Context(), p, "acme", "main", "nope", "")
	if err == nil {
		t.Fatal("expected an error duplicating a missing source")
	}
}
