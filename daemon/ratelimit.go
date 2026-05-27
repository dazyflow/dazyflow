package daemon

import (
	"net"
	"net/http"
	"sync"
	"time"
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

// NewAuthRateLimiter is the exported constructor cmd/hzd uses to build
// the gateway's auth limiter. Returns nil (limiter disabled) when
// perMinute <= 0.
func NewAuthRateLimiter(perMinute, burst int) *ipRateLimiter {
	return newIPRateLimiter(perMinute, burst)
}

// newIPRateLimiter builds a limiter allowing `perMinute` requests per IP
// in steady state, with a `burst` capacity for short spikes. Returns nil
// when perMinute <= 0 (caller treats nil as "disabled").
func newIPRateLimiter(perMinute, burst int) *ipRateLimiter {
	if perMinute <= 0 {
		return nil
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
func (l *ipRateLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now
	for ip, b := range l.buckets {
		if b.tokens >= l.burst && now.Sub(b.last) > time.Minute {
			delete(l.buckets, ip)
		}
	}
}

// clientIP extracts the source IP for rate-limiting. Uses RemoteAddr —
// behind a reverse proxy that's the proxy's IP unless the proxy sets
// RemoteAddr via PROXY protocol. (Honoring X-Forwarded-For is a
// follow-up; it's spoofable without a trusted-proxy allowlist, so the
// conservative default is the connection's peer address.)
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
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
