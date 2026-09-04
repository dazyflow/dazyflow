// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/jackc/pgx/v5/pgxpool"
)

// synthesize builds the derived repository for s into dir and returns a
// fingerprint of every ref, which is what a remote would receive.
func synthesize(t *testing.T, s *Store, dir string) map[string]string {
	t.Helper()
	pg, ok := s.b.(*pgBackend)
	if !ok {
		t.Fatal("not a Postgres-backed store")
	}
	m := &pgMirror{pg: pg, dir: dir}
	g, err := m.sync(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if g == nil {
		return map[string]string{}
	}
	refs := map[string]string{}
	iter, err := g.repo.References()
	if err != nil {
		t.Fatal(err)
	}
	if err := iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference {
			refs[r.Name().String()] = r.Hash().String()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return refs
}

func refNames(refs map[string]string) []string {
	out := make([]string, 0, len(refs))
	for k := range refs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// seedHistory writes a workspace with the shapes that make a mirror
// interesting: several flows, an edit, a rollback-style publish, a label, and a
// deletion.
func seedHistory(t *testing.T, s *Store) {
	t.Helper()
	v1 := mustSave(t, s, flow("shipping", "v1"), "ada@example.com")
	mustSave(t, s, flow("billing", "b1"), "grace@example.com")
	mustSave(t, s, flow("shipping", "v2"), "ada@example.com")
	if err := s.SetRevisionLabel("shipping", v1, "Black Friday config"); err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteToEnvironment("shipping", PublishedEnv, v1); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, flow("temp", "scratch"), "ada@example.com")
	if _, err := s.Delete("temp", "ada@example.com"); err != nil {
		t.Fatal(err)
	}
}

// The property the whole design rests on: whichever replica rebuilds the
// mirror derives the SAME commits. If two replicas disagreed, every failover
// would turn the next push into a force-push and the customer's mirror would
// lose its history.
func TestSynth_IsDeterministicAcrossRebuilds(t *testing.T) {
	s, _ := pgTestWorkspace(t)
	seedHistory(t, s)

	a := synthesize(t, s, t.TempDir())
	b := synthesize(t, s, t.TempDir())

	if len(a) == 0 {
		t.Fatal("synthesis produced no refs")
	}
	if len(a) != len(b) {
		t.Fatalf("ref sets differ in size: %v vs %v", refNames(a), refNames(b))
	}
	for name, hash := range a {
		if b[name] != hash {
			t.Errorf("ref %s: replica A derived %s, replica B derived %s", name, hash, b[name])
		}
	}
}

// The failover case, stated exactly: a repository built INCREMENTALLY must be
// byte-identical to one rebuilt from scratch. A pod that has been mirroring for
// months and a pod that just took over have to agree.
func TestSynth_IncrementalMatchesAColdRebuild(t *testing.T) {
	s, _ := pgTestWorkspace(t)
	seedHistory(t, s)

	incremental := t.TempDir()
	synthesize(t, s, incremental) // first pass

	// More history arrives, as it would between two pushes.
	mustSave(t, s, flow("shipping", "v3"), "ada@example.com")
	v4 := mustSave(t, s, flow("shipping", "v4"), "grace@example.com")
	if err := s.PromoteToEnvironment("shipping", PublishedEnv, v4); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, flow("newflow", "hello"), "ada@example.com")

	got := synthesize(t, s, incremental)  // resumes
	want := synthesize(t, s, t.TempDir()) // cold

	for name, hash := range want {
		if got[name] != hash {
			t.Errorf("ref %s: incremental has %s, cold rebuild has %s", name, got[name], hash)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("incremental refs %v, cold rebuild refs %v", refNames(got), refNames(want))
	}
}

// A pointer that moves must move in the mirror, and one that is cleared must
// disappear — otherwise the mirror keeps advertising a flow as live.
func TestSynth_EnvironmentPointersFollowTheLog(t *testing.T) {
	s, _ := pgTestWorkspace(t)
	v1 := mustSave(t, s, flow("f1", "v1"), "u")
	v2 := mustSave(t, s, flow("f1", "v2"), "u")
	if err := s.PromoteToEnvironment("f1", PublishedEnv, v1); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	first := synthesize(t, s, dir)
	tag := "refs/tags/" + envTag("f1", PublishedEnv)
	if _, ok := first[tag]; !ok {
		t.Fatalf("published tag missing: %v", refNames(first))
	}
	atV1 := first[tag]

	// Roll forward.
	if err := s.PromoteToEnvironment("f1", PublishedEnv, v2); err != nil {
		t.Fatal(err)
	}
	second := synthesize(t, s, dir)
	if second[tag] == atV1 {
		t.Fatal("published tag did not move after re-publishing")
	}

	// Unpublish.
	if err := s.ClearEnvironment("f1", PublishedEnv); err != nil {
		t.Fatal(err)
	}
	third := synthesize(t, s, dir)
	if _, ok := third[tag]; ok {
		t.Fatalf("published tag survived unpublishing: %v", refNames(third))
	}
}

func TestSynth_LabelsBecomeTags(t *testing.T) {
	s, _ := pgTestWorkspace(t)
	v1 := mustSave(t, s, flow("f1", "v1"), "u")
	if err := s.SetRevisionLabel("f1", v1, "Black Friday config"); err != nil {
		t.Fatal(err)
	}
	refs := synthesize(t, s, t.TempDir())
	var found bool
	for name := range refs {
		if len(name) > len("refs/tags/graphs/f1/labels/") &&
			name[:len("refs/tags/graphs/f1/labels/")] == "refs/tags/graphs/f1/labels/" {
			found = true
		}
	}
	if !found {
		t.Fatalf("label tag missing from the mirror: %v", refNames(refs))
	}
}

// A cache that no longer matches the log rebuilds rather than pushing a
// history that never happened.
func TestSynth_StaleCacheRebuilds(t *testing.T) {
	s, _ := pgTestWorkspace(t)
	seedHistory(t, s)
	dir := t.TempDir()
	before := synthesize(t, s, dir)

	// Corrupt the cache's sync point: a repository whose HEAD names no
	// revision the log knows.
	g, err := openSynthRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, err := g.repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := g.repo.Storer.SetReference(
		plumbing.NewHashReference(head.Name(), plumbing.NewHash("0000000000000000000000000000000000000001"))); err != nil {
		t.Fatal(err)
	}

	after := synthesize(t, s, dir)
	for name, hash := range before {
		if after[name] != hash {
			t.Errorf("after rebuilding from a stale cache, ref %s = %s, want %s", name, after[name], hash)
		}
	}
}

// The content committed must depend only on the flow, not on which backend
// happened to write it — otherwise a migrated install's mirror would churn.
func TestSynth_ContentIsCanonical(t *testing.T) {
	s, _ := pgTestWorkspace(t)
	mustSave(t, s, flow("f1", "x"), "u")
	dir := t.TempDir()
	synthesize(t, s, dir)

	g, err := openSynthRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := g.repo.Head()
	c, err := g.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.File(graphPath("f1"))
	if err != nil {
		t.Fatalf("committed file missing: %v", err)
	}
	body, err := f.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if body == "" || body[0] != '{' {
		t.Fatalf("committed content is not the flow's JSON: %q", body)
	}
	// Indented, because a mirror is read by people.
	if !contains2(body, "\n  \"id\"") {
		t.Fatalf("committed JSON is not indented:\n%s", body)
	}
}

func contains2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// End to end: a Postgres workspace pushes a real repository to a real remote,
// and the remote holds the flows, the published pointer and the label.
func TestSynth_PushesToARealRemote(t *testing.T) {
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB")
	}
	remote := bareRemote(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := EnsurePgWorkspaceSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	tenant := fmt.Sprintf("synth-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, tbl := range []string{"flow_revisions", "flow_heads", "flow_envs"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+tbl+" WHERE tenant=$1", tenant)
		}
	})
	s, err := OpenPostgres(pool, tenant, "main", WithMirrorCache(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	seedHistory(t, s)

	res, err := s.Push(context.Background(), "file://"+remote, nil)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Head == "" {
		t.Fatal("push reported no head")
	}

	// Clone it back: the mirror must be a usable repository, not just refs.
	clone := filepath.Join(t.TempDir(), "clone")
	if _, err := git.PlainClone(clone, false, &git.CloneOptions{URL: "file://" + remote}); err != nil {
		t.Fatalf("clone the mirror: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "graphs", "shipping.json")); err != nil {
		t.Fatalf("mirrored clone has no shipping flow: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clone, "graphs", "temp.json")); err == nil {
		t.Fatal("a flow deleted before the push is present in the mirror")
	}
	cloned, err := git.PlainOpen(clone)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloned.Reference(plumbing.NewTagReferenceName(envTag("shipping", PublishedEnv)), true); err != nil {
		t.Fatalf("mirror lost the published pointer: %v", err)
	}

	// A second push after more history must be a plain fast-forward. If
	// synthesis were non-deterministic this is where it would surface, as a
	// rejected non-fast-forward.
	mustSave(t, s, flow("shipping", "v3"), "ada@example.com")
	res2, err := s.Push(context.Background(), "file://"+remote, nil)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if res2.Head == res.Head {
		t.Fatal("second push did not advance the remote")
	}
}

// Failover, exactly as it happens: pod A has been mirroring and holds a warm
// cache; the mirror lock moves to pod B, which has none and rebuilds from the
// log. B's push must be a plain fast-forward onto what A left. This is the
// scenario determinism exists for — if it fails, every failover force-pushes
// over the customer's history.
func TestSynth_FailoverToAColdPodFastForwards(t *testing.T) {
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB")
	}
	remote := bareRemote(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := EnsurePgWorkspaceSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	tenant := fmt.Sprintf("failover-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, tbl := range []string{"flow_revisions", "flow_heads", "flow_envs"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+tbl+" WHERE tenant=$1", tenant)
		}
	})

	openPod := func(cache string) *Store {
		s, err := OpenPostgres(pool, tenant, "main", WithMirrorCache(cache))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	podA := openPod(t.TempDir())
	seedHistory(t, podA)
	if _, err := podA.Push(context.Background(), "file://"+remote, nil); err != nil {
		t.Fatalf("pod A push: %v", err)
	}

	// Work continues while the lock is held by A.
	mustSave(t, podA, flow("shipping", "v3"), "ada@example.com")
	if _, err := podA.Push(context.Background(), "file://"+remote, nil); err != nil {
		t.Fatalf("pod A second push: %v", err)
	}

	// The lock moves. Pod B has never mirrored this workspace.
	podB := openPod(t.TempDir())
	v4 := mustSave(t, podB, flow("shipping", "v4"), "grace@example.com")
	if err := podB.PromoteToEnvironment("shipping", PublishedEnv, v4); err != nil {
		t.Fatal(err)
	}
	// Push, NOT PushOverwritingUnrelated: the shared-history check is on, so a
	// divergent rebuild would be refused rather than silently overwriting.
	if _, err := podB.Push(context.Background(), "file://"+remote, nil); err != nil {
		t.Fatalf("cold pod could not fast-forward the mirror — synthesis is not deterministic: %v", err)
	}

	// And the remote holds B's work on top of A's, with history intact.
	clone := filepath.Join(t.TempDir(), "clone")
	if _, err := git.PlainClone(clone, false, &git.CloneOptions{URL: "file://" + remote}); err != nil {
		t.Fatalf("clone after failover: %v", err)
	}
	repo, err := git.PlainOpen(clone)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	defer iter.Close()
	n := 0
	if err := iter.ForEach(func(*object.Commit) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	// seedHistory writes 5 revisions; v3 and v4 add two more.
	if n != 7 {
		t.Fatalf("mirror holds %d commits after failover, want 7 — history was rewritten", n)
	}
}
