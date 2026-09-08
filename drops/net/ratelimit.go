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

// On a hosted instance one tenant's flow can otherwise exhaust a third party's
// rate limit for everyone, so pacing is per (tenant, host).
const (
	// Tuned to PACE a burst, not to throttle ordinary use.
	defaultEgressRatePerMin  = 300 // 5/s steady
	defaultEgressBurst       = 60  // absorbs a modest fan-out before pacing
	defaultEgressConcurrency = 8   // simultaneous in-flight calls per host

	// The map itself is the memory-exhaustion vector.
	maxEgressBuckets = 50_000

	// A hostile or broken server must not be able to stall a tenant indefinitely.
	maxCooldown = time.Hour

	fallbackCooldown = 5 * time.Second

	// How often a full slot set is re-checked.
	concPollInterval = 25 * time.Millisecond

	maxAcquireSleep = 250 * time.Millisecond

	// Servers send Reset either way, with nothing to distinguish them but size.
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
	l.buckets = make(map[string]*egressBucket)
}

// The caller MUST release, or the slot leaks for the process's life.
func AcquireEgress(ctx context.Context, rawURL string) (func(), error) {
	return egressLimit.acquire(ctx, limiterKey(ctx, rawURL))
}

// A 429 here is what sets the cooldown the NEXT call waits out.
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
		// So the retry happens one layer up, where it costs no worker time.
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

// Never evicts a bucket with work in flight.
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

// Accepts both the seconds and the HTTP-date forms.
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
