// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestStore_AmendRevertToParent reproduces the silent data-loss bug where an
// autosave burst that nets back to the pre-autosave content left HEAD carrying
// the change the user had reverted.
//
// go-git's amend sets the new commit's parent to HEAD's *parent* and reports
// ErrEmptyCommit when the staged tree matches that parent — which is exactly
// the case "add a node, then delete it again". The old code swallowed that as a
// no-op and returned the unchanged HEAD, so the deleted node reappeared on the
// next Load and stale graphs ran. The fix drops the redundant autosave commit.
func TestStore_AmendRevertToParent(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	graph := func(nodes ...string) core.Graph {
		ns := make([]core.Node, len(nodes))
		for i, id := range nodes {
			ns[i] = core.Node{ID: id, Module: "noop"}
		}
		return core.Graph{ID: "flow1", Version: "1", Nodes: ns}
	}
	const author = "anna@acme.com"

	// Baseline checkpoint: just node "a".
	if _, err := s.Save(graph("a"), author); err != nil {
		t.Fatalf("save baseline: %v", err)
	}
	baseCommits := countCommits(t, s)

	// Autosave adds node "b" — a fresh autosave commit on top of the checkpoint.
	if _, err := s.SaveCoalescing(graph("a", "b"), author); err != nil {
		t.Fatalf("autosave add: %v", err)
	}
	if n := countCommits(t, s); n != baseCommits+1 {
		t.Fatalf("after add: commits %d, want %d", n, baseCommits+1)
	}

	// Same burst reverts the addition: delete "b" again, back to just "a".
	// This amends the autosave; the staged tree now matches its parent.
	if _, err := s.SaveCoalescing(graph("a"), author); err != nil {
		t.Fatalf("autosave revert: %v", err)
	}

	// The reverted content must be what's stored — node "b" must be gone.
	got, err := s.Load("flow1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ids := make([]string, len(got.Nodes))
	for i, n := range got.Nodes {
		ids[i] = n.ID
	}
	if len(got.Nodes) != 1 || got.Nodes[0].ID != "a" {
		t.Fatalf("after revert: nodes = %v, want exactly [a] (node b leaked back)", ids)
	}

	// The redundant autosave commit was dropped, so HEAD is back at the
	// baseline checkpoint (no empty/stale commit left behind).
	if n := countCommits(t, s); n != baseCommits {
		t.Fatalf("after revert: commits %d, want %d (empty autosave should be dropped)", n, baseCommits)
	}
}
