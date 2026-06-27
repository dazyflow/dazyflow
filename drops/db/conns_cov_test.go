// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"testing"
	"time"
)

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
