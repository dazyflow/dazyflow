// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

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

func TestIPRateLimiter_GCAndEvict(t *testing.T) {
	l := newIPRateLimiter(60, 5)

	if !l.Allow("1.1.1.1") {
		t.Fatal("first request should be allowed")
	}

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

	l.mu.Lock()
	l.buckets = map[string]*tokenBucket{}
	l.evictOldestLocked()
	l.mu.Unlock()
}

func TestIPRateLimiter_BurstExhaustion(t *testing.T) {
	l := newIPRateLimiter(60, 2) // burst 2
	for i := 0; i < 2; i++ {
		if !l.Allow("ip") {
			t.Fatalf("request %d should pass the burst", i+1)
		}
	}
	if l.Allow("ip") {
		t.Fatal("third immediate request should be throttled")
	}
}

func TestIdempotencyStore_EvictLocked(t *testing.T) {
	s := newIdempotencyStore()
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

// A bucket abandoned while DEPLETED must still be reclaimed once enough time
// has passed for it to refill. Tokens are only updated inside Allow, so the
// stale count on the record says "still in debt" long after the bucket is
// indistinguishable from a fresh one — and those are precisely the buckets a
// scanner or credential-stuffer leaves behind, so leaking them defeated the
// sweep for the traffic it exists to survive.
func TestIPRateLimiter_GCReclaimsDepletedIdleBucket(t *testing.T) {
	l := newIPRateLimiter(60, 5) // 1 token/sec, burst 5

	l.mu.Lock()
	l.lastGC = time.Now().Add(-2 * time.Minute) // force the sweep to run
	l.buckets["drained-and-gone"] = &tokenBucket{tokens: 0, last: time.Now().Add(-10 * time.Minute)}
	l.gcLocked(time.Now())
	_, kept := l.buckets["drained-and-gone"]
	l.mu.Unlock()
	if kept {
		t.Error("a depleted bucket idle long enough to fully refill should be GC'd")
	}
}

func TestIPRateLimiter_GCKeepsIdleButStillIndebtedBucket(t *testing.T) {
	l := newIPRateLimiter(1, 100) // 1/60 tok/sec, burst 100 — very slow refill

	l.mu.Lock()
	l.lastGC = time.Now().Add(-2 * time.Minute)
	l.buckets["still-throttled"] = &tokenBucket{tokens: 0, last: time.Now().Add(-2 * time.Minute)}
	l.gcLocked(time.Now())
	_, kept := l.buckets["still-throttled"]
	l.mu.Unlock()
	if !kept {
		t.Error("a bucket that has NOT refilled must be kept so its throttle survives")
	}
}

// Generating a flow is a dozen model calls behind one request, so the bucket
// guards a bill. Everything here is about WHAT it counts by.
func TestRateLimitGenerate_CountsByTenantNotByCaller(t *testing.T) {
	t.Parallel()
	h := &HTTPGateway{GenerateRateLimit: newIPRateLimiter(60, 2)}
	calls := 0
	guarded := h.rateLimitGenerate(func(http.ResponseWriter, *http.Request, core.Principal) {
		calls++
	})
	ask := func(tenant, addr string) int {
		rw := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/tools/flow/generate", nil)
		r.RemoteAddr = addr
		guarded(rw, r, core.Principal{Tenant: tenant})
		return rw.Code
	}

	// A leaked key sprayed across many addresses is the case a per-IP bucket
	// would wave through: same tenant, a new source every time.
	if got := ask("acme", "203.0.113.1:1"); got != http.StatusOK {
		t.Fatalf("first call = %d, want 200", got)
	}
	if got := ask("acme", "203.0.113.2:1"); got != http.StatusOK {
		t.Fatalf("second call = %d, want 200 (burst is 2)", got)
	}
	if got := ask("acme", "203.0.113.3:1"); got != http.StatusTooManyRequests {
		t.Fatalf("third call from a THIRD address = %d, want 429 — the bucket must be the tenant's", got)
	}
	if calls != 2 {
		t.Errorf("handler ran %d times, want 2", calls)
	}

	// And one tenant burning its own budget must not spend anyone else's.
	if got := ask("other", "203.0.113.1:1"); got != http.StatusOK {
		t.Errorf("a different tenant = %d, want 200", got)
	}
}

func TestRateLimitGenerate_SaysHowLongToWait(t *testing.T) {
	t.Parallel()
	h := &HTTPGateway{GenerateRateLimit: newIPRateLimiter(60, 1)}
	guarded := h.rateLimitGenerate(func(http.ResponseWriter, *http.Request, core.Principal) {})
	for range 2 {
		rw := httptest.NewRecorder()
		guarded(rw, httptest.NewRequest(http.MethodPost, "/x", nil), core.Principal{Tenant: "acme"})
		if rw.Code == http.StatusTooManyRequests && rw.Header().Get("Retry-After") == "" {
			t.Error("a 429 with no Retry-After leaves the caller guessing")
		}
	}
}

// An unconfigured limiter must not close the door: the gateway builds one by
// default, but a zero value has to stay open rather than refuse everything.
func TestRateLimitGenerate_UnsetLimiterAllows(t *testing.T) {
	t.Parallel()
	h := &HTTPGateway{}
	ran := false
	h.rateLimitGenerate(func(http.ResponseWriter, *http.Request, core.Principal) { ran = true })(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", nil), core.Principal{Tenant: "acme"})
	if !ran {
		t.Error("no limiter configured, yet the request was refused")
	}
}

// Both dial-out routes share one bucket on purpose: they cost the same socket
// held for the same twenty seconds, so spending the budget on one has to spend
// it for the other.
func TestRateLimitProbe_SharesOneBudgetAcrossBothDialRoutes(t *testing.T) {
	t.Parallel()
	h := &HTTPGateway{ProbeRateLimit: newIPRateLimiter(60, 2)}
	scan := h.rateLimitProbe(func(http.ResponseWriter, *http.Request, core.Principal) {})
	verify := h.rateLimitProbe(func(http.ResponseWriter, *http.Request, core.Principal) {})
	ask := func(handler authedHandler) int {
		rw := httptest.NewRecorder()
		handler(rw, httptest.NewRequest(http.MethodPost, "/x", nil), core.Principal{Tenant: "acme"})
		return rw.Code
	}

	if got := ask(scan); got != http.StatusOK {
		t.Fatalf("first scan = %d, want 200", got)
	}
	if got := ask(verify); got != http.StatusOK {
		t.Fatalf("first verify = %d, want 200 (burst is 2)", got)
	}
	if got := ask(scan); got != http.StatusTooManyRequests {
		t.Fatalf("third dial = %d, want 429 — one budget covers both routes", got)
	}
}

// The two limiters must not share a bucket: checking a server should never
// spend the budget for generating a flow.
func TestRateLimits_GenerateAndProbeAreSeparateBudgets(t *testing.T) {
	t.Parallel()
	h := &HTTPGateway{
		ProbeRateLimit:    newIPRateLimiter(60, 1),
		GenerateRateLimit: newIPRateLimiter(60, 1),
	}
	ask := func(handler authedHandler) int {
		rw := httptest.NewRecorder()
		handler(rw, httptest.NewRequest(http.MethodPost, "/x", nil), core.Principal{Tenant: "acme"})
		return rw.Code
	}
	noop := func(http.ResponseWriter, *http.Request, core.Principal) {}

	if got := ask(h.rateLimitProbe(noop)); got != http.StatusOK {
		t.Fatalf("probe = %d, want 200", got)
	}
	if got := ask(h.rateLimitProbe(noop)); got != http.StatusTooManyRequests {
		t.Fatalf("second probe = %d, want 429", got)
	}
	if got := ask(h.rateLimitGenerate(noop)); got != http.StatusOK {
		t.Errorf("generation = %d, want 200 — a spent probe budget must not close it", got)
	}
}
