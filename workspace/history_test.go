package workspace

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestStore_HistoryAndRestore covers the version-history panel's backing
// data and the Google-Docs-style restore: restoring an old revision is a new
// commit on top (history grows, never rewrites) and Load reflects it.
func TestStore_HistoryAndRestore(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	mk := func(node string) core.Graph {
		return core.Graph{ID: "flow1", Version: "1", Nodes: []core.Node{{ID: node, Module: "noop"}}}
	}

	v1, err := s.Save(mk("a"), "anna@acme.com")
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	if _, err := s.Save(mk("b"), "anna@acme.com"); err != nil {
		t.Fatalf("save v2: %v", err)
	}

	revs, err := s.History("flow1", 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("history len = %d, want 2", len(revs))
	}
	if revs[0].Commit == revs[1].Commit {
		t.Fatalf("history commits not distinct")
	}

	// Restore v1's content: load at that commit and save as a new HEAD.
	old, err := s.LoadAt(v1, "flow1")
	if err != nil {
		t.Fatalf("loadAt v1: %v", err)
	}
	if _, err := s.Save(old, "anna@acme.com"); err != nil {
		t.Fatalf("restore save: %v", err)
	}

	// History grew (no rewrite) and HEAD now matches v1's content.
	revs, err = s.History("flow1", 100)
	if err != nil {
		t.Fatalf("history after restore: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("history len after restore = %d, want 3", len(revs))
	}
	head, err := s.Load("flow1")
	if err != nil {
		t.Fatalf("load head: %v", err)
	}
	if len(head.Nodes) != 1 || head.Nodes[0].ID != "a" {
		t.Fatalf("restored HEAD node = %+v, want id 'a'", head.Nodes)
	}
}
