// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

// Every semantic the daemon depends on, run against BOTH backends from one
// suite. The Postgres backend exists to replace the git one underneath callers
// that cannot tell them apart — so "the same tests pass" is the actual
// requirement, and asserting it once here is worth more than two parallel sets
// of tests that drift.

func flow(id, name string) core.Graph {
	return core.Graph{
		ID: id, Version: "1", Name: name,
		Nodes: []core.Node{{ID: "a", Module: "noop", Params: map[string]any{"label": name}}},
	}
}

func mustSave(t *testing.T, s *Store, g core.Graph, author string) string {
	t.Helper()
	rev, err := s.Save(g, author)
	if err != nil {
		t.Fatalf("save %s: %v", g.ID, err)
	}
	return rev
}

func runWorkspaceConformance(t *testing.T, mk func(t *testing.T) *Store) {
	t.Helper()

	t.Run("SaveLoadRoundTrip", func(t *testing.T) {
		s := mk(t)
		mustSave(t, s, flow("f1", "First"), "ada@example.com")
		got, err := s.Load("f1")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if got.Name != "First" || len(got.Nodes) != 1 || got.Nodes[0].ID != "a" {
			t.Fatalf("round-trip lost content: %+v", got)
		}
	})

	t.Run("MissingFlowIsNotFound", func(t *testing.T) {
		s := mk(t)
		if _, err := s.Load("nope"); !errors.Is(err, ErrGraphNotFound) {
			t.Fatalf("load missing = %v, want ErrGraphNotFound", err)
		}
	})

	t.Run("ListGraphs", func(t *testing.T) {
		s := mk(t)
		mustSave(t, s, flow("b", "B"), "u")
		mustSave(t, s, flow("a", "A"), "u")
		ids, err := s.ListGraphs()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(ids) != 2 {
			t.Fatalf("ListGraphs = %v, want 2", ids)
		}
	})

	t.Run("HistoryNewestFirst", func(t *testing.T) {
		s := mk(t)
		mustSave(t, s, flow("f1", "one"), "ada")
		mustSave(t, s, flow("f1", "two"), "ada")
		mustSave(t, s, flow("f1", "three"), "grace")
		revs, err := s.History("f1", 10)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(revs) != 3 {
			t.Fatalf("history = %d entries, want 3", len(revs))
		}
		if revs[0].Author != "grace" {
			t.Fatalf("newest entry author = %q, want grace", revs[0].Author)
		}
		if revs[0].Autosave {
			t.Error("an explicit save is not an autosave")
		}
	})

	t.Run("ResavingIdenticalContentAddsNoRevision", func(t *testing.T) {
		s := mk(t)
		g := flow("f1", "same")
		first := mustSave(t, s, g, "u")
		again := mustSave(t, s, g, "u")
		if again != first {
			t.Errorf("identical re-save produced a new revision %q (was %q)", again, first)
		}
		revs, _ := s.History("f1", 10)
		if len(revs) != 1 {
			t.Fatalf("history = %d entries after an identical re-save, want 1", len(revs))
		}
	})

	t.Run("AutosaveBurstCoalesces", func(t *testing.T) {
		s := mk(t)
		if _, err := s.SaveCoalescing(flow("f1", "v1"), "ada"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveCoalescing(flow("f1", "v2"), "ada"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveCoalescing(flow("f1", "v3"), "ada"); err != nil {
			t.Fatal(err)
		}
		revs, err := s.History("f1", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(revs) != 1 {
			t.Fatalf("autosave burst left %d history entries, want 1", len(revs))
		}
		if !revs[0].Autosave {
			t.Error("coalesced entry should be marked autosave")
		}
		got, _ := s.Load("f1")
		if got.Name != "v3" {
			t.Fatalf("after the burst the flow reads %q, want v3", got.Name)
		}
	})

	t.Run("AutosaveDoesNotCoalesceAcrossAuthors", func(t *testing.T) {
		s := mk(t)
		if _, err := s.SaveCoalescing(flow("f1", "v1"), "ada"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveCoalescing(flow("f1", "v2"), "grace"); err != nil {
			t.Fatal(err)
		}
		revs, _ := s.History("f1", 10)
		if len(revs) != 2 {
			t.Fatalf("two authors left %d history entries, want 2", len(revs))
		}
	})

	// The trap this guards: an editing burst that nets back to where it
	// started. Keeping the autosave would silently restore the change the user
	// undid, and the step would reappear on the next load.
	t.Run("AutosaveRevertedWithinTheBurstIsDiscarded", func(t *testing.T) {
		s := mk(t)
		base := mustSave(t, s, flow("f1", "base"), "ada")
		if _, err := s.SaveCoalescing(flow("f1", "edited"), "ada"); err != nil {
			t.Fatal(err)
		}
		back, err := s.SaveCoalescing(flow("f1", "base"), "ada")
		if err != nil {
			t.Fatal(err)
		}
		if back != base {
			t.Errorf("revert landed on %q, want the pre-autosave revision %q", back, base)
		}
		got, _ := s.Load("f1")
		if got.Name != "base" {
			t.Fatalf("flow reads %q after the revert, want base", got.Name)
		}
		revs, _ := s.History("f1", 10)
		if len(revs) != 1 {
			t.Fatalf("history = %d entries after a reverted burst, want 1", len(revs))
		}
	})

	t.Run("PublishPinsContentAgainstFurtherEdits", func(t *testing.T) {
		s := mk(t)
		rev := mustSave(t, s, flow("f1", "published-version"), "u")
		if err := s.PromoteToEnvironment("f1", PublishedEnv, rev); err != nil {
			t.Fatalf("publish: %v", err)
		}
		pub, err := s.PublishedCommit("f1")
		if err != nil || pub == "" {
			t.Fatalf("PublishedCommit = %q / %v", pub, err)
		}
		mustSave(t, s, flow("f1", "draft-version"), "u")

		got, err := s.LoadPublished("f1")
		if err != nil {
			t.Fatalf("load published: %v", err)
		}
		if got.Name != "published-version" {
			t.Fatalf("published reads %q — a draft edit leaked into the live version", got.Name)
		}
		draft, _ := s.Load("f1")
		if draft.Name != "draft-version" {
			t.Fatalf("draft reads %q, want draft-version", draft.Name)
		}
	})

	t.Run("UnpublishedFlowLoadsAsNotPublished", func(t *testing.T) {
		s := mk(t)
		mustSave(t, s, flow("f1", "x"), "u")
		if _, err := s.LoadPublished("f1"); !errors.Is(err, ErrNotPublished) {
			t.Fatalf("LoadPublished on a draft = %v, want ErrNotPublished", err)
		}
		if pub, _ := s.PublishedCommit("f1"); pub != "" {
			t.Fatalf("PublishedCommit = %q, want empty", pub)
		}
	})

	t.Run("ClearEnvironmentTakesTheFlowOffline", func(t *testing.T) {
		s := mk(t)
		rev := mustSave(t, s, flow("f1", "x"), "u")
		if err := s.PromoteToEnvironment("f1", PublishedEnv, rev); err != nil {
			t.Fatal(err)
		}
		if err := s.ClearEnvironment("f1", PublishedEnv); err != nil {
			t.Fatalf("unpublish: %v", err)
		}
		if pub, _ := s.PublishedCommit("f1"); pub != "" {
			t.Fatalf("PublishedCommit after unpublish = %q, want empty", pub)
		}
		if _, err := s.LoadPublished("f1"); !errors.Is(err, ErrNotPublished) {
			t.Fatalf("LoadPublished after unpublish = %v, want ErrNotPublished", err)
		}
		// Idempotent.
		if err := s.ClearEnvironment("f1", PublishedEnv); err != nil {
			t.Fatalf("second unpublish: %v", err)
		}
	})

	t.Run("RollbackRepublishesAnOlderRevision", func(t *testing.T) {
		s := mk(t)
		old := mustSave(t, s, flow("f1", "v1"), "u")
		newer := mustSave(t, s, flow("f1", "v2"), "u")
		if err := s.PromoteToEnvironment("f1", PublishedEnv, newer); err != nil {
			t.Fatal(err)
		}
		if err := s.PromoteToEnvironment("f1", PublishedEnv, old); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		got, err := s.LoadPublished("f1")
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "v1" {
			t.Fatalf("after rollback published reads %q, want v1", got.Name)
		}
	})

	t.Run("LoadAtNamesAnExactRevision", func(t *testing.T) {
		s := mk(t)
		first := mustSave(t, s, flow("f1", "v1"), "u")
		mustSave(t, s, flow("f1", "v2"), "u")
		got, err := s.LoadAt(first, "f1")
		if err != nil {
			t.Fatalf("LoadAt: %v", err)
		}
		if got.Name != "v1" {
			t.Fatalf("LoadAt(first) reads %q, want v1", got.Name)
		}
	})

	t.Run("LabelsAreKeyedToTheRevision", func(t *testing.T) {
		s := mk(t)
		first := mustSave(t, s, flow("f1", "v1"), "u")
		second := mustSave(t, s, flow("f1", "v2"), "u")
		if err := s.SetRevisionLabel("f1", first, "Black Friday config"); err != nil {
			t.Fatalf("label: %v", err)
		}
		got, err := s.RevisionLabel("f1", first)
		if err != nil || got != "Black Friday config" {
			t.Fatalf("RevisionLabel = %q / %v", got, err)
		}
		if other, _ := s.RevisionLabel("f1", second); other != "" {
			t.Fatalf("label leaked onto another revision: %q", other)
		}
		// It shows up in history, so the panel can name each entry.
		revs, _ := s.History("f1", 10)
		var labelled int
		for _, r := range revs {
			if r.Label == "Black Friday config" {
				labelled++
			}
		}
		if labelled != 1 {
			t.Fatalf("history shows the label on %d entries, want 1", labelled)
		}
		// Empty clears.
		if err := s.SetRevisionLabel("f1", first, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.RevisionLabel("f1", first); got != "" {
			t.Fatalf("label after clearing = %q", got)
		}
	})

	t.Run("DeleteRemovesTheFlowButKeepsItRecoverable", func(t *testing.T) {
		s := mk(t)
		mustSave(t, s, flow("f1", "x"), "u")
		mustSave(t, s, flow("f2", "y"), "u")
		if _, err := s.Delete("f1", "u"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.Load("f1"); !errors.Is(err, ErrGraphNotFound) {
			t.Fatalf("load after delete = %v, want ErrGraphNotFound", err)
		}
		ids, _ := s.ListGraphs()
		if len(ids) != 1 || ids[0] != "f2" {
			t.Fatalf("ListGraphs after delete = %v, want [f2]", ids)
		}
		// Deleting again is the same outcome as never having existed.
		rev, err := s.Delete("f1", "u")
		if err != nil || rev != "" {
			t.Fatalf("second delete = %q / %v, want empty/nil", rev, err)
		}
		if rev, err := s.Delete("never-existed", "u"); err != nil || rev != "" {
			t.Fatalf("delete of a missing flow = %q / %v, want empty/nil", rev, err)
		}
	})

	t.Run("DeleteThenRecreate", func(t *testing.T) {
		s := mk(t)
		mustSave(t, s, flow("f1", "first life"), "u")
		if _, err := s.Delete("f1", "u"); err != nil {
			t.Fatal(err)
		}
		mustSave(t, s, flow("f1", "second life"), "u")
		got, err := s.Load("f1")
		if err != nil {
			t.Fatalf("load after recreate: %v", err)
		}
		if got.Name != "second life" {
			t.Fatalf("recreated flow reads %q", got.Name)
		}
		ids, _ := s.ListGraphs()
		if len(ids) != 1 {
			t.Fatalf("ListGraphs = %v, want the recreated flow", ids)
		}
	})

	t.Run("HeadChangesOnWriteAndIsStableOtherwise", func(t *testing.T) {
		s := mk(t)
		empty, err := s.Head()
		if err != nil {
			t.Fatalf("head: %v", err)
		}
		mustSave(t, s, flow("f1", "x"), "u")
		afterFirst, _ := s.Head()
		if afterFirst == empty {
			t.Fatal("Head did not change after a save")
		}
		again, _ := s.Head()
		if again != afterFirst {
			t.Fatalf("Head changed without a write: %q then %q", afterFirst, again)
		}
		mustSave(t, s, flow("f2", "y"), "u")
		afterSecond, _ := s.Head()
		if afterSecond == afterFirst {
			t.Fatal("Head did not change after a second flow was saved")
		}
	})

	t.Run("ResolveFor", func(t *testing.T) {
		s := mk(t)
		rev := mustSave(t, s, flow("f1", "x"), "u")
		got, err := s.ResolveFor("f1", "HEAD")
		if err != nil {
			t.Fatalf("ResolveFor HEAD: %v", err)
		}
		if got != rev {
			t.Fatalf("ResolveFor(HEAD) = %q, want the saved revision %q", got, rev)
		}
		if got, err := s.ResolveFor("f1", rev); err != nil || got != rev {
			t.Fatalf("ResolveFor(rev) = %q / %v", got, err)
		}
	})

	t.Run("RejectsAnInvalidGraphID", func(t *testing.T) {
		s := mk(t)
		if _, err := s.Save(flow("../escape", "bad"), "u"); err == nil {
			t.Fatal("saved a flow whose id escapes its namespace")
		}
	})
}

func TestConformance_GitBackend(t *testing.T) {
	t.Parallel()
	runWorkspaceConformance(t, func(t *testing.T) *Store {
		t.Helper()
		s, err := OpenFS(t.TempDir())
		if err != nil {
			t.Fatalf("OpenFS: %v", err)
		}
		return s
	})
}

func TestConformance_PostgresBackend(t *testing.T) {
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run the Postgres workspace conformance suite")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := EnsurePgWorkspaceSchema(ctx, pool); err != nil {
		t.Fatalf("schema: %v", err)
	}
	var n int
	runWorkspaceConformance(t, func(t *testing.T) *Store {
		t.Helper()
		// A tenant per subtest, so they cannot see each other's flows.
		n++
		tenant := fmt.Sprintf("conf-%d-%d", time.Now().UnixNano(), n)
		s, err := OpenPostgres(pool, tenant, "main")
		if err != nil {
			t.Fatalf("OpenPostgres: %v", err)
		}
		t.Cleanup(func() {
			for _, tbl := range []string{"flow_revisions", "flow_heads", "flow_envs"} {
				_, _ = pool.Exec(context.Background(), "DELETE FROM "+tbl+" WHERE tenant=$1", tenant)
			}
		})
		return s
	})
}
