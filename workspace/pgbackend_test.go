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

// Concurrent saves used to read the head without a lock, so several landed
// on the same parent and forked the chain. With the head row locked every
// save succeeds and the chain stays linear: exactly one root, and no revision
// is the parent of more than one other.
func TestPgBackend_ConcurrentSavesKeepTheChainLinear(t *testing.T) {
	podA, podB := pgTestWorkspace(t)
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := podA.Save(flow("f1", fmt.Sprintf("A-%d", i)), "ada"); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := podB.Save(flow("f1", fmt.Sprintf("B-%d", i)), "grace"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent save: %v", err)
	}
	pg := podA.b.(*pgBackend)
	rows, err := pg.pool.Query(context.Background(),
		`SELECT parent, count(*) FROM flow_revisions
		  WHERE tenant=$1 AND workspace=$2 AND graph_id='f1' GROUP BY parent`,
		pg.tenant, pg.workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var parent string
		var c int
		if err := rows.Scan(&parent, &c); err != nil {
			t.Fatal(err)
		}
		total += c
		if c != 1 {
			t.Errorf("parent %q has %d children, want a linear chain", parent, c)
		}
	}
	if total != 2*n {
		t.Errorf("recorded %d revisions, want %d", total, 2*n)
	}
}

// A Store is opened per request, so each mirror() call builds a fresh
// pgMirror; they must still share one lock per workspace or concurrent pushes
// rebuild the same cache directory at once.
func TestPgBackend_MirrorsOfOneWorkspaceShareALock(t *testing.T) {
	a := &pgBackend{tenant: "t", workspace: "w", mirrorDir: "/cache/t/w"}
	b := &pgBackend{tenant: "t", workspace: "w", mirrorDir: "/cache/t/w"}
	other := &pgBackend{tenant: "t", workspace: "other", mirrorDir: "/cache/t/other"}
	ma, _ := a.mirror()
	mb, _ := b.mirror()
	mo, _ := other.mirror()
	if ma.(*pgMirror).mu != mb.(*pgMirror).mu {
		t.Fatal("two mirrors of one workspace hold different locks")
	}
	if ma.(*pgMirror).mu == mo.(*pgMirror).mu {
		t.Fatal("different workspaces share a mirror lock")
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
