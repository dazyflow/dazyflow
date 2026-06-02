package workspace

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// TestStore_DiskBackedPersistsAcrossReopen exercises the on-disk path
// (openDisk + openOrInit init/seed, then a reopen via git.Open) and
// confirms a saved graph survives reopening the store at the same dir.
func TestStore_DiskBackedPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	s1, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("OpenFS(dir) first: %v", err)
	}
	if _, err := s1.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reopen the same directory — must hit the git.Open (already-init) path
	// and see the prior commit.
	s2, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("OpenFS(dir) reopen: %v", err)
	}
	g, err := s2.Load("g")
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	if g.ID != "g" {
		t.Errorf("loaded %q, want g", g.ID)
	}
}

func TestStore_BranchesAndTags(t *testing.T) {
	s, _ := OpenFS("")
	commit, err := s.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	branches, err := s.Branches()
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	if len(branches) == 0 {
		t.Error("expected at least one branch after a commit")
	}
	if err := s.PromoteToEnvironment("g", "production", commit); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	tags, err := s.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	var found bool
	for _, tg := range tags {
		if strings.Contains(tg, "production") {
			found = true
		}
	}
	if !found {
		t.Errorf("env tag not in Tags(): %v", tags)
	}
}

func TestStore_LoadAtByHashAndBadRef(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna")

	// Load at the explicit commit hash (resolve's raw-hash path).
	if g, err := s.LoadAt(commit, "g"); err != nil || g.ID != "g" {
		t.Errorf("LoadAt(hash) = (%+v, %v)", g, err)
	}
	// An unresolvable ref errors rather than panicking.
	if _, err := s.LoadAt("no-such-ref", "g"); err == nil {
		t.Error("LoadAt(bad ref): want error")
	}
}

func TestStore_ErrorPaths(t *testing.T) {
	s, _ := OpenFS("")

	// Save with no ID is rejected before touching the repo.
	if _, err := s.Save(core.Graph{}, "anna"); err == nil {
		t.Error("Save(empty ID): want error")
	}
	// Load a graph that was never saved.
	if _, err := s.Load("missing"); err == nil {
		t.Error("Load(missing): want error")
	}
	// Promote with empty env, and with an unresolvable commit.
	if err := s.PromoteToEnvironment("g", "", "deadbeef"); err == nil {
		t.Error("Promote(empty env): want error")
	}
	if err := s.PromoteToEnvironment("g", "prod", "no-such-commit"); err == nil {
		t.Error("Promote(bad commit): want error")
	}
}
