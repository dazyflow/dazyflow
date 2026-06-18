package daemon

import (
	"net/http"
	"sync"
	"testing"
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
