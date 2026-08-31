// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/dazyflow/dazyflow/core"
)

// A mirror push force-updates every ref and deletes the ones that no longer
// exist locally. Both halves are destructive and neither is recoverable from
// the daemon's side, so the cases below are about the two ways that can go
// wrong: overwriting the WRONG repository, and computing the wrong ref set for
// the right one. The happy paths live in mirror_test.go (refspec mechanics)
// and mirror_ssh_test.go (a real key against a real server).

// --- helpers ----------------------------------------------------------

// mirrorOf pushes s to a fresh bare remote and returns the remote's path,
// so a test can then do something adversarial to either side.
func mirrorOf(t *testing.T, s *Store) string {
	t.Helper()
	remote := bareRemote(t)
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("seed mirror: %v", err)
	}
	return remote
}

// storeWithFlows builds a workspace holding the named flows, publishing the
// first, and returns it with the final commit.
func storeWithFlows(t *testing.T, ids ...string) (*Store, string) {
	t.Helper()
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	var commit string
	for _, id := range ids {
		commit, err = s.Save(core.Graph{
			ID:    id,
			Nodes: []core.Node{{ID: "a", Module: "noop"}},
		}, "anna@acme.com")
		if err != nil {
			t.Fatalf("save %q: %v", id, err)
		}
	}
	if len(ids) > 0 {
		if err := s.PromoteToEnvironment(ids[0], PublishedEnv, commit); err != nil {
			t.Fatalf("publish %q: %v", ids[0], err)
		}
	}
	return s, commit
}

// remoteFlows lists the flow ids readable from the remote's branch tip — the
// question that actually matters after a destructive push: is the data there?
func remoteFlows(t *testing.T, dir string) []string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	refs := remoteRefs(t, dir)
	var tip plumbing.Hash
	for name, hash := range refs {
		if strings.HasPrefix(name, "refs/heads/") {
			tip = plumbing.NewHash(hash)
		}
	}
	if tip.IsZero() {
		return nil
	}
	commit, err := repo.CommitObject(tip)
	if err != nil {
		t.Fatalf("remote tip commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("remote tree: %v", err)
	}
	var out []string
	for _, e := range tree.Entries {
		if e.Name != "graphs" {
			continue
		}
		sub, err := tree.Tree("graphs")
		if err != nil {
			t.Fatalf("graphs tree: %v", err)
		}
		for _, f := range sub.Entries {
			out = append(out, strings.TrimSuffix(f.Name, ".json"))
		}
	}
	return out
}

// --- overwriting the wrong repository ---------------------------------

// TestMirror_RefusesUnrelatedRemote is the data-loss case that mattered most:
// a deployment whose data volume was lost comes back with an empty workspace
// and mirrors it over the very repository it should have been restored from.
// Before the shared-history check this silently deleted every flow and the
// published tag.
func TestMirror_RefusesUnrelatedRemote(t *testing.T) {
	// A populated mirror, from a workspace that then disappears.
	original, _ := storeWithFlows(t, "invoices", "alerts", "backup")
	remote := mirrorOf(t, original)
	before := remoteRefs(t, remote)
	if len(before) < 2 {
		t.Fatalf("setup: expected a branch and a tag on the mirror, got %v", before)
	}

	// A brand-new workspace — same URL, no shared history.
	fresh, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	_, err = fresh.Push(context.Background(), remote, nil)
	if !errors.Is(err, ErrUnrelatedRemote) {
		t.Fatalf("push from an unrelated workspace = %v, want ErrUnrelatedRemote", err)
	}

	// Nothing moved. This is the assertion the whole guard exists for.
	if after := remoteRefs(t, remote); len(after) != len(before) {
		t.Errorf("refused push still changed the remote: %v -> %v", before, after)
	}
	flows := remoteFlows(t, remote)
	if len(flows) != 3 {
		t.Errorf("remote flows = %v, want all three intact", flows)
	}
}

// TestMirror_OverwriteUnrelatedIsOptIn — the same push, explicitly confirmed,
// must go through. Refusing forever would make a legitimately repointed
// mirror impossible to fix from the UI.
func TestMirror_OverwriteUnrelatedIsOptIn(t *testing.T) {
	original, _ := storeWithFlows(t, "invoices")
	remote := mirrorOf(t, original)

	fresh, freshCommit := storeWithFlows(t, "somethingelse")
	if _, err := fresh.Push(context.Background(), remote, nil); !errors.Is(err, ErrUnrelatedRemote) {
		t.Fatalf("setup: expected the guard to fire, got %v", err)
	}
	res, err := fresh.PushOverwritingUnrelated(context.Background(), remote, nil)
	if err != nil {
		t.Fatalf("confirmed overwrite: %v", err)
	}
	if res.Head != freshCommit {
		t.Errorf("head = %q, want the new workspace's commit %q", res.Head, freshCommit)
	}
	if flows := remoteFlows(t, remote); len(flows) != 1 || flows[0] != "somethingelse" {
		t.Errorf("remote flows = %v, want just the new workspace's flow", flows)
	}
}

