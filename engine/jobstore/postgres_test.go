// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Integration test against a real Postgres. Skipped unless DAZYFLOW_TEST_DB
// is set, e.g.
//
//	DAZYFLOW_TEST_DB=postgres://localhost/dazyflow_test go test ./...
func TestPostgres_RoundTrip(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()

	// Clean slate within the test's namespace.
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")

	// Kind must be node — Claim only hands out node-kind work units
	// (graph-kind records are the parent submission, never claimed).
	rec := core.JobRecord{ID: "pg-1", Kind: core.JobKindNode, GraphID: "g", NodeID: "n", Tenant: "t"}
	if err := store.Enqueue(ctx, rec); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.ID != "pg-1" {
		t.Errorf("claimed.ID = %q", claimed.ID)
	}
	if err := store.Complete(ctx, "pg-1", core.JobStatusSucceeded, &core.Result{Status: "ok"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, err := store.Get(ctx, "pg-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q", got.Status)
	}
}

// TestPostgres_MaxConcurrentPerTenant exercises the per-tenant soft cap
// against a real Postgres. Skipped unless DAZYFLOW_TEST_DB is set.
func TestPostgres_MaxConcurrentPerTenant(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")
	store.SetMaxConcurrentPerTenant(2)

	for _, id := range []string{"a1", "a2", "a3"} {
		if err := store.Enqueue(ctx, core.JobRecord{ID: id, Kind: core.JobKindNode, Tenant: "acme"}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	// Two claims succeed, then acme is at its cap.
	if _, err := store.Claim(ctx, "w", 30*time.Second); err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	if _, err := store.Claim(ctx, "w", 30*time.Second); err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if _, err := store.Claim(ctx, "w", 30*time.Second); !errors.Is(err, core.ErrNoJobs) {
		t.Fatalf("claim 3 err = %v, want ErrNoJobs (acme at cap)", err)
	}

	// A different tenant is unaffected.
	if err := store.Enqueue(ctx, core.JobRecord{ID: "b1", Kind: core.JobKindNode, Tenant: "globex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "w", 30*time.Second); err != nil {
		t.Fatalf("globex claim should succeed despite acme at cap: %v", err)
	}
}

// TestPostgres_Conformance runs the shared store-conformance suite
// against real Postgres. Each subtest truncates jobs first so they don't
// interfere with each other.
func TestPostgres_Conformance(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(store.Close)
	runConformance(t, func(t *testing.T) core.JobStore {
		_, err := store.pool.Exec(ctx, "TRUNCATE jobs")
		if err != nil {
			t.Fatalf("TRUNCATE: %v", err)
		}
		return store
	})
}

// TestPostgres_OpenPostgres_BadDSN covers the connect / schema failure
// paths so the OpenPostgres + NewPostgresFromPool branches that return
// errors get exercised.
func TestPostgres_OpenPostgres_BadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// pgxpool.New rejects an obviously malformed DSN synchronously.
	if _, err := OpenPostgres(ctx, "not-a-valid-dsn"); err == nil {
		t.Errorf("OpenPostgres on bad DSN: want error, got nil")
	}
}

// TestPostgres_NewPostgresFromPool_NilPool exercises the nil-pool guard.
func TestPostgres_NewPostgresFromPool_NilPool(t *testing.T) {
	if _, err := NewPostgresFromPool(t.Context(), nil); err == nil {
		t.Errorf("NewPostgresFromPool(nil) = nil, want error")
	}
}

// TestPostgres_CompleteOwned_FencesNonOwner mirrors the memory test
// against real Postgres. Skipped unless DAZYFLOW_TEST_DB is set.
func TestPostgres_CompleteOwned_FencesNonOwner(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")

	if err := store.Enqueue(ctx, core.JobRecord{ID: "j1", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "worker-A", 30*time.Second); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.CompleteOwned(ctx, "j1", "worker-B", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("CompleteOwned by non-owner = %v, want ErrConflict", err)
	}
	if rec, _ := store.Get(ctx, "j1"); rec.Status != core.JobStatusRunning {
		t.Errorf("status = %q after fenced write, want running", rec.Status)
	}
	if err := store.CompleteOwned(ctx, "j1", "worker-A", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("owner CompleteOwned: %v", err)
	}
}

// openPG opens the Postgres store with a clean jobs table, skipping when
// DAZYFLOW_TEST_DB is unset. Returns the store and a live context.
func openPG(t *testing.T) (*Postgres, context.Context) {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return store, ctx
}

// TestPostgres_PruneTerminal exercises the retention sweep: only terminal,
// old-enough rows are removed, in batches.
func TestPostgres_PruneTerminal(t *testing.T) {
	store, ctx := openPG(t)

	// olderThan <= 0 is a guarded no-op.
	if n, err := store.PruneTerminal(ctx, 0, 100); err != nil || n != 0 {
		t.Errorf("PruneTerminal(0) = %d, %v; want 0, nil", n, err)
	}

	// Two terminal rows + one still-running row.
	for _, id := range []string{"t1", "t2"} {
		if err := store.Enqueue(ctx, core.JobRecord{ID: id, Kind: core.JobKindNode, Tenant: "t"}); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(ctx, id, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Enqueue(ctx, core.JobRecord{ID: "live", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}

	// batch=1 forces the multi-iteration loop; both terminal rows go,
	// the running row stays.
	n, err := store.PruneTerminal(ctx, time.Nanosecond, 1)
	if err != nil {
		t.Fatalf("PruneTerminal: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want 2", n)
	}
	if _, err := store.Get(ctx, "live"); err != nil {
		t.Errorf("running row should survive prune: %v", err)
	}
	if _, err := store.Get(ctx, "t1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("t1 should be pruned: %v", err)
	}
}

// TestPostgres_OldestQueuedEnqueuedAt covers the queue-latency probe.
func TestPostgres_OldestQueuedEnqueuedAt(t *testing.T) {
	store, ctx := openPG(t)

	// Empty queue reports no row.
	if _, ok, err := store.OldestQueuedEnqueuedAt(ctx); err != nil || ok {
		t.Errorf("empty queue = ok %v, err %v; want false, nil", ok, err)
	}

	// A future-available row must not count as claimable. Enqueue stores no
	// available_at (only Requeue sets it), so requeue the row into the future.
	if err := store.Enqueue(ctx, core.JobRecord{ID: "later", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Requeue(ctx, "later", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if _, ok, _ := store.OldestQueuedEnqueuedAt(ctx); ok {
		t.Errorf("future-available row counted as queued")
	}

	// An immediately-claimable row is reported.
	if err := store.Enqueue(ctx, core.JobRecord{ID: "now", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	at, ok, err := store.OldestQueuedEnqueuedAt(ctx)
	if err != nil || !ok {
		t.Fatalf("OldestQueuedEnqueuedAt = ok %v, err %v; want true, nil", ok, err)
	}
	if at.IsZero() {
		t.Error("enqueued_at is zero")
	}
}
