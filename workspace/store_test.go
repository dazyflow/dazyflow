// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestStore_SaveAndLoad(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}

	graph := core.Graph{
		ID:      "ci-pipeline",
		Version: "1",
		Nodes:   []core.Node{{ID: "a", Module: "noop"}},
	}
	commit, err := s.Save(graph, "anna@acme.com")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if commit == "" {
		t.Error("expected non-empty commit hash")
	}

	loaded, err := s.Load("ci-pipeline")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != graph.ID || len(loaded.Nodes) != 1 {
		t.Errorf("loaded = %+v", loaded)
	}
}

func TestStore_PromoteToEnvironment(t *testing.T) {
	s, _ := OpenFS("")
	graph := core.Graph{ID: "ci-pipeline", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	commit, err := s.Save(graph, "anna@acme.com")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := s.PromoteToEnvironment("ci-pipeline", "production", commit); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	tags, err := s.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	found := false
	for _, tag := range tags {
		if tag == "graphs/ci-pipeline/production" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected production tag, got %v", tags)
	}
}

func TestStore_ListGraphs(t *testing.T) {
	s, _ := OpenFS("")
	for _, id := range []string{"ci", "email", "etl"} {
		if _, err := s.Save(core.Graph{ID: id, Nodes: []core.Node{{ID: "x", Module: "noop"}}}, "anna"); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	ids, err := s.ListGraphs()
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("got %v, want 3", ids)
	}
}

func TestStore_HistoryAcrossSaves(t *testing.T) {
	s, _ := OpenFS("")
	g := core.Graph{ID: "g", Nodes: []core.Node{{ID: "v1", Module: "noop"}}}
	first, _ := s.Save(g, "anna")
	g.Nodes = []core.Node{{ID: "v2", Module: "noop"}}
	second, _ := s.Save(g, "anna")

	if first == second {
		t.Fatal("two saves should produce different commits")
	}

	older, err := s.LoadAt(first, "g")
	if err != nil {
		t.Fatalf("LoadAt first: %v", err)
	}
	if older.Nodes[0].ID != "v1" {
		t.Errorf("LoadAt first returned %+v", older)
	}
	newer, _ := s.LoadAt(second, "g")
	if newer.Nodes[0].ID != "v2" {
		t.Errorf("LoadAt second returned %+v", newer)
	}
}

// TestStore_DropAmendedHead covers the amend-produces-empty-commit recovery:
// an autosave is amended back to its pre-autosave content, so go-git reports an
// empty commit and save() calls dropAmendedHead to rewind the branch to the
// autosave's parent (discarding the now-redundant autosave).
func TestStore_DropAmendedHead(t *testing.T) {
	s, _ := OpenFS("")
	base := sampleGraph("flow", "a") // explicit checkpoint content

	// Explicit checkpoint P.
	pHash, err := s.Save(base, "anna")
	if err != nil {
		t.Fatalf("explicit save: %v", err)
	}

	// Autosave that changes content (adds node "b") ⇒ new autosave commit A.
	changed := sampleGraph("flow", "a", "b")
	if _, err := s.SaveCoalescing(changed, "anna"); err != nil {
		t.Fatalf("autosave change: %v", err)
	}
	if h, _ := s.Head(); h == pHash {
		t.Fatal("autosave should have created a new commit above P")
	}

	// Coalescing save back to the original content ⇒ amend A, empty commit,
	// dropAmendedHead rewinds to P.
	got, err := s.SaveCoalescing(base, "anna")
	if err != nil {
		t.Fatalf("revert autosave: %v", err)
	}
	if got != pHash {
		t.Errorf("dropAmendedHead returned %q, want parent %q", got, pHash)
	}
	if h, _ := s.Head(); h != pHash {
		t.Errorf("HEAD = %q after revert, want P %q", h, pHash)
	}
	// The reverted content is what loads back.
	g, err := s.Load("flow")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(g.Nodes) != 1 {
		t.Errorf("loaded %d nodes, want 1 (revert dropped node b)", len(g.Nodes))
	}
}

// TestStore_OpenFS_ReopenExistingRepo covers openOrInit's git.Open success
// branch (an already-initialized on-disk repo is reopened, not re-init'd).
func TestStore_OpenFS_ReopenExistingRepo(t *testing.T) {
	dir := t.TempDir()
	s1, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("first OpenFS: %v", err)
	}
	if _, err := s1.Save(sampleGraph("g"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Reopen: openOrInit's git.Open returns nil error, repo is reused.
	s2, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("reopen OpenFS: %v", err)
	}
	ids, err := s2.ListGraphs()
	if err != nil {
		t.Fatalf("ListGraphs after reopen: %v", err)
	}
	if len(ids) != 1 || ids[0] != "g" {
		t.Errorf("reopened graphs = %v, want [g]", ids)
	}
}

// TestStore_LoadPublished_RawHashTag covers publishedCommit returning a
// raw hash that LoadPublished then loads via plumbing.NewHash (the
// commit != "" branch, distinct from the HEAD fallback).
func TestStore_LoadPublished_RawHashTag(t *testing.T) {
	s, _ := OpenFS("")
	c1, _ := s.Save(sampleGraph("g", "a"), "anna")

	if err := s.PromoteToEnvironment("g", PublishedEnv, c1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	pub, err := s.PublishedCommit("g")
	if err != nil {
		t.Fatalf("PublishedCommit: %v", err)
	}
	if pub != c1 {
		t.Errorf("PublishedCommit = %q, want %q", pub, c1)
	}
	g, err := s.LoadPublished("g")
	if err != nil {
		t.Fatalf("LoadPublished: %v", err)
	}
	if g.ID != "g" {
		t.Errorf("loaded %q, want g", g.ID)
	}
}

// TestStore_ClearEnvironment_RemovesPublishedTag covers ClearEnvironment's
// successful RemoveReference branch (env tag present, then removed).
func TestStore_ClearEnvironment_RemovesPublishedTag(t *testing.T) {
	s, _ := OpenFS("")
	c1, _ := s.Save(sampleGraph("g", "a"), "anna")
	if err := s.PromoteToEnvironment("g", PublishedEnv, c1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub, _ := s.PublishedCommit("g"); pub != c1 {
		t.Fatalf("pre-clear published = %q, want %q", pub, c1)
	}
	if err := s.ClearEnvironment("g", PublishedEnv); err != nil {
		t.Fatalf("ClearEnvironment: %v", err)
	}
	if pub, _ := s.PublishedCommit("g"); pub != "" {
		t.Errorf("post-clear published = %q, want empty", pub)
	}
}

// TestStore_PromoteToEnvironment_BadCommit covers PromoteToEnvironment's
// resolve-error branch (unresolvable commit ref).
func TestStore_PromoteToEnvironment_BadCommit(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(sampleGraph("g"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.PromoteToEnvironment("g", "staging", "not-a-ref"); err == nil {
		t.Error("PromoteToEnvironment with bad commit: want error")
	}
}

// TestStore_History_DefaultLimit covers the limit<=0 default branch in History.
func TestStore_History_DefaultLimit(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(sampleGraph("g"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	revs, err := s.History("g", 0) // 0 ⇒ default limit
	if err != nil {
		t.Fatalf("History(0): %v", err)
	}
	if len(revs) == 0 {
		t.Error("expected at least one revision with default limit")
	}
}

// TestStore_SaveCoalescing_FirstHasNoRecentAutosave covers headIsRecentAutosave
// returning false when HEAD is an explicit (non-autosave) commit: the first
// coalescing save after an explicit checkpoint must start a fresh commit.
func TestStore_SaveCoalescing_FirstHasNoRecentAutosave(t *testing.T) {
	s, _ := OpenFS("")
	explicit, _ := s.Save(sampleGraph("g", "a"), "anna")
	got, err := s.SaveCoalescing(sampleGraph("g", "a", "b"), "anna")
	if err != nil {
		t.Fatalf("coalescing after explicit: %v", err)
	}
	if got == explicit {
		t.Error("first coalescing save after an explicit commit should not amend it")
	}
}

// TestStore_RevisionLabel_UnlabeledCommit covers revisionLabel's "no tag"
// return path (reference lookup fails ⇒ "").
func TestStore_RevisionLabel_UnlabeledCommit(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(sampleGraph("g"), "anna")
	if got, err := s.RevisionLabel("g", commit); err != nil || got != "" {
		t.Errorf("RevisionLabel unlabeled = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestStore_LoadAt_BadRef covers LoadAt's resolve-error branch.
func TestStore_LoadAt_BadRef(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(sampleGraph("g"), "anna"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.LoadAt("not-a-ref", "g"); err == nil {
		t.Error("LoadAt with unresolvable ref: want error")
	}
}

// TestStore_Tags_ListsLabelAndEnvTags covers listRefs over refs/tags with
// multiple tag kinds present.
func TestStore_Tags_ListsLabelAndEnvTags(t *testing.T) {
	s, _ := OpenFS("")
	c1, _ := s.Save(sampleGraph("g"), "anna")
	if err := s.PromoteToEnvironment("g", "staging", c1); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := s.SetRevisionLabel("g", c1, "v1"); err != nil {
		t.Fatalf("label: %v", err)
	}
	tags, err := s.Tags()
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	if len(tags) < 2 {
		t.Errorf("tags = %v, want at least the env + label tag", tags)
	}
}

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
// Environment guards + LoadPublished falls back to HEAD.
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

func TestStore_LoadPublished_FallsBackToHead(t *testing.T) {
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
	// No HEAD fallback: an unpublished flow refuses to load for firing.
	if _, err := s.LoadPublished("g"); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("LoadPublished on unpublished flow = %v, want ErrNotPublished", err)
	}
}

func TestStore_LoadPublished_UsesPublishedTag(t *testing.T) {
	s, _ := OpenFS("")
	c1, _ := s.Save(sampleGraph("g", "a"), "anna")
	// Advance HEAD with a second revision, then publish the first.
	if _, err := s.Save(sampleGraph("g", "a", "b"), "anna"); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if err := s.PromoteToEnvironment("g", PublishedEnv, c1); err != nil {
		t.Fatalf("publish: %v", err)
	}
	g, err := s.LoadPublished("g")
	if err != nil {
		t.Fatalf("LoadPublished: %v", err)
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

var (
	osWriteFile = os.WriteFile
	osRemoveAll = os.RemoveAll
)

func commitWithSig(wt *git.Worktree, msg string) (string, error) {
	h, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@local", When: time.Now()},
	})
	if err != nil {
		return "", err
	}
	return h.String(), nil
}

// TestStore_SaveIdempotentReturnsHEAD covers the ErrEmptyCommit branch:
// saving identical content twice must return HEAD (not an error). This
// is the "MCP-then-apply" path that motivated the branch.
func TestStore_SaveIdempotentReturnsHEAD(t *testing.T) {
	s, _ := OpenFS("")
	g := core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	h1, err := s.Save(g, "anna")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	h2, err := s.Save(g, "anna")
	if err != nil {
		t.Fatalf("idempotent re-Save: %v", err)
	}
	if h2 != h1 {
		t.Errorf("idempotent re-Save returned %q, want HEAD %q", h2, h1)
	}
}

// TestStore_LoadAt_MissingGraphAtKnownCommit pins the "graph %q at %s"
// branch of loadAt: a valid commit but no file by that ID.
func TestStore_LoadAt_MissingGraphAtKnownCommit(t *testing.T) {
	s, _ := OpenFS("")
	commit, _ := s.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna")
	_, err := s.LoadAt(commit, "different-id")
	if err == nil || !strings.Contains(err.Error(), "different-id") {
		t.Errorf("err = %v, want one mentioning 'different-id'", err)
	}
}

// TestStore_ListGraphs_EmptyRepo covers the "no HEAD yet" early-return.
// A freshly-init repo with no commits returns nil, nil.
func TestStore_ListGraphs_EmptyRepo(t *testing.T) {
	// We can't call OpenFS("") without it seeding an initial commit
	// (HEAD then exists). But the "no HEAD" branch is exercised
	// post-init too: at this point ListGraphs walks HEAD's tree and
	// finds only .gitkeep, returning an empty slice. That's the
	// nil-graphs branch.
	s, _ := OpenFS("")
	ids, err := s.ListGraphs()
	if err != nil {
		t.Fatalf("ListGraphs on init-only repo: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ListGraphs on init-only = %v, want empty", ids)
	}
}

// TestStore_LoadAt_ResolvesByBranch covers the resolve() ResolveRevision
// branch (a real ref name, not a raw 40-char hash).
func TestStore_LoadAt_ResolvesByBranch(t *testing.T) {
	s, _ := OpenFS("")
	if _, err := s.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	branches, err := s.Branches()
	if err != nil || len(branches) == 0 {
		t.Fatalf("Branches: %v len=%d", err, len(branches))
	}
	// LoadAt by branch name resolves through ResolveRevision.
	g, err := s.LoadAt(branches[0], "g")
	if err != nil {
		t.Fatalf("LoadAt(branch): %v", err)
	}
	if g.ID != "g" {
		t.Errorf("loaded %q", g.ID)
	}
}

// TestStore_OpenFS_NestedDirAutoCreates covers the openDisk MkdirAll
// branch when the parent directory doesn't exist yet.
func TestStore_OpenFS_NestedDirAutoCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deeply", "nested", "workspace")
	s, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("OpenFS(nested): %v", err)
	}
	if _, err := s.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna"); err != nil {
		t.Errorf("Save into newly-created dir: %v", err)
	}
}

// TestStore_LoadAt_FortyCharHashThatDoesntExist covers loadAt's
// CommitObject error branch. resolve() treats a 40-char string as a
// raw hash; CommitObject then errors because no such commit exists.
func TestStore_LoadAt_FortyCharHashThatDoesntExist(t *testing.T) {
	s, _ := OpenFS("")
	// Valid 40-char hex string, but no commit with this hash.
	bogus := "0000000000000000000000000000000000000001"
	_, err := s.LoadAt(bogus, "g")
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Errorf("err = %v, want one mentioning 'commit'", err)
	}
}

// TestStore_LoadAt_InvalidJSONInGraphFile pins loadAt's
// json.Unmarshal error branch. Bypasses Save by writing junk directly
// through the underlying filesystem, then triggers a load.
func TestStore_LoadAt_InvalidJSONInGraphFile(t *testing.T) {
	s, _ := OpenFS("")
	// Use the internal worktree to write a bogus graph file + commit.
	wt, err := s.repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := s.fs.MkdirAll("graphs", 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := s.fs.Create("graphs/broken.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("{ not valid json")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := wt.Add("graphs/broken.json"); err != nil {
		t.Fatal(err)
	}
	commitHash, err := commitWithSig(wt, "seed broken")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	_, err = s.LoadAt(commitHash, "broken")
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v, want one mentioning 'parse'", err)
	}
}

// TestStore_OpenFS_MkdirFailsOnInvalidParent covers the openDisk
// MkdirAll error branch by pointing at a path under a regular file
// (not a directory) — MkdirAll then fails.
func TestStore_OpenFS_MkdirFailsOnInvalidParent(t *testing.T) {
	root := t.TempDir()
	// Create a regular file at `barrier` so that <root>/barrier/<x>
	// can't be created — MkdirAll up the chain hits an ENOTDIR.
	barrier := filepath.Join(root, "barrier")
	if err := osWriteFile(barrier, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := OpenFS(filepath.Join(barrier, "child"))
	if err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("OpenFS through file = %v, want mkdir error", err)
	}
}

// TestStore_Save_AfterRemovingGitDir simulates a corrupted repo: the
// .git dir is wiped from under us, so wt.Add / wt.Commit fails. Pins
// the Save commit-error path.
func TestStore_Save_AfterRemovingGitDir(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenFS(dir)
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	// Wipe the .git dir to corrupt the underlying repo.
	if err := osRemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("rm .git: %v", err)
	}
	_, err = s.Save(core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna")
	if err == nil {
		t.Error("Save after .git wipe: want error")
	}
}

// TestStore_PromoteToEnvironment_MovesTag exercises the force-update
// behavior of PromoteToEnvironment — promoting the same env twice to
// different commits must succeed (env tags are intentionally movable).
func TestStore_PromoteToEnvironment_MovesTag(t *testing.T) {
	s, _ := OpenFS("")
	g := core.Graph{ID: "g", Nodes: []core.Node{{ID: "a", Module: "noop"}}}
	c1, _ := s.Save(g, "anna")

	// Save a different revision so we have a second commit to move to.
	g.Nodes = append(g.Nodes, core.Node{ID: "b", Module: "noop"})
	c2, _ := s.Save(g, "anna")
	if c1 == c2 {
		t.Fatalf("expected distinct commits, got both %q", c1)
	}

	if err := s.PromoteToEnvironment("g", "staging", c1); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	// Re-promote to c2 — force-update path.
	if err := s.PromoteToEnvironment("g", "staging", c2); err != nil {
		t.Fatalf("re-promote: %v", err)
	}

	// LoadAt the env tag (resolves the tag through ResolveRevision).
	g2, err := s.LoadAt("refs/tags/graphs/g/staging", "g")
	if err != nil {
		t.Fatalf("LoadAt env tag: %v", err)
	}
	if len(g2.Nodes) != 2 {
		t.Errorf("after re-promote, env points at %d-node graph, want 2", len(g2.Nodes))
	}
}

// The store is the choke point every writer reaches the repository through —
// the API, dzctl, MCP, the flow generator, git sync — so it is where a flow ID
// that would escape graphs/ (or make an unpublishable git ref) is refused. The
// escaping ID used to save a flow that could then never be loaded again.
func TestStore_Save_RefusesUnusableID(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	for _, id := range []string{"a/../../escape", "..", "with space", strings.Repeat("n", 300), ""} {
		g := core.Graph{ID: id, Nodes: []core.Node{{ID: "a", Module: "noop"}}}
		if _, err := s.Save(g, "qa"); err == nil {
			t.Errorf("Save(id=%q) = nil, want a rejected id", id)
		}
	}
	if _, err := s.Save(core.Graph{ID: "fine-flow", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "qa"); err != nil {
		t.Errorf("a usable id was refused: %v", err)
	}
}
