// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ipRateLimiter is a tiny per-client-IP token-bucket limiter for the
// auth endpoints (sign-in / sign-up), where unthrottled requests invite
// credential stuffing and signup spam. Self-contained on purpose — no
// external dependency — since the policy here is simple: a steady refill
// rate with a small burst, per source IP.
//
// Memory is bounded by sweeping idle buckets; a bucket is "idle" once it
// has refilled back to full and hasn't been touched for the sweep
// window, so the map tracks only currently-active clients.
// Safe defaults applied when the operator leaves the auth rate limit
// unconfigured (perMinute <= 0). Previously that disabled throttling
// entirely — an unconfigured deploy invited credential stuffing. We now
// fall back to a conservative non-zero policy so auth endpoints are always
// throttled unless the operator explicitly raises the limit.
const (
	defaultAuthRatePerMin = 30
	defaultAuthRateBurst  = 10
	// Webhook/event surfaces are hit by automated senders (Stripe retries,
	// Slack event bursts, a flow's own webhook callers), so the steady rate
	// is higher than auth — but still bounded so a stranger can't brute-force
	// a per-graph secret or flood the HMAC path. Per source IP.
	// Support writes are authenticated, so these are generous — they exist to
	// stop a runaway client or a bored user from filling the ticket table (and,
	// via flow_id, minting a stored diagnostic bundle per request), not to
	// police normal conversation. Keyed by SUBJECT, not IP: everyone behind one
	// office NAT is a different person.
	defaultSupportRatePerMin = 20
	defaultSupportRateBurst  = 10

	defaultWebhookRatePerMin = 120
	defaultWebhookRateBurst  = 40

	// The runner endpoints are polled, not called: an idle agent asks for work
	// every POLL_SECONDS and that same call is its heartbeat, so the steady
	// rate has to comfortably fit a whole office of agents behind one NAT
	// rather than one caller. It is still bounded, because /claim runs a
	// credential lookup and a locking UPDATE before it can reject a stranger,
	// and an unthrottled loop of those is a cheap way to exhaust the pool.
	// Registration is deliberately NOT on this allowance — it is rare, it
	// opens a transaction against runner_tokens, and it keeps the tighter
	// webhook limiter.
	defaultRunnerRatePerMin = 600
	defaultRunnerRateBurst  = 120
	// maxRateLimiterBuckets bounds the per-IP bucket map. Without a cap a
	// flood of distinct source IPs (or IP rotation) grows the map without
	// bound between GC sweeps. When full we evict the oldest-seen bucket.
	maxRateLimiterBuckets = 50_000
)

type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens added per second
	burst   float64 // bucket capacity
	lastGC  time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewAuthRateLimiter is the exported constructor cmd/dzd uses to build the
// gateway's auth limiter. It always returns a non-nil limiter: an
// unconfigured (perMinute <= 0) deploy falls back to the safe default
// policy rather than disabling throttling.
func NewAuthRateLimiter(perMinute, burst int) *ipRateLimiter {
	return newIPRateLimiter(perMinute, burst)
}

// newIPRateLimiter builds a limiter allowing `perMinute` requests per IP
// in steady state, with a `burst` capacity for short spikes. When
// perMinute <= 0 it applies the safe defaults (defaultAuthRatePerMin /
// defaultAuthRateBurst) instead of disabling — an unconfigured deploy must
// still be throttled.
func newIPRateLimiter(perMinute, burst int) *ipRateLimiter {
	if perMinute <= 0 {
		perMinute = defaultAuthRatePerMin
		if burst <= 0 {
			burst = defaultAuthRateBurst
		}
	}
	if burst <= 0 {
		burst = 1
	}
	return &ipRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		lastGC:  time.Now(),
	}
}

