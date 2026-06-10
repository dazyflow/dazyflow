package daemon

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var usageNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func TestUsagePeriod(t *testing.T) {
	if got := usagePeriod(usageNow); got != "2026-06" {
		t.Errorf("got %q, want 2026-06", got)
	}
	// Local-zone timestamps bucket by their UTC month, so replicas in
	// different zones agree on the bucket.
	nyc := time.FixedZone("EST", -5*3600)
	if got := usagePeriod(time.Date(2026, 6, 30, 23, 0, 0, 0, nyc)); got != "2026-07" {
		t.Errorf("got %q, want 2026-07 (23:00 EST on the 30th is July UTC)", got)
	}
}

// usageStoreContract runs the behavior shared by both backends.
func usageStoreContract(t *testing.T, store UsageStore) {
	ctx := context.Background()

	// Counts accumulate per tenant per month.
	for i := 0; i < 3; i++ {
		if err := store.AddRun(ctx, "acme", usageNow); err != nil {
			t.Fatalf("AddRun: %v", err)
		}
	}
	if err := store.AddNodeExecutions(ctx, "acme", 7, usageNow); err != nil {
		t.Fatalf("AddNodeExecutions: %v", err)
	}
	// A different month gets its own bucket…
	prev := usageNow.AddDate(0, -1, 0)
	if err := store.AddRun(ctx, "acme", prev); err != nil {
		t.Fatalf("AddRun prev month: %v", err)
	}
	// …and a different tenant never bleeds in.
	if err := store.AddRun(ctx, "globex", usageNow); err != nil {
		t.Fatalf("AddRun other tenant: %v", err)
	}

	got, err := store.Usage(ctx, "acme", 12)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(got), got)
	}
	if got[0].Period != "2026-06" || got[0].GraphRuns != 3 || got[0].NodeExecutions != 7 {
		t.Errorf("current bucket = %+v, want 2026-06/3/7", got[0])
	}
	if got[1].Period != "2026-05" || got[1].GraphRuns != 1 || got[1].NodeExecutions != 0 {
		t.Errorf("previous bucket = %+v, want 2026-05/1/0", got[1])
	}

	// The months cap limits history, newest kept.
	capped, err := store.Usage(ctx, "acme", 1)
	if err != nil {
		t.Fatalf("Usage capped: %v", err)
	}
	if len(capped) != 1 || capped[0].Period != "2026-06" {
		t.Errorf("capped = %+v, want just 2026-06", capped)
	}

	// Unknown tenant: empty, not an error.
	none, err := store.Usage(ctx, "nobody", 12)
	if err != nil || len(none) != 0 {
		t.Errorf("unknown tenant = %v/%v, want empty/nil", none, err)
	}
}

func TestMemUsageStore(t *testing.T) {
	usageStoreContract(t, NewMemUsageStore())
}

func TestMemUsageStore_Concurrent(t *testing.T) {
	store := NewMemUsageStore()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.AddRun(context.Background(), "acme", usageNow)
			_ = store.AddNodeExecutions(context.Background(), "acme", 2, usageNow)
		}()
	}
	wg.Wait()
	got, _ := store.Usage(context.Background(), "acme", 1)
	if len(got) != 1 || got[0].GraphRuns != 50 || got[0].NodeExecutions != 100 {
		t.Errorf("got %+v, want 50 runs / 100 node executions", got)
	}
}

// Gated on HAZYFLOW_TEST_DB (a real Postgres), like the jobstore/auth
// integration tests.
func TestPgUsageStore(t *testing.T) {
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres usage tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPgUsageStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgUsageStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE usage_counters"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	usageStoreContract(t, store)
}
