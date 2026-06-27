package workspace

import (
	"testing"
)

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

// TestStore_LoadPublishedOrHead_RawHashTag covers publishedCommit returning a
// raw hash that LoadPublishedOrHead then loads via plumbing.NewHash (the
// commit != "" branch, distinct from the HEAD fallback).
func TestStore_LoadPublishedOrHead_RawHashTag(t *testing.T) {
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
	g, err := s.LoadPublishedOrHead("g")
	if err != nil {
		t.Fatalf("LoadPublishedOrHead: %v", err)
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
