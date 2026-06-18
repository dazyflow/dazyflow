package workspace

import (
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"git.sr.ht/~klahr/dazyflow/core"
)

func countCommits(t *testing.T, s *Store) int {
	t.Helper()
	if _, err := s.repo.Head(); err != nil {
		return 0 // no commits yet (fresh in-memory repo has no HEAD)
	}
	iter, err := s.repo.Log(&git.LogOptions{})
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	n := 0
	if err := iter.ForEach(func(_ *object.Commit) error { n++; return nil }); err != nil {
		t.Fatalf("iter: %v", err)
	}
	return n
}

// TestStore_AutosaveCoalesces verifies that consecutive autosaves of the same
// graph by the same author amend into a single commit, while explicit saves
// always start a fresh one — so editor autosave doesn't flood the history.
func TestStore_AutosaveCoalesces(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	g := func(node string) core.Graph {
		return core.Graph{ID: "flow1", Version: "1", Nodes: []core.Node{{ID: node, Module: "noop"}}}
	}

	base := countCommits(t, s)

	if _, err := s.Save(g("a"), "anna@acme.com"); err != nil { // explicit checkpoint
		t.Fatalf("save: %v", err)
	}
	afterExplicit := countCommits(t, s)
	if afterExplicit != base+1 {
		t.Fatalf("explicit save: commits %d, want %d", afterExplicit, base+1)
	}

	// First autosave after an explicit commit starts its own commit.
	if _, err := s.SaveCoalescing(g("b"), "anna@acme.com"); err != nil {
		t.Fatalf("autosave 1: %v", err)
	}
	if n := countCommits(t, s); n != afterExplicit+1 {
		t.Fatalf("autosave 1: commits %d, want %d", n, afterExplicit+1)
	}

	// Subsequent autosaves of the same flow+author amend — no new commit.
	for range 3 {
		if _, err := s.SaveCoalescing(g("c"), "anna@acme.com"); err != nil {
			t.Fatalf("autosave coalesce: %v", err)
		}
	}
	if n := countCommits(t, s); n != afterExplicit+1 {
		t.Fatalf("coalesced autosaves: commits %d, want %d (should amend)", n, afterExplicit+1)
	}

	// Latest content is preserved through the amends.
	got, err := s.Load("flow1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].ID != "c" {
		t.Fatalf("loaded node = %+v, want node id 'c'", got.Nodes)
	}

	// A different author does not coalesce onto anna's autosave.
	beforeBob := countCommits(t, s)
	if _, err := s.SaveCoalescing(g("d"), "bob@acme.com"); err != nil {
		t.Fatalf("autosave bob: %v", err)
	}
	if n := countCommits(t, s); n != beforeBob+1 {
		t.Fatalf("different author: commits %d, want %d (no coalesce)", n, beforeBob+1)
	}

	// An explicit save never coalesces — always its own checkpoint.
	beforeExplicit2 := countCommits(t, s)
	if _, err := s.Save(g("e"), "bob@acme.com"); err != nil {
		t.Fatalf("explicit 2: %v", err)
	}
	if n := countCommits(t, s); n != beforeExplicit2+1 {
		t.Fatalf("explicit after autosave: commits %d, want %d", n, beforeExplicit2+1)
	}
}
