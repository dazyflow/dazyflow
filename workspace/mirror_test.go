// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// bareRemote creates an empty bare repository to push into and returns its
// path. Pushing to a local path exercises go-git's file transport, which
// shells out to git-receive-pack — so the test skips where git is absent
// rather than failing on a missing binary.
func bareRemote(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available; the file:// transport needs git-receive-pack")
	}
	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	return dir
}

// remoteRefs lists the refs the bare remote holds, so assertions read as
// "the mirror carries this" rather than poking at git internals inline.
func remoteRefs(t *testing.T, dir string) map[string]string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	iter, err := repo.References()
	if err != nil {
		t.Fatalf("remote refs: %v", err)
	}
	out := map[string]string{}
	if err := iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference {
			out[string(r.Name())] = r.Hash().String()
		}
		return nil
	}); err != nil {
		t.Fatalf("iterate remote refs: %v", err)
	}
	return out
}

// TestStore_PushMirrorsGraphsAndTags is the end-to-end contract of the
// mirror: after a push the remote holds the same commit, the flow's JSON is
// readable from it, and the published-environment tag came along (a mirror
// that carried flows but not the published tag would lose which revision is
// live).
func TestStore_PushMirrorsGraphsAndTags(t *testing.T) {
	remote := bareRemote(t)
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	commit, err := s.Save(core.Graph{ID: "flow1", Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna@acme.com")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	res, err := s.Push(context.Background(), remote, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !res.Changed {
		t.Error("first push reported Changed=false; the remote was empty, so it moved")
	}
	if res.Head != commit {
		t.Errorf("push head = %q, want the saved commit %q", res.Head, commit)
	}

	refs := remoteRefs(t, remote)
	var head string
	for name, hash := range refs {
		if strings.HasPrefix(name, "refs/heads/") {
			head = hash
		}
	}
	if head != commit {
		t.Errorf("remote branch at %q, want the local commit %q (refs: %v)", head, commit, refs)
	}
	tag := "refs/tags/graphs/flow1/" + PublishedEnv
	if _, ok := refs[tag]; !ok {
		t.Errorf("published tag %q missing from the mirror (refs: %v)", tag, refs)
	}

	// The mirrored commit must contain the flow itself, not just a ref —
	// this is what makes the mirror a restorable copy rather than a pointer.
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	obj, err := repo.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		t.Fatalf("remote commit object: %v", err)
	}
	f, err := obj.File(graphPath("flow1"))
	if err != nil {
		t.Fatalf("remote tree missing %s: %v", graphPath("flow1"), err)
	}
	contents, err := f.Contents()
	if err != nil {
		t.Fatalf("read mirrored graph: %v", err)
	}
	if !strings.Contains(contents, `"noop"`) {
		t.Errorf("mirrored graph JSON doesn't look like the saved flow: %s", contents)
	}
}

// TestStore_PushIsIdempotent covers the no-op path: a second push with
// nothing new must report success with Changed=false, not an error. go-git
// signals up-to-date with a non-nil error value, so getting this wrong turns
// every quiet mirror into a red "mirror failed" in the UI.
func TestStore_PushIsIdempotent(t *testing.T) {
	remote := bareRemote(t)
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	if _, err := s.Save(core.Graph{ID: "flow1"}, "anna@acme.com"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("first push: %v", err)
	}
	res, err := s.Push(context.Background(), remote, nil)
	if err != nil {
		t.Fatalf("second push (nothing new) should succeed, got: %v", err)
	}
	if res.Changed {
		t.Error("second push reported Changed=true; nothing had changed")
	}
}

// TestStore_PushPrunesDeletedTags is why Prune is on. Unpublishing a flow
// removes its published tag locally; without prune the mirror would keep
// advertising a revision as live after it was taken offline.
func TestStore_PushPrunesDeletedTags(t *testing.T) {
	remote := bareRemote(t)
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	commit, err := s.Save(core.Graph{ID: "flow1"}, "anna@acme.com")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push published: %v", err)
	}
	tag := "refs/tags/graphs/flow1/" + PublishedEnv
	if _, ok := remoteRefs(t, remote)[tag]; !ok {
		t.Fatalf("setup: expected %q on the mirror", tag)
	}

	// Unpublish, then re-mirror.
	if err := s.ClearEnvironment("flow1", PublishedEnv); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push after unpublish: %v", err)
	}
	if _, ok := remoteRefs(t, remote)[tag]; ok {
		t.Errorf("tag %q still on the mirror after unpublish; prune didn't take", tag)
	}
}

// TestStore_PushSurvivesAmendedHistory is the reason the mirror forces.
// SaveCoalescing amends the previous autosave inside its window, so a
// workspace's history is legitimately rewritten during ordinary editing. A
// non-forced mirror would start rejecting pushes the first time a user typed
// two params half a minute apart.
func TestStore_PushSurvivesAmendedHistory(t *testing.T) {
	remote := bareRemote(t)
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	mk := func(node string) core.Graph {
		return core.Graph{ID: "flow1", Nodes: []core.Node{{ID: node, Module: "noop"}}}
	}
	first, err := s.SaveCoalescing(mk("a"), "anna@acme.com")
	if err != nil {
		t.Fatalf("first autosave: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push after first autosave: %v", err)
	}

	// A second coalescing save inside the window amends, so HEAD's hash
	// changes without a new commit on top — exactly the shape a plain push
	// rejects as non-fast-forward.
	second, err := s.SaveCoalescing(mk("b"), "anna@acme.com")
	if err != nil {
		t.Fatalf("second autosave: %v", err)
	}
	if second == first {
		t.Fatal("setup: expected the coalescing save to amend into a new hash")
	}
	res, err := s.Push(context.Background(), remote, nil)
	if err != nil {
		t.Fatalf("push after amend must force through, got: %v", err)
	}
	if res.Head != second {
		t.Errorf("push head = %q, want the amended commit %q", res.Head, second)
	}
	var branch string
	for name, hash := range remoteRefs(t, remote) {
		if strings.HasPrefix(name, "refs/heads/") {
			branch = hash
		}
	}
	if branch != second {
		t.Errorf("mirror branch at %q, want the amended commit %q", branch, second)
	}
}

// TestStore_PushRejectsEmptyURL guards the argument check — a misconfigured
// mirror must fail fast with a clear message rather than reaching go-git.
func TestStore_PushRejectsEmptyURL(t *testing.T) {
	s, err := OpenFS("")
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	if _, err := s.Push(context.Background(), "", nil); err == nil {
		t.Error("push with no remote URL should fail")
	}
}
