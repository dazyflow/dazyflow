package jobstore

import (
	"context"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Integration test against a real Postgres. Skipped unless HAZYFLOW_TEST_DB
// is set, e.g.
//
//   HAZYFLOW_TEST_DB=postgres://localhost/hazyflow_test go test ./...
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

	rec := core.JobRecord{ID: "pg-1", GraphID: "g", NodeID: "n", Tenant: "t"}
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
