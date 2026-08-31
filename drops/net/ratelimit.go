// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// Per-(tenant, external host) outbound pacing. On a HOSTED multi-tenant
// deployment the polling/connector fleet shares egress IPs and a single
// third-party API's rate budget across every tenant, so one tenant's burst
// — a 1000-item for_each, a tight poll loop — can exhaust a partner API's
// quota or get the platform's egress IP throttled for everyone. The SSRF
// guard and egress allowlist bound WHERE calls go; this bounds HOW FAST.
//
// Three controls per (tenant, host) bucket:
//   - a token bucket paces steady throughput (rate) with a short burst,
//   - a concurrency cap bounds simultaneous in-flight calls (so a fan-out
//     can't open hundreds of sockets to one host at once), and
//   - a cooldown set from an observed 429/Retry-After / RateLimit-Reset
//     stalls the bucket until the server's window resets.
//
// Because every connector call funnels through Acquire, a fanned step that
// issues one call per item is drip-paced for free — bounding fan-out by
// RATE, not just the engine's blunt item-count cap.
const (
	// Conservative defaults, per (tenant, host). Tuned to PACE bursts, not
	// to match any specific API's ceiling (which we can't know) — a steady
	// few-per-second with a short burst smooths a fan-out without throttling
	// ordinary interactive flows. Operators raise/lower via env (cmd/dzd).
	defaultEgressRatePerMin  = 300 // 5/s steady
	defaultEgressBurst       = 60  // absorbs a modest fan-out before pacing
	defaultEgressConcurrency = 8   // simultaneous in-flight calls per host

	// maxEgressBuckets bounds the bucket map against a flood of distinct
	// (tenant, host) pairs. When full we evict the least-recently-used idle
	// bucket — same policy as the auth limiter.
	maxEgressBuckets = 50_000

	// maxCooldown caps how long a single Retry-After / Reset can stall a
	// host, so a hostile or buggy upstream that returns "Retry-After: 31536000"
	// can't wedge a tenant's calls to that host for a year.
	maxCooldown = time.Hour

	// fallbackCooldown paces a 429/503 that arrives with NO usable header,
	// so even a header-less rate-limit response slows the next call.
	fallbackCooldown = 5 * time.Second

	// concPollInterval is how often Acquire re-checks a full concurrency
	// slot. Releases are not event-signalled (keeping the lock discipline
	// simple); a short poll is fine because token pacing dominates the wait.
	concPollInterval = 25 * time.Millisecond

	// maxAcquireSleep caps a single wait nap so a long token/cooldown wait
	// still re-checks ctx cancellation and a freshly-released slot promptly.
	maxAcquireSleep = 250 * time.Millisecond

	// epochThreshold distinguishes a delta-seconds Reset value from an
	// absolute unix-epoch one (GitHub's X-RateLimit-Reset). Anything past
	// this many seconds can't be a sane delta, so treat it as an epoch.
	epochThreshold = 100_000_000
)

type egressBucket struct {
	tokens   float64
	last     time.Time
	cooldown time.Time // no token dispensed until now >= cooldown
	inflight int
}

type egressLimiter struct {
	mu      sync.Mutex
	buckets map[string]*egressBucket
	rate    float64 // tokens added per second; <=0 disables the limiter
	burst   float64
	conc    int
	lastGC  time.Time
}

// egressLimit is the process-wide limiter every outbound connector call goes
// through. It starts with the safe defaults; cmd/dzd may retune it from env
// at startup, and tests may relax it via SetEgressRateLimit.
var egressLimit = newEgressLimiter(defaultEgressRatePerMin, defaultEgressBurst, defaultEgressConcurrency)

func newEgressLimiter(perMin, burst, conc int) *egressLimiter {
	rate := float64(perMin) / 60.0
	if burst < 1 {
		burst = 1
	}
	if conc < 1 {
		conc = 1
	}
	return &egressLimiter{
		buckets: make(map[string]*egressBucket),
		rate:    rate,
		burst:   float64(burst),
		conc:    conc,
		lastGC:  time.Now(),
	}
}

// SetEgressRateLimit retunes the process-wide outbound limiter. perMin <= 0
// DISABLES pacing entirely (Acquire becomes a pass-through) — used by cmd/dzd
// when an operator opts out, and by tests that fire many calls in a tight
// loop. burst/conc fall back to the safe defaults when non-positive.
func SetEgressRateLimit(perMin, burst, conc int) {
	if burst <= 0 {
		burst = defaultEgressBurst
	}
	if conc <= 0 {
		conc = defaultEgressConcurrency
	}
	l := egressLimit
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate = float64(perMin) / 60.0 // <=0 → disabled (see Acquire)
	l.burst = float64(burst)
	l.conc = conc
	// Drop existing buckets so the new policy applies cleanly and stale
	// cooldowns from the old policy don't linger.
	l.buckets = make(map[string]*egressBucket)
}

// AcquireEgress reserves one outbound slot for the (tenant, host) of rawURL,
// blocking — while honoring ctx — until a token is free, a concurrency slot
// is open, and any active cooldown for that host has elapsed. The returned
// release MUST be called once the call completes (defer it) to free the
// concurrency slot. On ctx cancellation it returns ctx.Err() and a no-op
// release.
//
// tenant is resolved from ctx (core.TenantFromContext); an empty tenant
// shares one bucket per host, which is correct for the in-process / single-
// tenant path.
func AcquireEgress(ctx context.Context, rawURL string) (func(), error) {
	return egressLimit.acquire(ctx, limiterKey(ctx, rawURL))
}

