// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pgTestWorkspace returns two INDEPENDENT Stores over the same (tenant,
// workspace) — the two-pod case. They share nothing in process: no mutex, no
// cache, no working tree.
func pgTestWorkspace(t *testing.T) (*Store, *Store) {
	t.Helper()
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run the Postgres workspace tests")
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
	tenant := fmt.Sprintf("pg-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		for _, tbl := range []string{"flow_revisions", "flow_heads", "flow_envs"} {
			_, _ = pool.Exec(context.Background(), "DELETE FROM "+tbl+" WHERE tenant=$1", tenant)
		}
	})
	a, err := OpenPostgres(pool, tenant, "main")
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenPostgres(pool, tenant, "main")
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// The point of the whole backend: a flow saved on one dzd is readable on
// another, with no shared disk and nothing to reconcile.
func TestPgBackend_WriteOnOneInstanceReadsOnTheOther(t *testing.T) {
	podA, podB := pgTestWorkspace(t)

	rev := mustSave(t, podA, flow("f1", "written on A"), "ada")
	got, err := podB.Load("f1")
	if err != nil {
		t.Fatalf("pod B could not read pod A's flow: %v", err)
	}
	if got.Name != "written on A" {
		t.Fatalf("pod B reads %q", got.Name)
	}

	// And a publish on one is live on the other, which is what makes the
	// scheduler on any pod fire what an author published on any other.
	if err := podA.PromoteToEnvironment("f1", PublishedEnv, rev); err != nil {
		t.Fatal(err)
	}
	if pub, _ := podB.PublishedCommit("f1"); pub != rev {
		t.Fatalf("pod B sees published=%q, want %q", pub, rev)
	}
	if _, err := podB.LoadPublished("f1"); err != nil {
		t.Fatalf("pod B could not load the published revision: %v", err)
	}
}

// Two instances hammering one workspace at once. Under git this is the case
// that corrupts `.git/index`; here it must merely interleave.
func TestPgBackend_ConcurrentInstancesDoNotCorrupt(t *testing.T) {
	podA, podB := pgTestWorkspace(t)

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, 4*n)
	for i := range n {
		wg.Add(2)
		// Distinct flows from both pods at once.
		go func() {
			defer wg.Done()
			if _, err := podA.Save(flow(fmt.Sprintf("a%02d", i), "A"), "ada"); err != nil {
				errs <- fmt.Errorf("pod A save: %w", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := podB.Save(flow(fmt.Sprintf("b%02d", i), "B"), "grace"); err != nil {
				errs <- fmt.Errorf("pod B save: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write across instances: %v", err)
	}

	ids, err := podA.ListGraphs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(ids) != 2*n {
		t.Fatalf("ListGraphs = %d flows, want %d — a concurrent write was lost", len(ids), 2*n)
	}
	// Every one of them is readable and intact.
	for _, id := range ids {
		g, err := podB.Load(id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		if len(g.Nodes) != 1 {
			t.Fatalf("flow %s came back malformed: %+v", id, g)
		}
	}
}

// Concurrent edits to the SAME flow are last-writer-wins, not corruption: both
// land in the history and the flow reads as one of them.
func TestPgBackend_ConcurrentEditsToOneFlowResolveCleanly(t *testing.T) {
	podA, podB := pgTestWorkspace(t)
	mustSave(t, podA, flow("f1", "base"), "u")

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = podA.Save(flow("f1", fmt.Sprintf("A-%d", i)), "ada")
		}()
		go func() {
			defer wg.Done()
			_, _ = podB.Save(flow("f1", fmt.Sprintf("B-%d", i)), "grace")
		}()
	}
	wg.Wait()

	g, err := podA.Load("f1")
	if err != nil {
		t.Fatalf("load after concurrent edits: %v", err)
	}
	if g.ID != "f1" || len(g.Nodes) != 1 {
		t.Fatalf("flow is malformed after concurrent edits: %+v", g)
	}
	revs, err := podA.History("f1", 200)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(revs) < 2 {
		t.Fatalf("history = %d entries, want the concurrent edits recorded", len(revs))
	}
	// The flow's current content must be one that was actually written.
	head, err := podB.ResolveFor("f1", "HEAD")
	if err != nil || head == "" {
		t.Fatalf("ResolveFor HEAD = %q / %v", head, err)
	}
	if _, err := podB.LoadAt(head, "f1"); err != nil {
		t.Fatalf("head revision %q is not loadable: %v", head, err)
	}
}

// Mirroring is a push of a real git repository, and a Postgres workspace has
// none. It must say so rather than silently doing nothing.
func TestPgBackend_MirroringIsRefusedClearly(t *testing.T) {
	podA, _ := pgTestWorkspace(t)
	if _, err := podA.Push(context.Background(), "https://example.com/x.git", nil); err == nil {
		t.Fatal("Push on a Postgres workspace should not succeed")
	} else if !errors.Is(err, ErrMirrorUnsupported) {
		t.Fatalf("Push error = %v, want ErrMirrorUnsupported", err)
	}
	if _, err := podA.PushOverwritingUnrelated(context.Background(), "https://example.com/x.git", nil); !errors.Is(err, ErrMirrorUnsupported) {
		t.Fatalf("PushOverwritingUnrelated error = %v, want ErrMirrorUnsupported", err)
	}
}
