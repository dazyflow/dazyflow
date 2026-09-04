// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"errors"
	"testing"
)

// A migration that silently dropped history, labels or the published pointer
// would look like a success and take the install's live flows offline. Each of
// those is checked on the far side.
func TestMigrate_GitToPostgresPreservesEverything(t *testing.T) {
	dst, _ := pgTestWorkspace(t) // skips unless DAZYFLOW_TEST_DB is set
	src, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A flow with several revisions, a label on an older one, published at
	// that older one (a rollback that is currently live).
	v1 := mustSave(t, src, flow("shipping", "v1"), "ada@example.com")
	mustSave(t, src, flow("shipping", "v2"), "ada@example.com")
	v3 := mustSave(t, src, flow("shipping", "v3"), "grace@example.com")
	if err := src.SetRevisionLabel("shipping", v1, "Black Friday config"); err != nil {
		t.Fatal(err)
	}
	if err := src.PromoteToEnvironment("shipping", PublishedEnv, v1); err != nil {
		t.Fatal(err)
	}
	// A second flow, published at its newest revision.
	billing := mustSave(t, src, flow("billing", "current"), "ada@example.com")
	if err := src.PromoteToEnvironment("billing", PublishedEnv, billing); err != nil {
		t.Fatal(err)
	}
	// A third that is only a draft.
	mustSave(t, src, flow("draft", "wip"), "ada@example.com")

	res, err := Migrate(ctx, dst, src)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Flows != 3 {
		t.Fatalf("migrated %d flows, want 3", res.Flows)
	}
	if res.Published != 2 {
		t.Fatalf("migrated %d published pointers, want 2", res.Published)
	}
	if len(res.Truncated) != 0 {
		t.Fatalf("unexpected truncation: %v", res.Truncated)
	}

	// Every flow is there and reads as its draft did.
	ids, err := dst.ListGraphs()
	if err != nil || len(ids) != 3 {
		t.Fatalf("ListGraphs = %v / %v, want 3 flows", ids, err)
	}
	got, err := dst.Load("shipping")
	if err != nil || got.Name != "v3" {
		t.Fatalf("draft after migration = %+v / %v, want v3", got, err)
	}

	// The published pointer survived AND still names the rolled-back revision.
	pub, err := dst.PublishedCommit("shipping")
	if err != nil {
		t.Fatal(err)
	}
	if pub != v1 {
		t.Fatalf("published revision = %q, want the original id %q — ids must carry over", pub, v1)
	}
	live, err := dst.LoadPublished("shipping")
	if err != nil || live.Name != "v1" {
		t.Fatalf("published content = %+v / %v, want v1", live, err)
	}

	// History came across, oldest to newest, with authors intact.
	revs, err := dst.History("shipping", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("history = %d entries, want 3", len(revs))
	}
	if revs[0].Commit != v3 || revs[0].Author != "grace@example.com" {
		t.Fatalf("newest history entry = %+v, want %s by grace", revs[0], v3)
	}
	if revs[2].Commit != v1 {
		t.Fatalf("oldest history entry = %q, want %q", revs[2].Commit, v1)
	}

	// The label followed its revision.
	label, err := dst.RevisionLabel("shipping", v1)
	if err != nil || label != "Black Friday config" {
		t.Fatalf("label = %q / %v, want the original", label, err)
	}

	// An old revision is still loadable by id, so rollback still works.
	old, err := dst.LoadAt(v1, "shipping")
	if err != nil || old.Name != "v1" {
		t.Fatalf("LoadAt(v1) = %+v / %v", old, err)
	}

	// The draft-only flow did not gain a published pointer.
	if p, _ := dst.PublishedCommit("draft"); p != "" {
		t.Fatalf("draft flow came across published at %q", p)
	}

	// Re-running converges rather than colliding.
	if _, err := Migrate(ctx, dst, src); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	revs2, _ := dst.History("shipping", 100)
	if len(revs2) != 3 {
		t.Fatalf("history after a repeat migration = %d entries, want 3", len(revs2))
	}
}

// A flow deleted before the migration is not resurrected by it.
func TestMigrate_DeletedFlowDoesNotComeAcross(t *testing.T) {
	dst, _ := pgTestWorkspace(t)
	src, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, src, flow("gone", "x"), "u")
	mustSave(t, src, flow("kept", "y"), "u")
	if _, err := src.Delete("gone", "u"); err != nil {
		t.Fatal(err)
	}

	if _, err := Migrate(context.Background(), dst, src); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := dst.Load("gone"); !errors.Is(err, ErrGraphNotFound) {
		t.Fatalf("deleted flow came across: %v", err)
	}
	ids, _ := dst.ListGraphs()
	if len(ids) != 1 || ids[0] != "kept" {
		t.Fatalf("ListGraphs = %v, want [kept]", ids)
	}
}

// Migrating into a git workspace is refused rather than half-done.
func TestMigrate_RefusesANonPostgresDestination(t *testing.T) {
	a, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), a, b); err == nil {
		t.Fatal("migration into a git workspace should be refused")
	}
}

// The verifier is what turns "is it safe to delete the git workspaces?" into a
// checkable question, so it has to actually catch a bad migration — not just
// agree with a good one.
func TestVerifyMigration(t *testing.T) {
	dst, _ := pgTestWorkspace(t)
	src, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	v1 := mustSave(t, src, flow("shipping", "v1"), "ada")
	mustSave(t, src, flow("shipping", "v2"), "ada")
	if err := src.SetRevisionLabel("shipping", v1, "Black Friday"); err != nil {
		t.Fatal(err)
	}
	if err := src.PromoteToEnvironment("shipping", PublishedEnv, v1); err != nil {
		t.Fatal(err)
	}
	mustSave(t, src, flow("billing", "b1"), "grace")

	if _, err := Migrate(ctx, dst, src); err != nil {
		t.Fatal(err)
	}
	res, err := VerifyMigration(ctx, dst, src)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !res.OK() {
		t.Fatalf("a good migration reported issues: %+v", res.Issues)
	}
	if res.Flows != 2 || res.Revisions != 3 {
		t.Fatalf("verified %d flows / %d revisions, want 2 / 3", res.Flows, res.Revisions)
	}

	// Now break it in each of the ways that matter and confirm each is caught.
	t.Run("catches a missing flow", func(t *testing.T) {
		d2, _ := pgTestWorkspace(t)
		if _, err := Migrate(ctx, d2, src); err != nil {
			t.Fatal(err)
		}
		if _, err := d2.Delete("billing", "u"); err != nil {
			t.Fatal(err)
		}
		res, err := VerifyMigration(ctx, d2, src)
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Fatal("a flow missing from the migrated copy was not reported")
		}
	})

	t.Run("catches a moved published pointer", func(t *testing.T) {
		d2, _ := pgTestWorkspace(t)
		if _, err := Migrate(ctx, d2, src); err != nil {
			t.Fatal(err)
		}
		if err := d2.ClearEnvironment("shipping", PublishedEnv); err != nil {
			t.Fatal(err)
		}
		res, err := VerifyMigration(ctx, d2, src)
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Fatal("a lost published pointer was not reported")
		}
	})

	t.Run("catches altered content", func(t *testing.T) {
		d2, _ := pgTestWorkspace(t)
		if _, err := Migrate(ctx, d2, src); err != nil {
			t.Fatal(err)
		}
		mustSave(t, d2, flow("shipping", "tampered"), "someone")
		res, err := VerifyMigration(ctx, d2, src)
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Fatal("altered content was not reported")
		}
	})
}