// TestMirror_EmptyRemoteIsNotUnrelated — a fresh empty repository is the
// expected target of a first push. If the guard fired here, nobody could ever
// set a mirror up.
func TestMirror_EmptyRemoteIsNotUnrelated(t *testing.T) {
	s, commit := storeWithFlows(t, "flow1")
	remote := bareRemote(t)
	res, err := s.Push(context.Background(), remote, nil)
	if err != nil {
		t.Fatalf("first push to an empty remote: %v", err)
	}
	if res.Head != commit {
		t.Errorf("head = %q, want %q", res.Head, commit)
	}
}

// TestMirror_AmendedHistoryIsStillRelated is the false-positive the guard had
// to avoid. SaveCoalescing amends, so the remote's tip stops being an
// ancestor of local HEAD during ordinary editing — but it is still an object
// we hold, which is what the check tests. If this regressed, mirroring would
// break for every user who types two params half a minute apart.
func TestMirror_AmendedHistoryIsStillRelated(t *testing.T) {
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
	remote := mirrorOf(t, s)

	second, err := s.SaveCoalescing(mk("b"), "anna@acme.com")
	if err != nil {
		t.Fatalf("second autosave: %v", err)
	}
	if second == first {
		t.Fatal("setup: expected the coalescing save to amend")
	}
	// The remote still points at the amended-away commit.
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push after amend must be allowed, got: %v", err)
	}
}

// TestMirror_DivergedRemoteIsStillRelated — someone commits directly on the
// mirror. Its tip is unknown to us, but its other refs are ours, so this is
// clearly still the same repository and the replica contract applies: we
// overwrite. The guard must not turn a diverged replica into a stuck mirror.
func TestMirror_DivergedRemoteIsStillRelated(t *testing.T) {
	s, _ := storeWithFlows(t, "flow1")
	remote := mirrorOf(t, s)

	// Put a REAL commit on the remote that we have never seen, and move the
	// branch to it, leaving our published tag in place. A dangling hash would
	// not do: receive-pack rejects an update whose old value has no object,
	// which would fail this test for a reason that has nothing to do with the
	// guard under test.
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	head, err := repo.Reference(plumbing.ReferenceName("refs/heads/master"), true)
	if err != nil {
		t.Fatalf("remote head: %v", err)
	}
	stranger := commitOnRemote(t, repo, head.Hash())
	if err := repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), stranger)); err != nil {
		t.Fatalf("move remote branch: %v", err)
	}
	// Sanity: the branch tip is now something we do not hold, while the tag
	// still is — which is exactly the "diverged but related" shape.
	if s.repo.Storer.HasEncodedObject(stranger) == nil {
		t.Fatal("setup: the fabricated commit should be unknown locally")
	}

	if _, err := s.Save(core.Graph{ID: "flow1", Nodes: []core.Node{{ID: "b", Module: "noop"}}}, "anna@acme.com"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push to a diverged-but-related remote: %v", err)
	}
	// Our branch won, which is the documented replica behaviour.
	for name, hash := range remoteRefs(t, remote) {
		if strings.HasPrefix(name, "refs/heads/") && hash == stranger.String() {
			t.Error("the stranger commit survived; the mirror did not take over the branch")
		}
	}
}

// commitOnRemote writes a real commit object into the remote's own store,
// parented on `parent` and reusing its tree — the cheapest way to simulate
// "somebody pushed to the mirror directly" without a second worktree.
func commitOnRemote(t *testing.T, repo *git.Repository, parent plumbing.Hash) plumbing.Hash {
	t.Helper()
	parentCommit, err := repo.CommitObject(parent)
	if err != nil {
		t.Fatalf("remote parent commit: %v", err)
	}
	when := parentCommit.Author.When.Add(time.Minute)
	c := &object.Commit{
		Author:       object.Signature{Name: "Someone", Email: "someone@example.com", When: when},
		Committer:    object.Signature{Name: "Someone", Email: "someone@example.com", When: when},
		Message:      "a commit made directly on the mirror\n",
		TreeHash:     parentCommit.TreeHash,
		ParentHashes: []plumbing.Hash{parent},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}
	return h
}

