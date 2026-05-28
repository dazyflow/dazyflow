package jobstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Integration test against a real Postgres. Skipped unless HAZYFLOW_TEST_DB
// is set, e.g.
//
//	HAZYFLOW_TEST_DB=postgres://localhost/hazyflow_test go test ./...
func TestPostgres_RoundTrip(t *testing.T) {
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
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
// against a real Postgres. Skipped unless HAZYFLOW_TEST_DB is set.
func TestPostgres_MaxConcurrentPerTenant(t *testing.T) {
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
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
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
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
// against real Postgres. Skipped unless HAZYFLOW_TEST_DB is set.
func TestPostgres_CompleteOwned_FencesNonOwner(t *testing.T) {
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
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
