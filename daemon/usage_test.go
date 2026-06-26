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
	for i := 0; i < 2; i++ {
		if err := store.AddSkippedRun(ctx, "acme", usageNow); err != nil {
			t.Fatalf("AddSkippedRun: %v", err)
		}
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
	if got[0].Period != "2026-06" || got[0].GraphRuns != 3 || got[0].NodeExecutions != 7 || got[0].SkippedRuns != 2 {
		t.Errorf("current bucket = %+v, want 2026-06/3/7 skipped=2", got[0])
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

// Gated on DAZYFLOW_TEST_DB (a real Postgres), like the jobstore/auth
// integration tests.
func TestPgUsageStore(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres usage tests")
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

// countingUsage wraps MemUsageStore and counts inner write calls.
type countingUsage struct {
	*MemUsageStore
	nodeWrites int
}

func (c *countingUsage) AddNodeExecutions(ctx context.Context, tenant string, n int, now time.Time) error {
	c.nodeWrites++
	return c.MemUsageStore.AddNodeExecutions(ctx, tenant, n, now)
}

func TestBufferedUsage(t *testing.T) {
	inner := &countingUsage{MemUsageStore: NewMemUsageStore()}
	b := NewBufferedUsage(inner)
	ctx := context.Background()

	// 50 node executions accumulate without touching the store…
	for i := 0; i < 50; i++ {
		if err := b.AddNodeExecutions(ctx, "acme", 1, usageNow); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if inner.nodeWrites != 0 {
		t.Fatalf("inner writes before flush = %d", inner.nodeWrites)
	}
	// …runs pass straight through (the run gate reads them)…
	if err := b.AddRun(ctx, "acme", usageNow); err != nil {
		t.Fatalf("run: %v", err)
	}
	// …and a read flushes first, so the caller sees everything.
	got, err := b.Usage(ctx, "acme", 1)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if inner.nodeWrites != 1 {
		t.Errorf("inner writes after read-flush = %d, want 1 (batched)", inner.nodeWrites)
	}
	if len(got) != 1 || got[0].NodeExecutions != 50 || got[0].GraphRuns != 1 {
		t.Errorf("buckets = %+v, want 50 executions / 1 run", got)
	}
	// A flush with nothing pending writes nothing.
	if err := b.Flush(ctx); err != nil || inner.nodeWrites != 1 {
		t.Errorf("idle flush: err=%v writes=%d", err, inner.nodeWrites)
	}
}

func TestCachedPlanStore(t *testing.T) {
	inner := NewMemPlanStore()
	c := NewCachedPlanStore(inner, time.Minute)
	ctx := context.Background()

	// First read goes through; the second is served from cache even if
	// the inner store changes underneath (TTL staleness by design).
	p, err := c.GetPlan(ctx, "acme")
	if err != nil || p.Plan != PlanFree {
		t.Fatalf("first read = %+v/%v", p, err)
	}
	_ = inner.SetPlan(ctx, TenantPlan{Tenant: "acme", Plan: PlanPro}) // behind the cache's back
	p, _ = c.GetPlan(ctx, "acme")
	if p.Plan != PlanFree {
		t.Errorf("cached read = %q, want the cached free plan", p.Plan)
	}
	// SetPlan through the cache writes through AND refreshes it.
	if err := c.SetPlan(ctx, TenantPlan{Tenant: "acme", Plan: PlanPro}); err != nil {
		t.Fatalf("set: %v", err)
	}
	p, _ = c.GetPlan(ctx, "acme")
	if p.Plan != PlanPro {
		t.Errorf("after write-through = %q, want pro", p.Plan)
	}
	// The dedupe extension passes through to the inner store.
	first, err := c.MarkStripeEvent(ctx, "evt_x")
	if err != nil || !first {
		t.Errorf("dedupe first = %v/%v", first, err)
	}
	again, _ := c.MarkStripeEvent(ctx, "evt_x")
	if again {
		t.Error("dedupe replay reported first=true through the cache")
	}
}
