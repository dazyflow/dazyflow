// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// reloadTrustedProxiesForTest re-parses DAZYFLOW_TRUSTED_PROXIES into the
// package-level allowlist, bypassing the production sync.Once so a test
// can exercise clientIP with a specific config.
func reloadTrustedProxiesForTest() {
	trustedProxies = nil
	trustedProxiesOnce = sync.Once{}
	trustedProxiesOnce.Do(loadTrustedProxies)
}

func TestClientIP_NoTrustedProxiesUsesRemoteAddr(t *testing.T) {
	t.Setenv("DAZYFLOW_TRUSTED_PROXIES", "")
	reloadTrustedProxiesForTest()
	r := &http.Request{RemoteAddr: "203.0.113.7:5555", Header: http.Header{}}
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want peer 203.0.113.7 (XFF must be ignored when unconfigured)", got)
	}
}

func TestClientIP_UntrustedPeerIgnoresXFF(t *testing.T) {
	t.Setenv("DAZYFLOW_TRUSTED_PROXIES", "10.0.0.0/8")
	reloadTrustedProxiesForTest()
	// Peer is NOT in the trusted range, so its XFF can't be believed.
	r := &http.Request{RemoteAddr: "203.0.113.7:5555", Header: http.Header{}}
	r.Header.Set("X-Forwarded-For", "1.1.1.1")
	if got := clientIP(r); got != "203.0.113.7" {
		t.Errorf("clientIP = %q, want peer 203.0.113.7 (untrusted peer's XFF must be ignored)", got)
	}
}

func TestClientIP_TrustedPeerHonorsXFF(t *testing.T) {
	t.Setenv("DAZYFLOW_TRUSTED_PROXIES", "10.0.0.0/8")
	reloadTrustedProxiesForTest()
	r := &http.Request{RemoteAddr: "10.1.2.3:443", Header: http.Header{}}
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if got := clientIP(r); got != "198.51.100.9" {
		t.Errorf("clientIP = %q, want forwarded 198.51.100.9", got)
	}
}

func TestClientIP_TrustedPeerSkipsTrustedHops(t *testing.T) {
	t.Setenv("DAZYFLOW_TRUSTED_PROXIES", "10.0.0.0/8")
	reloadTrustedProxiesForTest()
	// Chain: real client, then two of our own proxies. The rightmost
	// non-trusted entry is the real client; client-injected prefixes
	// (an extra leftmost spoof) must not win.
	r := &http.Request{RemoteAddr: "10.1.2.3:443", Header: http.Header{}}
	r.Header.Set("X-Forwarded-For", "5.5.5.5, 198.51.100.9, 10.9.9.9")
	if got := clientIP(r); got != "198.51.100.9" {
		t.Errorf("clientIP = %q, want real client 198.51.100.9", got)
	}
}

func TestIPRateLimiter_BurstThenBlock(t *testing.T) {
	// 60/min = 1 token/sec, burst 3. First 3 immediate calls pass
	// (burst), the 4th is blocked (no time to refill).
	l := newIPRateLimiter(60, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("call %d should pass (within burst)", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Error("4th call should be blocked (burst exhausted)")
	}
}

func TestIPRateLimiter_PerIPIsolation(t *testing.T) {
	l := newIPRateLimiter(60, 1)
	if !l.Allow("a") {
		t.Fatal("first IP-a call should pass")
	}
	if l.Allow("a") {
		t.Fatal("second IP-a call should block")
	}
	// Different IP has its own bucket — unaffected by IP-a's exhaustion.
	if !l.Allow("b") {
		t.Error("IP-b should have its own full bucket")
	}
}

func TestNewAuthRateLimiter_SafeDefaultWhenZero(t *testing.T) {
	// An unconfigured (perMinute<=0) deploy must NOT disable throttling —
	// it falls back to the safe default policy so auth endpoints stay
	// protected against credential stuffing.
	l := NewAuthRateLimiter(0, 0)
	if l == nil {
		t.Fatal("perMinute=0 should fall back to a safe default, not nil")
	}
	// Burst is bounded: after defaultAuthRateBurst allowances the same IP is
	// throttled (the refill rate can't replenish within this tight loop).
	allowed := 0
	for i := 0; i < defaultAuthRateBurst+5; i++ {
		if l.Allow("1.2.3.4") {
			allowed++
		}
	}
	if allowed > defaultAuthRateBurst {
		t.Errorf("allowed %d requests, want <= burst %d", allowed, defaultAuthRateBurst)
	}
	if allowed == 0 {
		t.Error("limiter rejected every request; the default burst should allow some")
	}
}

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