// --- computing the ref set for the right repository -------------------

// TestMirror_PrunesExtraRemoteBranchesButNotOurs — the delete half of the
// push. Extra refs on the remote go; the ref being updated must not be caught
// in the same sweep (which is exactly what go-git's Prune did, and why this
// code enumerates refs itself).
func TestMirror_PrunesExtraRemoteBranchesButNotOurs(t *testing.T) {
	s, _ := storeWithFlows(t, "flow1")
	remote := mirrorOf(t, s)

	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	head, err := repo.Reference(plumbing.ReferenceName("refs/heads/master"), true)
	if err != nil {
		t.Fatalf("remote head: %v", err)
	}
	for _, name := range []string{"refs/heads/someones-branch", "refs/tags/v1.0", "refs/heads/wip/nested"} {
		if err := repo.Storer.SetReference(
			plumbing.NewHashReference(plumbing.ReferenceName(name), head.Hash()),
		); err != nil {
			t.Fatalf("seed remote ref %s: %v", name, err)
		}
	}

	commit, err := s.Save(core.Graph{ID: "flow1", Nodes: []core.Node{{ID: "b", Module: "noop"}}}, "anna@acme.com")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := s.Push(context.Background(), remote, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Deleted != 3 {
		t.Errorf("Deleted = %d, want the 3 extra refs", res.Deleted)
	}
	after := remoteRefs(t, remote)
	for _, gone := range []string{"refs/heads/someones-branch", "refs/tags/v1.0", "refs/heads/wip/nested"} {
		if _, ok := after[gone]; ok {
			t.Errorf("%s should have been pruned (refs: %v)", gone, after)
		}
	}
	// And the branch we were updating is still there, at the new commit.
	var branch string
	for name, hash := range after {
		if strings.HasPrefix(name, "refs/heads/") {
			branch = hash
		}
	}
	if branch != commit {
		t.Errorf("our branch = %q, want the new commit %q (refs: %v)", branch, commit, after)
	}
	if flows := remoteFlows(t, remote); len(flows) != 1 || flows[0] != "flow1" {
		t.Errorf("remote flows = %v, want flow1 intact", flows)
	}
}

// TestMirror_DeletedFlowPropagates — a deleted flow must disappear from the
// mirror's tip while remaining in its history. This is the restore path: the
// mirror is where a flow deleted by mistake is recovered from.
func TestMirror_DeletedFlowPropagates(t *testing.T) {
	s, _ := storeWithFlows(t, "keep", "remove")
	remote := mirrorOf(t, s)
	if flows := remoteFlows(t, remote); len(flows) != 2 {
		t.Fatalf("setup: remote flows = %v, want both", flows)
	}

	if _, err := s.Delete("remove", "anna@acme.com"); err != nil {
		t.Fatalf("delete flow: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push after delete: %v", err)
	}
	flows := remoteFlows(t, remote)
	if len(flows) != 1 || flows[0] != "keep" {
		t.Errorf("remote flows = %v, want only \"keep\"", flows)
	}
	// Still recoverable: the deletion is a commit, so the flow lives on in
	// the mirrored history rather than being erased from it.
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	iter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		t.Fatalf("remote log: %v", err)
	}
	found := false
	if err := iter.ForEach(func(c *object.Commit) error {
		if _, err := c.File(graphPath("remove")); err == nil {
			found = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk remote history: %v", err)
	}
	if !found {
		t.Error("the deleted flow is not recoverable from the mirror's history")
	}
}

// TestMirror_ManyFlowsAllMirrored guards the ref/refspec enumeration at a
// size where an off-by-one or a truncation would show up.
func TestMirror_ManyFlowsAllMirrored(t *testing.T) {
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	const n = 40
	var last string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("flow-%02d", i)
		last, err = s.Save(core.Graph{ID: id, Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna@acme.com")
		if err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		// Publish every third flow, so there are many tags to carry too.
		if i%3 == 0 {
			if err := s.PromoteToEnvironment(id, PublishedEnv, last); err != nil {
				t.Fatalf("publish %s: %v", id, err)
			}
		}
	}
	remote := bareRemote(t)
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	if flows := remoteFlows(t, remote); len(flows) != n {
		t.Errorf("mirrored %d flows, want %d", len(flows), n)
	}
	tags := 0
	for name := range remoteRefs(t, remote) {
		if strings.HasPrefix(name, "refs/tags/") {
			tags++
		}
	}
	if want := (n + 2) / 3; tags != want {
		t.Errorf("mirrored %d tags, want %d", tags, want)
	}
}

// TestMirror_AwkwardFlowIDs — ids reach the mirror as both a path
// (graphs/<id>.json) and a tag (refs/tags/graphs/<id>/<env>). Characters that
// are legal in one and awkward in the other are where a mirror silently drops
// a flow or produces an unpushable ref.
func TestMirror_AwkwardFlowIDs(t *testing.T) {
	ids := []string{
		"with-dashes",
		"with_underscores",
		"MiXedCase",
		"digits123",
		"a", // single character
		strings.Repeat("long", 20),
	}
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	for _, id := range ids {
		commit, err := s.Save(core.Graph{ID: id, Nodes: []core.Node{{ID: "a", Module: "noop"}}}, "anna@acme.com")
		if err != nil {
			t.Fatalf("save %q: %v", id, err)
		}
		if err := s.PromoteToEnvironment(id, PublishedEnv, commit); err != nil {
			t.Fatalf("publish %q: %v", id, err)
		}
	}
	remote := bareRemote(t)
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	got := map[string]bool{}
	for _, f := range remoteFlows(t, remote) {
		got[f] = true
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("flow %q missing from the mirror (got %v)", id, got)
		}
		tag := "refs/tags/graphs/" + id + "/" + PublishedEnv
		if _, ok := remoteRefs(t, remote)[tag]; !ok {
			t.Errorf("tag for %q missing from the mirror", id)
		}
	}
}

// --- failure and concurrency ------------------------------------------

// TestMirror_FailedPushLeavesBothSidesIntact. The local half matters as much
// as the remote: go-git's own prune implementation REMOVES LOCAL REFS, so a
// wrong turn here would have the mirror corrupting the workspace it is
// supposed to be backing up.
func TestMirror_FailedPushLeavesBothSidesIntact(t *testing.T) {
	s, commit := storeWithFlows(t, "flow1", "flow2")
	remote := mirrorOf(t, s)
	refsBefore := remoteRefs(t, remote)

	localBefore, err := s.mirroredRefs()
	if err != nil {
		t.Fatalf("local refs: %v", err)
	}
	// A remote that cannot be reached at all.
	if _, err := s.Push(context.Background(), "ssh://git@127.0.0.1:1/nope.git", nil); err == nil {
		t.Fatal("push to a dead address should fail")
	}
	localAfter, err := s.mirroredRefs()
	if err != nil {
		t.Fatalf("local refs after: %v", err)
	}
	if strings.Join(localBefore, ",") != strings.Join(localAfter, ",") {
		t.Errorf("local refs changed after a failed push: %v -> %v", localBefore, localAfter)
	}
	for _, id := range []string{"flow1", "flow2"} {
		if _, err := s.Load(id); err != nil {
			t.Errorf("flow %q unreadable after a failed push: %v", id, err)
		}
	}
	if head, err := s.Head(); err != nil || head != commit {
		t.Errorf("local HEAD = (%q, %v), want %q", head, err, commit)
	}
	// And the real mirror, which we never touched, is untouched.
	if after := remoteRefs(t, remote); len(after) != len(refsBefore) {
		t.Errorf("the healthy remote changed: %v -> %v", refsBefore, after)
	}
}

// TestMirror_CancelledPushIsSafe — the pusher bounds every push with a
// timeout, so cancellation is a routine event, not an exception.
func TestMirror_CancelledPushIsSafe(t *testing.T) {
	s, commit := storeWithFlows(t, "flow1")
	remote := bareRemote(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before we start
	if _, err := s.Push(ctx, remote, nil); err == nil {
		t.Fatal("push with a cancelled context should fail")
	}
	// The store is still usable and unchanged — a cancelled mirror must not
	// cost the workspace anything.
	if head, err := s.Head(); err != nil || head != commit {
		t.Errorf("local HEAD = (%q, %v), want %q", head, err, commit)
	}
	if _, err := s.Load("flow1"); err != nil {
		t.Errorf("flow unreadable after a cancelled push: %v", err)
	}
	// A later push still works.
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Errorf("push after a cancelled one: %v", err)
	}
}

// TestMirror_ConcurrentPushesSerialize — the pusher coalesces, but a manual
// "Push now" can land while an automatic push is in flight. go-git's
// repository is not concurrency-safe, so the store lock is what stands
// between that and a corrupted object store. Run with -race.
func TestMirror_ConcurrentPushesSerialize(t *testing.T) {
	s, _ := storeWithFlows(t, "flow1")
	remote := mirrorOf(t, s)

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Push(context.Background(), remote, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent push %d: %v", i, err)
		}
	}
	if flows := remoteFlows(t, remote); len(flows) != 1 || flows[0] != "flow1" {
		t.Errorf("remote flows = %v after concurrent pushes, want flow1", flows)
	}
	if _, err := s.Load("flow1"); err != nil {
		t.Errorf("local store damaged by concurrent pushes: %v", err)
	}
}

// TestMirror_PushWhileSaving — the other concurrency shape: a save (which
// writes refs) racing a push (which reads them). Both must complete and the
// store must stay readable. Run with -race.
func TestMirror_PushWhileSaving(t *testing.T) {
	s, _ := storeWithFlows(t, "flow1")
	remote := mirrorOf(t, s)

	var wg sync.WaitGroup
	wg.Add(2)
	var saveErr, pushErr error
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := s.Save(core.Graph{
				ID:    "flow1",
				Nodes: []core.Node{{ID: fmt.Sprintf("n%d", i), Module: "noop"}},
			}, "anna@acme.com"); err != nil {
				saveErr = err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := s.Push(context.Background(), remote, nil); err != nil {
				pushErr = err
				return
			}
		}
	}()
	wg.Wait()
	if saveErr != nil {
		t.Errorf("save during push: %v", saveErr)
	}
	if pushErr != nil {
		t.Errorf("push during save: %v", pushErr)
	}
	if _, err := s.Load("flow1"); err != nil {
		t.Errorf("store damaged: %v", err)
	}
	// The mirror ends up holding some valid state — not necessarily the very
	// last save (a push races the writes by design), but a readable one.
	if flows := remoteFlows(t, remote); len(flows) != 1 {
		t.Errorf("remote flows = %v, want exactly flow1", flows)
	}
}

