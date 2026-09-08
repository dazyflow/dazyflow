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
)

// Per-client-IP token bucket for the unauthenticated surfaces.
const (
	defaultAuthRatePerMin = 30
	defaultAuthRateBurst  = 10
	// Automated senders burst, so these get a higher ceiling than the auth routes.
	defaultSupportRatePerMin = 20
	defaultSupportRateBurst  = 10

	defaultWebhookRatePerMin = 120
	defaultWebhookRateBurst  = 40

	// Polled, not called: an idle agent asks for work continuously.
	defaultRunnerRatePerMin = 600
	defaultRunnerRateBurst  = 120
	// Without a cap the map itself is the memory-exhaustion vector.
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

func NewAuthRateLimiter(perMinute, burst int) *ipRateLimiter {
	return newIPRateLimiter(perMinute, burst)
}

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

func (l *ipRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.gcLocked(now)
	b := l.buckets[ip]
	if b == nil {
		if len(l.buckets) >= maxRateLimiterBuckets {
			l.evictOldestLocked()
		}
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
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

// A fully refilled bucket is indistinguishable from a new one, so it can go.
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

// Evicts rather than refusing, so a flood cannot lock out real callers.
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

// Only these may set X-Forwarded-For: trusting it unconditionally lets any caller
// forge a source IP and evade the limit entirely.
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

// The transport address by default; a forwarded header is honoured ONLY when the
// immediate peer is a configured trusted proxy.
func clientIP(r *http.Request) string {
	trustedProxiesOnce.Do(loadTrustedProxies)

	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}

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
			break
		}
		if !isTrustedProxy(ip) {
			return ip.String()
		}
	}
	return host
}

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
