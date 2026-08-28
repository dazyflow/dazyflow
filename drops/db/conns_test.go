// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// TestMySQLSSRFDial_BlocksAtDial proves the MySQL egress guard runs at the
// actual dial on the resolved address — not just as a pre-flight hostname
// check — so it resists DNS rebinding. We exercise the exact dialer the
// driver invokes (ssrfMySQLDial) against private/metadata destinations.
func TestMySQLSSRFDial_BlocksAtDial(t *testing.T) {
	// This package's TestMain turns private egress ON (so integration tests
	// can reach a local DB), which disables the guard. Turn it OFF for this
	// test, then restore the package default. With the guard active the
	// Control rejects each address AFTER DNS resolution but BEFORE connecting,
	// so these assertions never touch the network.
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	for _, addr := range []string{
		"127.0.0.1:3306",       // loopback
		"169.254.169.254:3306", // cloud metadata (link-local)
		"10.1.2.3:3306",        // RFC1918 private
		"[::1]:3306",           // IPv6 loopback
	} {
		t.Run(addr, func(t *testing.T) {
			_, err := ssrfMySQLDial(context.Background(), addr)
			if err == nil || !strings.Contains(err.Error(), "ssrf_blocked") {
				t.Fatalf("dial to %s must be ssrf_blocked, got %v", addr, err)
			}
		})
	}
}

// TestRegisterMySQLSSRFDialer_Idempotent: the once-guarded registration is
// safe to call repeatedly (it runs on every MySQL connect).
func TestRegisterMySQLSSRFDialer_Idempotent(t *testing.T) {
	registerMySQLSSRFDialer()
	registerMySQLSSRFDialer() // must not panic on the global driver map
}

// ---------------------------------------------------------------------
// Unit tests — no real Postgres needed. We test the registry's
// bookkeeping by stubbing the pool field directly; the connection
// itself is never used.
// ---------------------------------------------------------------------

func TestPGRegistry_CachesPerKey(t *testing.T) {
	r := newPGPoolRegistry(time.Hour, time.Hour)

	// Inject fake entries — we never call New, so no real connections.
	// pool=nil keeps closeAll defensive but never reached here.
	r.pools[dbConnKey{"acmeA", "dsn1"}] = &pgEntry{pool: nil, lastUse: time.Now()}
	r.pools[dbConnKey{"acmeA", "dsn2"}] = &pgEntry{pool: nil, lastUse: time.Now()}
	r.pools[dbConnKey{"acmeB", "dsn1"}] = &pgEntry{pool: nil, lastUse: time.Now()}

	if len(r.pools) != 3 {
		t.Fatalf("expected 3 distinct keys, got %d", len(r.pools))
	}
	// Confirm key semantics: tenant is part of the key, so (acmeA,
	// dsn1) and (acmeB, dsn1) must be different.
	if _, ok := r.pools[dbConnKey{"acmeA", "dsn1"}]; !ok {
		t.Error("acmeA/dsn1 missing")
	}
	if _, ok := r.pools[dbConnKey{"acmeB", "dsn1"}]; !ok {
		t.Error("acmeB/dsn1 missing")
	}
}

func TestPGRegistry_SweepEvictsIdle(t *testing.T) {
	// Idle = 100ms; entry with lastUse=now-200ms should be evicted.
	r := newPGPoolRegistry(100*time.Millisecond, 0)
	now := time.Now()
	r.pools[dbConnKey{"t", "fresh"}] = &pgEntry{pool: nil, lastUse: now}
	r.pools[dbConnKey{"t", "stale"}] = &pgEntry{pool: nil, lastUse: now.Add(-200 * time.Millisecond)}

	r.mu.Lock()
	r.sweepLocked(now)
	r.mu.Unlock()

	if _, ok := r.pools[dbConnKey{"t", "fresh"}]; !ok {
		t.Error("fresh entry evicted")
	}
	if _, ok := r.pools[dbConnKey{"t", "stale"}]; ok {
		t.Error("stale entry not evicted")
	}
}

