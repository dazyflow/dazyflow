package db

import (
	"os"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// Unit tests — no real Postgres needed. We test the registry's
// bookkeeping by stubbing the pool field directly; the connection
// itself is never used.
// ---------------------------------------------------------------------

func TestPGRegistry_CachesPerKey(t *testing.T) {
	r := newPGPoolRegistry(time.Hour, time.Hour)

	// Inject fake entries — we never call New, so no real connections.
	// pool=nil keeps closeAll defensive but never reached here.
	r.pools[pgPoolKey{"acmeA", "dsn1"}] = &pgEntry{pool: nil, lastUse: time.Now()}
	r.pools[pgPoolKey{"acmeA", "dsn2"}] = &pgEntry{pool: nil, lastUse: time.Now()}
	r.pools[pgPoolKey{"acmeB", "dsn1"}] = &pgEntry{pool: nil, lastUse: time.Now()}

	if len(r.pools) != 3 {
		t.Fatalf("expected 3 distinct keys, got %d", len(r.pools))
	}
	// Confirm key semantics: tenant is part of the key, so (acmeA,
	// dsn1) and (acmeB, dsn1) must be different.
	if _, ok := r.pools[pgPoolKey{"acmeA", "dsn1"}]; !ok {
		t.Error("acmeA/dsn1 missing")
	}
	if _, ok := r.pools[pgPoolKey{"acmeB", "dsn1"}]; !ok {
		t.Error("acmeB/dsn1 missing")
	}
}

func TestPGRegistry_SweepEvictsIdle(t *testing.T) {
	// Idle = 100ms; entry with lastUse=now-200ms should be evicted.
	r := newPGPoolRegistry(100*time.Millisecond, 0)
	now := time.Now()
	r.pools[pgPoolKey{"t", "fresh"}] = &pgEntry{pool: nil, lastUse: now}
	r.pools[pgPoolKey{"t", "stale"}] = &pgEntry{pool: nil, lastUse: now.Add(-200 * time.Millisecond)}

	r.mu.Lock()
	r.sweepLocked(now)
	r.mu.Unlock()

	if _, ok := r.pools[pgPoolKey{"t", "fresh"}]; !ok {
		t.Error("fresh entry evicted")
	}
	if _, ok := r.pools[pgPoolKey{"t", "stale"}]; ok {
		t.Error("stale entry not evicted")
	}
}

func TestPGRegistry_SweepRespectsBoundary(t *testing.T) {
	// Exactly at the idle boundary the entry stays (the check is
	// strictly greater-than). One ns over → evicted.
	r := newPGPoolRegistry(time.Second, 0)
	now := time.Now()
	r.pools[pgPoolKey{"t", "boundary"}] = &pgEntry{pool: nil, lastUse: now.Add(-time.Second)}
	r.pools[pgPoolKey{"t", "past"}] = &pgEntry{pool: nil, lastUse: now.Add(-time.Second - time.Nanosecond)}

	r.mu.Lock()
	r.sweepLocked(now)
	r.mu.Unlock()

	if _, ok := r.pools[pgPoolKey{"t", "boundary"}]; !ok {
		t.Error("at-boundary entry should survive")
	}
	if _, ok := r.pools[pgPoolKey{"t", "past"}]; ok {
		t.Error("past-boundary entry should be evicted")
	}
}

func TestPGRegistry_BadDSNDoesNotPoison(t *testing.T) {
	// A bad DSN should return an error without leaving a dead entry
	// in the map — otherwise every subsequent call for that key
	// would short-circuit on a broken pool.
	r := newPGPoolRegistry(time.Hour, time.Hour)
	_, err := r.pgPool(t.Context(), "acme", "totally-not-a-valid-dsn")
	if err == nil {
		t.Fatal("expected an error from a malformed DSN")
	}
	if len(r.pools) != 0 {
		t.Errorf("bad DSN poisoned registry: %d entries", len(r.pools))
	}
}

// ---------------------------------------------------------------------
// Integration test — gated on HAZYFLOW_TEST_DB. Confirms that two
// consecutive pgPool calls for the same key return the same pool
// pointer (i.e., we actually reuse rather than creating a fresh one).
// ---------------------------------------------------------------------

func TestPGRegistry_ReusesPoolAcrossCalls(t *testing.T) {
	dsn := os.Getenv("HAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	r := newPGPoolRegistry(time.Hour, time.Hour)
	t.Cleanup(r.closeAll)

	p1, err := r.pgPool(t.Context(), "acme", dsn)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	p2, err := r.pgPool(t.Context(), "acme", dsn)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if p1 != p2 {
		t.Errorf("same (tenant, dsn) returned different pools: %p vs %p", p1, p2)
	}
	// Different tenant on the same DSN MUST get a different pool.
	p3, err := r.pgPool(t.Context(), "other", dsn)
	if err != nil {
		t.Fatalf("other tenant: %v", err)
	}
	if p1 == p3 {
		t.Error("different tenants shared a pool — isolation broken")
	}
}
