// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strconv"
	"testing"
	"time"
)

// TestIPRateLimiter_GCAndEvict directly exercises gcLocked (drops idle, fully-
// refilled buckets) and evictOldestLocked (removes the LRU bucket).
func TestIPRateLimiter_GCAndEvict(t *testing.T) {
	l := newIPRateLimiter(60, 5)

	// Normal Allow path: first request from an IP creates a bucket and passes.
	if !l.Allow("1.1.1.1") {
		t.Fatal("first request should be allowed")
	}

	// gcLocked: an idle, fully-refilled bucket is forgotten; a recently-used
	// one (with debt) is kept.
	l.mu.Lock()
	l.lastGC = time.Now().Add(-2 * time.Minute) // force GC to run
	l.buckets["idle"] = &tokenBucket{tokens: l.burst, last: time.Now().Add(-2 * time.Minute)}
	l.buckets["busy"] = &tokenBucket{tokens: 0, last: time.Now()}
	l.gcLocked(time.Now())
	_, idleKept := l.buckets["idle"]
	_, busyKept := l.buckets["busy"]
	l.mu.Unlock()
	if idleKept {
		t.Error("idle full bucket should be GC'd")
	}
	if !busyKept {
		t.Error("busy bucket should be kept")
	}

	// evictOldestLocked: with several buckets, the one with the oldest `last`
	// is removed.
	l.mu.Lock()
	l.buckets = map[string]*tokenBucket{
		"old": {tokens: 1, last: time.Now().Add(-time.Hour)},
		"mid": {tokens: 1, last: time.Now().Add(-time.Minute)},
		"new": {tokens: 1, last: time.Now()},
	}
	l.evictOldestLocked()
	_, oldGone := l.buckets["old"]
	n := len(l.buckets)
	l.mu.Unlock()
	if oldGone {
		t.Error("evictOldestLocked should remove the oldest bucket")
	}
	if n != 2 {
		t.Fatalf("after evict = %d buckets, want 2", n)
	}

	// evictOldestLocked on an empty map is a safe no-op.
	l.mu.Lock()
	l.buckets = map[string]*tokenBucket{}
	l.evictOldestLocked()
	l.mu.Unlock()
}

// TestIPRateLimiter_BurstExhaustion drives Allow until the burst is spent so
// the deny branch is covered.
func TestIPRateLimiter_BurstExhaustion(t *testing.T) {
	l := newIPRateLimiter(60, 2) // burst 2
	if !l.Allow("ip") || !l.Allow("ip") {
		t.Fatal("first two requests should pass the burst")
	}
	if l.Allow("ip") {
		t.Fatal("third immediate request should be throttled")
	}
}

// TestIdempotencyStore_EvictLocked directly forces the cap-eviction loop by
// constructing an over-cap store and calling evictLocked.
func TestIdempotencyStore_EvictLocked(t *testing.T) {
	s := newIdempotencyStore()
	// Seed cap+2 entries so evictLocked drops the two oldest.
	total := idempotencyMaxCache + 2
	for i := 0; i < total; i++ {
		key := keyFor(i)
		s.entries[key] = &idempotentResponse{storedAt: time.Now()}
		s.order = append(s.order, key)
	}
	s.evictLocked()
	if len(s.entries) != idempotencyMaxCache {
		t.Fatalf("after evict = %d entries, want %d", len(s.entries), idempotencyMaxCache)
	}
	// The two oldest keys are gone.
	if _, ok := s.entries[keyFor(0)]; ok {
		t.Error("oldest entry survived eviction")
	}
	if _, ok := s.entries[keyFor(1)]; ok {
		t.Error("second-oldest entry survived eviction")
	}
}

func keyFor(i int) string {
	return "k-" + strconv.Itoa(i)
}
