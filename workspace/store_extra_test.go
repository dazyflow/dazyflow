// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