// ObserveEgressResponse records rate-limit signals from a completed outbound
// call so subsequent calls to the same (tenant, host) self-pace:
//   - a 429/503 sets a cooldown from Retry-After / RateLimit-Reset (or a
//     small fallback) AND feeds the delay to the engine's retry scheduler
//     via the ctx RetryHint, so the requeue waits the server-asked interval.
//   - a 2xx that reports the budget is now exhausted (RateLimit-Remaining: 0)
//     proactively cools the host until its window resets, so the NEXT call
//     doesn't earn a 429.
func ObserveEgressResponse(ctx context.Context, rawURL string, status int, header http.Header) {
	egressLimit.observe(ctx, limiterKey(ctx, rawURL), status, header)
}

func limiterKey(ctx context.Context, rawURL string) string {
	tenant, _ := core.TenantFromContext(ctx)
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	return tenant + "|" + host
}

func (l *egressLimiter) acquire(ctx context.Context, key string) (func(), error) {
	for {
		l.mu.Lock()
		if l.rate <= 0 { // disabled: pass through, no concurrency accounting
			l.mu.Unlock()
			return func() {}, nil
		}
		now := time.Now()
		l.gcLocked(now)
		b := l.bucketLocked(key, now)
		b.tokens += now.Sub(b.last).Seconds() * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now

		var wait time.Duration
		switch {
		case now.Before(b.cooldown):
			wait = b.cooldown.Sub(now)
		case b.inflight >= l.conc:
			wait = concPollInterval
		case b.tokens < 1:
			wait = time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
		default:
			b.tokens--
			b.inflight++
			l.mu.Unlock()
			return l.releaseFunc(key), nil
		}
		l.mu.Unlock()

		if wait <= 0 {
			wait = time.Millisecond
		}
		if wait > maxAcquireSleep {
			wait = maxAcquireSleep
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return func() {}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *egressLimiter) releaseFunc(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if b := l.buckets[key]; b != nil && b.inflight > 0 {
				b.inflight--
			}
			l.mu.Unlock()
		})
	}
}

func (l *egressLimiter) observe(ctx context.Context, key string, status int, header http.Header) {
	now := time.Now()
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		d := retryAfter(header, now)
		if d <= 0 {
			d = fallbackCooldown
		}
		l.penalize(key, d, now)
		// Tell the worker's retry scheduler to wait the server-asked interval
		// rather than the blind exponential backoff. No-op when no hint is on
		// ctx (the in-process / non-worker path).
		core.SetRetryAfter(ctx, d)
		return
	}
	if status >= 200 && status < 300 {
		if remaining, ok := headerInt(header, "RateLimit-Remaining", "X-RateLimit-Remaining"); ok && remaining == 0 {
			if d := resetDelay(header, now); d > 0 {
				l.penalize(key, d, now)
			}
		}
	}
}

func (l *egressLimiter) penalize(key string, d time.Duration, now time.Time) {
	if d <= 0 {
		return
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rate <= 0 {
		return
	}
	b := l.bucketLocked(key, now)
	if until := now.Add(d); until.After(b.cooldown) {
		b.cooldown = until
	}
}

// bucketLocked returns (creating, evicting if needed) the bucket for key.
// Caller holds l.mu.
func (l *egressLimiter) bucketLocked(key string, now time.Time) *egressBucket {
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= maxEgressBuckets {
			l.evictOldestLocked()
		}
		b = &egressBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	return b
}

// gcLocked drops buckets that are full, idle, not in cooldown, and have no
// in-flight calls — indistinguishable from a fresh bucket, so forgetting
// them is free. Runs at most once a minute.
func (l *egressLimiter) gcLocked(now time.Time) {
	if now.Sub(l.lastGC) < time.Minute {
		return
	}
	l.lastGC = now
	for k, b := range l.buckets {
		if b.inflight == 0 && b.tokens >= l.burst && now.After(b.cooldown) && now.Sub(b.last) > time.Minute {
			delete(l.buckets, k)
		}
	}
}

// evictOldestLocked removes the least-recently-touched bucket that has no
// in-flight calls, to make room at the cap. Never evicts a bucket with live
// calls (its inflight count is load-bearing). Caller holds l.mu.
func (l *egressLimiter) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	found := false
	for k, b := range l.buckets {
		if b.inflight > 0 {
			continue
		}
		if !found || b.last.Before(oldest) {
			oldestKey, oldest, found = k, b.last, true
		}
	}
	if found {
		delete(l.buckets, oldestKey)
	}
}

// retryAfter derives the server-requested wait from a 429/503 response.
// Retry-After (delta-seconds or HTTP-date) wins; otherwise a RateLimit-Reset
// header. Zero when nothing usable is present. Capped at maxCooldown.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if h == nil {
		return 0
	}
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			return clampCooldown(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(ra); err == nil {
			if d := t.Sub(now); d > 0 {
				return clampCooldown(d)
			}
		}
	}
	return resetDelay(h, now)
}

// resetDelay reads a RateLimit-Reset / X-RateLimit-Reset header, handling
// both the IETF delta-seconds form and the unix-epoch form (GitHub).
func resetDelay(h http.Header, now time.Time) time.Duration {
	v, ok := headerInt(h, "RateLimit-Reset", "X-RateLimit-Reset")
	if !ok || v <= 0 {
		return 0
	}
	if v > epochThreshold { // absolute unix epoch, not a delta
		if d := time.Unix(int64(v), 0).Sub(now); d > 0 {
			return clampCooldown(d)
		}
		return 0
	}
	return clampCooldown(time.Duration(v) * time.Second)
}

func clampCooldown(d time.Duration) time.Duration {
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

// headerInt returns the first of names present as a non-negative integer.
func headerInt(h http.Header, names ...string) (int, bool) {
	if h == nil {
		return 0, false
	}
	for _, name := range names {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				return n, true
			}
		}
	}
	return 0, false
}