// TestMirror_RepeatedPushesAreStable — mirroring runs unattended for months.
// Repeated pushes with no local change must stay no-ops rather than
// accumulating anything or flapping.
func TestMirror_RepeatedPushesAreStable(t *testing.T) {
	s, _ := storeWithFlows(t, "flow1")
	remote := mirrorOf(t, s)
	first := remoteRefs(t, remote)

	for i := 0; i < 5; i++ {
		res, err := s.Push(context.Background(), remote, nil)
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if res.Changed {
			t.Errorf("push %d reported a change with nothing to do", i)
		}
	}
	if after := remoteRefs(t, remote); len(after) != len(first) {
		t.Errorf("refs drifted over repeated pushes: %v -> %v", first, after)
	}
}

// TestMirror_UnpublishThenRepublish — the tag lifecycle, which is what tells
// the mirror which revision is live. Going offline and back must leave the tag
// pointing at the new revision, not the old one.
func TestMirror_UnpublishThenRepublish(t *testing.T) {
	s, first := storeWithFlows(t, "flow1")
	remote := mirrorOf(t, s)
	tag := "refs/tags/graphs/flow1/" + PublishedEnv
	if _, ok := remoteRefs(t, remote)[tag]; !ok {
		t.Fatalf("setup: expected %s on the mirror", tag)
	}

	if err := s.ClearEnvironment("flow1", PublishedEnv); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push after unpublish: %v", err)
	}
	if _, ok := remoteRefs(t, remote)[tag]; ok {
		t.Error("the published tag survived an unpublish")
	}

	second, err := s.Save(core.Graph{ID: "flow1", Nodes: []core.Node{{ID: "b", Module: "noop"}}}, "anna@acme.com")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if second == first {
		t.Fatal("setup: expected a new commit")
	}
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, second); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if _, err := s.Push(context.Background(), remote, nil); err != nil {
		t.Fatalf("push after republish: %v", err)
	}
	got, ok := remoteRefs(t, remote)[tag]
	if !ok {
		t.Fatal("the published tag did not come back")
	}
	// The tag is annotated, so its ref points at a tag object rather than the
	// commit — resolve it to check which revision is actually live.
	repo, err := git.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	target := resolveTagTarget(t, repo, plumbing.NewHash(got))
	if target != second {
		t.Errorf("mirrored published tag points at %q, want the new revision %q", target, second)
	}
}

// resolveTagTarget follows an annotated tag to the commit it names, falling
// back to the hash itself for a lightweight tag.
func resolveTagTarget(t *testing.T, repo *git.Repository, h plumbing.Hash) string {
	t.Helper()
	if tag, err := repo.TagObject(h); err == nil {
		return tag.Target.String()
	}
	return h.String()
}
