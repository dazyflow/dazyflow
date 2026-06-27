package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func sampleGraph(id string, nodes ...string) core.Graph {
	g := core.Graph{ID: id}
	for _, n := range nodes {
		g.Nodes = append(g.Nodes, core.Node{ID: n, Module: "noop"})
	}
	if len(g.Nodes) == 0 {
		g.Nodes = []core.Node{{ID: "a", Module: "noop"}}
	}
	return g
}

// ----------------------------------------------------------------------
// Delete: happy path, already-gone idempotency, missing-ID guard.
// ----------------------------------------------------------------------

func TestStore_Delete_RemovesGraph(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(sampleGraph("doomed"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	ids, _ := s.ListGraphs()
	if len(ids) != 1 {
		t.Fatalf("pre-delete graphs = %v, want 1", ids)
	}

	hash, err := s.Delete("doomed", "anna")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if hash == "" {
		t.Error("delete returned empty commit for an existing graph")
	}
	ids, _ = s.ListGraphs()
	if len(ids) != 0 {
		t.Errorf("post-delete graphs = %v, want empty", ids)
	}
	// Loading the deleted graph now fails.
	if _, err := s.Load("doomed"); err == nil {
		t.Error("Load after Delete: want error")
	}
}

func TestStore_Delete_AlreadyGoneIsNoop(t *testing.T) {
	s, _ := OpenFS("")
	hash, err := s.Delete("never-existed", "anna")
	if err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if hash != "" {
		t.Errorf("delete of missing graph returned %q, want empty", hash)
	}
}

func TestStore_Delete_RequiresID(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Delete("", "anna"); err == nil {
		t.Error("Delete with empty ID: want error")
	}
}

// TestStore_Delete_CommitErrorOnCorruptRepo covers Delete's commit-error path:
// the graph file exists in the worktree but the .git store is wiped, so the
// removal commit fails.
func TestStore_Delete_CommitErrorOnCorruptRepo(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	if _, err := s.Save(sampleGraph("g"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("rm .git: %v", err)
	}
	if _, err := s.Delete("g", "anna"); err == nil {
		t.Error("Delete after .git wipe: want error")
	}
}

// ----------------------------------------------------------------------
// Head: empty-ish (init-only) and post-save.
// ----------------------------------------------------------------------

func TestStore_Head_TracksLatestCommit(t *testing.T) {
	// In-memory store has no seed commit, so HEAD is empty until the first
	// save — this covers Head's ErrReferenceNotFound early return.
	s, _ := OpenFS("")
	h0, err := s.Head()
	if err != nil {
		t.Fatalf("Head init: %v", err)
	}
	if h0 != "" {
		t.Errorf("Head before any commit = %q, want empty", h0)
	}
	commit, _ := s.Save(sampleGraph("g"), "anna")
	h1, err := s.Head()
	if err != nil {
		t.Fatalf("Head post-save: %v", err)
	}
	if h1 != commit {
		t.Errorf("Head = %q, want save commit %q", h1, commit)
	}
}

func TestStore_Head_DiskSeedCommit(t *testing.T) {
	// Disk store seeds an "init" commit on creation, so HEAD is non-empty
	// immediately — covers Head's success return.
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS(dir): %v", err)
	}
	h, err := s.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if h == "" {
		t.Error("disk store Head after init should be the seed commit, got empty")
	}
}

// ----------------------------------------------------------------------
// Resolve: HEAD, branch, raw hash, and the unresolvable case.
// ----------------------------------------------------------------------

func TestStore_Resolve_Variants(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(sampleGraph("g"), "anna")

	got, err := s.Resolve("HEAD")
	if err != nil {
		t.Fatalf("Resolve(HEAD): %v", err)
	}
	if got != commit {
		t.Errorf("Resolve(HEAD) = %q, want %q", got, commit)
	}

	// Raw 40-char hash resolves to itself.
	got, err = s.Resolve(commit)
	if err != nil {
		t.Fatalf("Resolve(hash): %v", err)
	}
	if got != commit {
		t.Errorf("Resolve(hash) = %q, want %q", got, commit)
	}

	// Garbage ref: not a revision, not 40 chars ⇒ error.
	if _, err := s.Resolve("not-a-ref"); err == nil {
		t.Error("Resolve(garbage): want error")
	}
}

// ----------------------------------------------------------------------
// save: explicit vs autosave commit messages distinguish history entries.
// ----------------------------------------------------------------------

func TestStore_SaveCoalescing_AutosaveMessage(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.SaveCoalescing(sampleGraph("g"), "anna"); err != nil {
		t.Fatalf("autosave: %v", err)
	}
	revs, err := s.History("g", 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(revs) == 0 || !revs[0].Autosave {
		t.Fatalf("newest revision = %+v, want autosave=true", revs)
	}
	if !strings.HasPrefix(revs[0].Message, "autosave:") {
		t.Errorf("autosave message = %q", revs[0].Message)
	}
}

// ----------------------------------------------------------------------
// History: limit capping and missing-ID guard.
// ----------------------------------------------------------------------

func TestStore_History_RespectsLimit(t *testing.T) {
	s, _ := OpenFS("")
	g := sampleGraph("g")
	for i := 0; i < 4; i++ {
		g.Nodes = append(g.Nodes, core.Node{ID: string(rune('b' + i)), Module: "noop"})
		if _, err := s.Save(g, "anna"); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	revs, err := s.History("g", 2)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(revs) != 2 {
		t.Errorf("history(limit=2) returned %d revisions, want 2", len(revs))
	}
}

func TestStore_History_RequiresID(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.History("", 10); err == nil {
		t.Error("History with empty ID: want error")
	}
}

// ----------------------------------------------------------------------
// RevisionLabel / SetRevisionLabel: requires-ID and unresolvable-commit
// guards, plus a relabel-then-clear round trip.
// ----------------------------------------------------------------------

func TestStore_SetRevisionLabel_Guards(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(sampleGraph("g"), "anna")

	if err := s.SetRevisionLabel("", commit, "x"); err == nil {
		t.Error("SetRevisionLabel empty ID: want error")
	}
	if err := s.SetRevisionLabel("g", "not-a-ref", "x"); err == nil {
		t.Error("SetRevisionLabel bad commit: want error")
	}
	if _, err := s.RevisionLabel("", commit); err == nil {
		t.Error("RevisionLabel empty ID: want error")
	}
	if _, err := s.RevisionLabel("g", "not-a-ref"); err == nil {
		t.Error("RevisionLabel bad commit: want error")
	}
}

func TestStore_RevisionLabel_RelabelThenClear(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(sampleGraph("g"), "anna")

	if err := s.SetRevisionLabel("g", commit, "first"); err != nil {
		t.Fatalf("label: %v", err)
	}
	if got, _ := s.RevisionLabel("g", commit); got != "first" {
		t.Errorf("label = %q, want first", got)
	}
	// Relabel replaces (exercises the remove-then-create path).
	if err := s.SetRevisionLabel("g", commit, "second"); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	if got, _ := s.RevisionLabel("g", commit); got != "second" {
		t.Errorf("relabel = %q, want second", got)
	}
	// Empty label clears.
	if err := s.SetRevisionLabel("g", commit, "   "); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := s.RevisionLabel("g", commit); got != "" {
		t.Errorf("after clear label = %q, want empty", got)
	}
}

// ----------------------------------------------------------------------
// Environment guards + LoadPublishedOrHead falls back to HEAD.
// ----------------------------------------------------------------------

func TestStore_Environment_Guards(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(sampleGraph("g"), "anna")

	if err := s.PromoteToEnvironment("g", "", commit); err == nil {
		t.Error("PromoteToEnvironment empty env: want error")
	}
	if err := s.ClearEnvironment("g", ""); err == nil {
		t.Error("ClearEnvironment empty env: want error")
	}
	// Clearing an env that was never set is a no-op, not an error.
	if err := s.ClearEnvironment("g", "staging"); err != nil {
		t.Errorf("ClearEnvironment unset: %v", err)
	}
}

func TestStore_LoadPublishedOrHead_FallsBackToHead(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(sampleGraph("g", "a", "b"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Never published ⇒ PublishedCommit is "" and load falls back to HEAD.
	pub, err := s.PublishedCommit("g")
	if err != nil {
		t.Fatalf("PublishedCommit: %v", err)
	}
	if pub != "" {
		t.Errorf("PublishedCommit = %q, want empty for unpublished flow", pub)
	}
	g, err := s.LoadPublishedOrHead("g")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead: %v", err)
	}
	if g.ID != "g" {
		t.Errorf("loaded %q, want g", g.ID)
	}
}

func TestStore_LoadPublishedOrHead_UsesPublishedTag(t *testing.T) {
	s, _ := OpenFS("")
	c1, _ := s.Save(sampleGraph("g", "a"), "anna")
	// Advance HEAD with a second revision, then publish the first.
	if _, err := s.Save(sampleGraph("g", "a", "b"), "anna"); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if err := s.PromoteToEnvironment("g", PublishedEnv, c1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	g, err := s.LoadPublishedOrHead("g")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Errorf("published load returned %d-node graph, want 1 (the published revision)", len(g.Nodes))
	}
}

// ----------------------------------------------------------------------
// save: missing-ID guard.
// ----------------------------------------------------------------------

func TestStore_Save_RequiresID(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(core.Graph{}, "anna"); err == nil {
		t.Error("Save with empty graph ID: want error")
	}
}