// Allow reports whether a request from ip may proceed, consuming one
// token if so.
func (l *ipRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.gcLocked(now)
	b := l.buckets[ip]
	if b == nil {
		// Bound memory: if the map is full, drop the least-recently-touched
		// bucket to make room. Evicting a stranger's bucket only resets them
		// to a full bucket, so the worst case is a missed throttle for one
		// IP under extreme churn — acceptable versus unbounded growth.
		if len(l.buckets) >= maxRateLimiterBuckets {
			l.evictOldestLocked()
		}
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	// Refill proportional to elapsed time, capped at burst.
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// gcLocked drops buckets that have fully refilled (no outstanding debt)
// and haven't been touched recently — they're indistinguishable from a
// fresh bucket, so forgetting them is free. Runs at most once a minute.
//
// The refill is recomputed here rather than read off the bucket. b.tokens is
// only updated inside Allow, so a bucket abandoned while DEPLETED keeps its
// stale near-zero count forever and would never satisfy a bare
// `b.tokens >= l.burst` test — which selected against exactly the buckets most
// worth expiring, since a scanner or credential-stuffer leaves its bucket
// drained and never returns. Those entries then survived until the map hit
// maxRateLimiterBuckets and evictOldestLocked started paying an O(n) scan per
// insert. Judging on what the bucket WOULD hold now reclaims them on the first
// sweep past their refill time.
func (l *ipRateLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now
	for ip, b := range l.buckets {
		idle := now.Sub(b.last)
		if idle <= time.Minute {
			continue // recently active — keep, debt or not
		}
		if b.tokens+idle.Seconds()*l.rate >= l.burst {
			delete(l.buckets, ip)
		}
	}
}

// evictOldestLocked removes the bucket with the oldest `last` timestamp to
// make room when the map hits its cap. Caller holds s.mu. O(n), but only on
// the rare full-map insert path — the GC sweep keeps the map well below cap
// in normal operation.
func (l *ipRateLimiter) evictOldestLocked() {
	var oldestIP string
	var oldest time.Time
	first := true
	for ip, b := range l.buckets {
		if first || b.last.Before(oldest) {
			oldestIP = ip
			oldest = b.last
			first = false
		}
	}
	if !first {
		delete(l.buckets, oldestIP)
	}
}

// trustedProxies holds the operator-configured set of CIDRs whose
// connections are allowed to assert a client IP via X-Forwarded-For. It
// is parsed once from $DAZYFLOW_TRUSTED_PROXIES (comma-separated CIDRs,
// e.g. "10.0.0.0/8,2001:db8::/32") on first use. When the env var is
// unset/empty the slice is nil and XFF is never honored — behavior is
// then identical to the pre-proxy code (RemoteAddr only). Invalid CIDR
// entries are skipped (a malformed allowlist must never widen trust).
var (
	trustedProxiesOnce sync.Once
	trustedProxies     []*net.IPNet
)

func loadTrustedProxies() {
	raw := os.Getenv("DAZYFLOW_TRUSTED_PROXIES")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Accept a bare IP as a /32 (or /128) as well as a CIDR.
		if !strings.Contains(part, "/") {
			if ip := net.ParseIP(part); ip != nil {
				if ip.To4() != nil {
					part += "/32"
				} else {
					part += "/128"
				}
			}
		}
		if _, ipnet, err := net.ParseCIDR(part); err == nil {
			trustedProxies = append(trustedProxies, ipnet)
		}
	}
}

func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP extracts the source IP for rate-limiting. By default it uses
// the connection's peer address (RemoteAddr), which behind a reverse
// proxy is the proxy's IP — so without further config every client would
// share one bucket behind the TLS ingress.
//
// To fix that an operator sets $DAZYFLOW_TRUSTED_PROXIES to the CIDR(s)
// the ingress connects from. Only when the direct peer (RemoteAddr) is
// within that allowlist do we consult X-Forwarded-For; otherwise XFF is
// ignored entirely (an untrusted peer can forge it freely).
//
// We honor the RIGHTMOST X-Forwarded-For entry that is NOT itself a
// trusted proxy. XFF is appended by each hop, so the rightmost entries
// are the ones our own infrastructure added and can be believed; walking
// right-to-left and stopping at the first non-trusted address yields the
// real client even with a chain of trusted proxies, while a client-
// injected prefix (the leftmost, attacker-controlled entries) is
// discarded. If every listed hop is trusted (or the header is empty) we
// fall back to RemoteAddr.
func clientIP(r *http.Request) string {
	trustedProxiesOnce.Do(loadTrustedProxies)

	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}

	// Only a trusted peer may speak for someone else via XFF.
	if len(trustedProxies) == 0 || !isTrustedProxy(net.ParseIP(host)) {
		return host
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			// A malformed hop breaks the trust chain — stop and use what
			// we have (the peer) rather than trusting anything further left.
			break
		}
		if !isTrustedProxy(ip) {
			return ip.String()
		}
	}
	return host
}

// rateLimitAuth wraps a handler with the gateway's auth rate limiter.
// When the limiter is nil (disabled) it's a pass-through.
func (h *HTTPGateway) rateLimitAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if h.AuthRateLimit != nil && !h.AuthRateLimit.Allow(clientIP(r)) {
			rw.Header().Set("Retry-After", "60")
			writeJSONError(rw, http.StatusTooManyRequests, "rate limit exceeded — slow down")
			return
		}
		next(rw, r)
	}
}

// rateLimitWebhook wraps the unauthenticated inbound surfaces (/trigger and
// the provider event endpoints) with the per-IP webhook limiter, so a
// stranger can't brute-force a per-graph secret or flood the HMAC path. The
// throttle runs BEFORE the handler reads the body or loads any resource.
func (h *HTTPGateway) rateLimitWebhook(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if h.WebhookRateLimit != nil && !h.WebhookRateLimit.Allow(clientIP(r)) {
			rw.Header().Set("Retry-After", "60")
			writeJSONError(rw, http.StatusTooManyRequests, "rate limit exceeded — slow down")
			return
		}
		next(rw, r)
	}
}

// rateLimitRunner wraps the agent's own endpoints with the per-IP runner
// limiter. They sit outside requireAuth — an agent holds a runner credential,
// not a session — and every one of them touches the database before it can
// decide the caller is a stranger, so the throttle runs first.
func (h *HTTPGateway) rateLimitRunner(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if h.RunnerRateLimit != nil && !h.RunnerRateLimit.Allow(clientIP(r)) {
			rw.Header().Set("Retry-After", "60")
			writeJSONError(rw, http.StatusTooManyRequests, "rate limit exceeded — slow down")
			return
		}
		next(rw, r)
	}
}

// allowSupportWrite throttles an authenticated support write (filing a ticket,
// posting a message) per principal subject. Returns false having already written
// the 429, so callers just return.
//
// Ticket creation is the expensive one: naming a flow makes the server build and
// PERSIST a redacted bundle, so an unthrottled loop is a cheap way to grow the
// database. Reads (the queue, a thread poll) are deliberately not limited — the
// UI polls them by design.
func (h *HTTPGateway) allowSupportWrite(rw http.ResponseWriter, p core.Principal) bool {
	if h.SupportRateLimit == nil {
		return true
	}
	key := p.Subject
	if key == "" {
		key = p.Tenant
	}
	if !h.SupportRateLimit.Allow(key) {
		rw.Header().Set("Retry-After", "60")
		writeAPIError(rw, http.StatusTooManyRequests, "rate_limited",
			"you're filing support messages very quickly — wait a moment and try again")
		return false
	}
	return true
}