func TestPGRegistry_SweepRespectsBoundary(t *testing.T) {
	// Exactly at the idle boundary the entry stays (the check is
	// strictly greater-than). One ns over → evicted.
	r := newPGPoolRegistry(time.Second, 0)
	now := time.Now()
	r.pools[dbConnKey{"t", "boundary"}] = &pgEntry{pool: nil, lastUse: now.Add(-time.Second)}
	r.pools[dbConnKey{"t", "past"}] = &pgEntry{pool: nil, lastUse: now.Add(-time.Second - time.Nanosecond)}

	r.mu.Lock()
	r.sweepLocked(now)
	r.mu.Unlock()

	if _, ok := r.pools[dbConnKey{"t", "boundary"}]; !ok {
		t.Error("at-boundary entry should survive")
	}
	if _, ok := r.pools[dbConnKey{"t", "past"}]; ok {
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
// Integration test — gated on DAZYFLOW_TEST_DB. Confirms that two
// consecutive pgPool calls for the same key return the same pool
// pointer (i.e., we actually reuse rather than creating a fresh one).
// ---------------------------------------------------------------------

func TestPGRegistry_ReusesPoolAcrossCalls(t *testing.T) {
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
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

// TestSQLDBRegistry_CachesPerKey mirrors the pg registry's per-(tenant,dsn)
// keying using injected entries (no real connections opened).
func TestSQLDBRegistry_CachesPerKey(t *testing.T) {
	r := newSQLDBRegistry("mysql", time.Hour, time.Hour)
	r.dbs[dbConnKey{"acmeA", "dsn1"}] = &sqlDBEntry{db: nil, lastUse: time.Now()}
	r.dbs[dbConnKey{"acmeA", "dsn2"}] = &sqlDBEntry{db: nil, lastUse: time.Now()}
	r.dbs[dbConnKey{"acmeB", "dsn1"}] = &sqlDBEntry{db: nil, lastUse: time.Now()}

	if len(r.dbs) != 3 {
		t.Fatalf("expected 3 distinct keys, got %d", len(r.dbs))
	}
	if _, ok := r.dbs[dbConnKey{"acmeB", "dsn1"}]; !ok {
		t.Error("acmeB/dsn1 missing — tenant must be part of the key")
	}
}

// TestSQLDBRegistry_SweepRespectsBoundary covers the strictly-greater-than
// idle check at the exact boundary.
func TestSQLDBRegistry_SweepRespectsBoundary(t *testing.T) {
	r := newSQLDBRegistry("mysql", time.Second, 0)
	now := time.Now()
	r.dbs[dbConnKey{"t", "boundary"}] = &sqlDBEntry{db: nil, lastUse: now.Add(-time.Second)}
	r.dbs[dbConnKey{"t", "past"}] = &sqlDBEntry{db: nil, lastUse: now.Add(-time.Second - time.Nanosecond)}

	r.mu.Lock()
	r.sweepLocked(now)
	r.mu.Unlock()

	if _, ok := r.dbs[dbConnKey{"t", "boundary"}]; !ok {
		t.Error("at-boundary entry should survive")
	}
	if _, ok := r.dbs[dbConnKey{"t", "past"}]; ok {
		t.Error("past-boundary entry should be evicted")
	}
}

// TestSQLDBRegistry_OpportunisticSweepOnGet covers the sweep-on-access branch:
// a get after the sweep gap evicts a stale entry before the lookup. A bad DSN
// is used so no real connection is attempted.
func TestSQLDBRegistry_OpportunisticSweepOnGet(t *testing.T) {
	r := newSQLDBRegistry("mysql", 10*time.Millisecond, 0)
	// Pre-load a stale entry and force lastSweep into the past so the next
	// sqlDB call triggers the opportunistic sweep.
	r.dbs[dbConnKey{"t", "stale"}] = &sqlDBEntry{db: nil, lastUse: time.Now().Add(-time.Hour)}
	r.lastSweep = time.Now().Add(-time.Hour)

	// This call sweeps first (evicting "stale"), then fails on the bad DSN.
	_, _ = r.sqlDB(t.Context(), "t", "still not a valid dsn @@@")
	if _, ok := r.dbs[dbConnKey{"t", "stale"}]; ok {
		t.Error("stale entry should have been swept on access")
	}
}

// TestPGRegistry_OpportunisticSweepOnGet mirrors the above for the pg pool
// registry's sweep-on-access branch.
func TestPGRegistry_OpportunisticSweepOnGet(t *testing.T) {
	r := newPGPoolRegistry(10*time.Millisecond, 0)
	r.pools[dbConnKey{"t", "stale"}] = &pgEntry{pool: nil, lastUse: time.Now().Add(-time.Hour)}
	r.lastSweep = time.Now().Add(-time.Hour)

	_, _ = r.pgPool(t.Context(), "t", "totally-not-a-valid-dsn")
	if _, ok := r.pools[dbConnKey{"t", "stale"}]; ok {
		t.Error("stale entry should have been swept on access")
	}
}

// TestSQLDBRegistry_CloseAllNilTolerant confirms closeAll-equivalent cleanup
// via sweep handles nil db handles without panicking.
func TestSQLDBRegistry_SweepNilTolerant(t *testing.T) {
	r := newSQLDBRegistry("mysql", 0, 0)
	r.dbs[dbConnKey{"t", "x"}] = &sqlDBEntry{db: nil, lastUse: time.Now().Add(-time.Hour)}
	r.mu.Lock()
	r.sweepLocked(time.Now()) // must not panic on nil db
	r.mu.Unlock()
	if len(r.dbs) != 0 {
		t.Errorf("expected entry swept, got %d", len(r.dbs))
	}
}
