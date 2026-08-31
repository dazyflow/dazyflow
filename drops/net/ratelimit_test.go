// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// newTestLimiter builds an isolated limiter so tests don't fight the
// package-global egressLimit or each other.
func newTestLimiter(perMin, burst, conc int) *egressLimiter {
	return newEgressLimiter(perMin, burst, conc)
}

func TestAcquireConsumesBurstThenPaces(t *testing.T) {
	// 60/min = 1/s refill, burst 3: three immediate acquires, the fourth waits.
	l := newTestLimiter(60, 3, 10)
	ctx := context.Background()
	for i := range 3 {
		rel, err := l.acquire(ctx, "t|api.example.com")
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		rel()
	}
	start := time.Now()
	rel, err := l.acquire(ctx, "t|api.example.com")
	if err != nil {
		t.Fatalf("fourth acquire: %v", err)
	}
	rel()
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("fourth acquire should have paced ~1s, waited %v", waited)
	}
}

func TestAcquireRespectsContextCancel(t *testing.T) {
	// Drain the single burst token, then a cancelled ctx must abort the wait.
	l := newTestLimiter(1, 1, 10) // ~1/min refill: the next token is far off
	rel, err := l.acquire(context.Background(), "t|h")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer rel()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := l.acquire(ctx, "t|h"); err == nil {
		t.Fatal("expected context error when no token available")
	}
	if time.Since(start) > time.Second {
		t.Fatal("acquire did not honor ctx promptly")
	}
}

func TestConcurrencyCap(t *testing.T) {
	// burst high so tokens never gate; conc 2 should block the third acquire.
	l := newTestLimiter(6000, 100, 2)
	ctx := context.Background()
	r1, _ := l.acquire(ctx, "t|h")
	r2, _ := l.acquire(ctx, "t|h")

	cctx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(cctx, "t|h"); err == nil {
		t.Fatal("third concurrent acquire should block on the conc cap")
	}
	r1()
	// A freed slot lets the next acquire through.
	r3, err := l.acquire(ctx, "t|h")
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	r3()
	r2()
}

func TestDisabledLimiterPassesThrough(t *testing.T) {
	l := newTestLimiter(0, 1, 1) // rate <= 0 disables
	for range 100 {
		rel, err := l.acquire(context.Background(), "t|h")
		if err != nil {
			t.Fatalf("disabled limiter should never block/err: %v", err)
		}
		rel()
	}
}

func Test429SetsCooldownAndRetryHint(t *testing.T) {
	l := newTestLimiter(6000, 100, 10) // tokens/conc never gate; isolate cooldown
	ctx, hint := core.WithRetryHint(context.Background())
	h := http.Header{}
	h.Set("Retry-After", "1")
	l.observe(ctx, "t|h", http.StatusTooManyRequests, h)

	if got := hint.After(); got != time.Second {
		t.Fatalf("retry hint = %v, want 1s", got)
	}
	// The next acquire must wait out the ~1s cooldown.
	start := time.Now()
	rel, err := l.acquire(ctx, "t|h")
	if err != nil {
		t.Fatalf("acquire after 429: %v", err)
	}
	rel()
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("expected ~1s cooldown wait, got %v", waited)
	}
}

func TestRemainingZeroCoolsProactively(t *testing.T) {
	l := newTestLimiter(6000, 100, 10)
	ctx := context.Background()
	h := http.Header{}
	h.Set("RateLimit-Remaining", "0")
	h.Set("RateLimit-Reset", "1")
	l.observe(ctx, "t|h", http.StatusOK, h)

	start := time.Now()
	rel, _ := l.acquire(ctx, "t|h")
	rel()
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("remaining=0 should cool host ~1s, waited %v", waited)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		set  func(http.Header)
		want time.Duration
	}{
		{"delta-seconds", func(h http.Header) { h.Set("Retry-After", "30") }, 30 * time.Second},
		{"http-date", func(h http.Header) { h.Set("Retry-After", now.Add(45*time.Second).UTC().Format(http.TimeFormat)) }, 45 * time.Second},
		{"ratelimit-reset-delta", func(h http.Header) { h.Set("RateLimit-Reset", "12") }, 12 * time.Second},
		{"x-ratelimit-reset-epoch", func(h http.Header) { h.Set("X-RateLimit-Reset", "1782388820") }, time.Unix(1782388820, 0).Sub(now)},
		{"over-cap", func(h http.Header) { h.Set("Retry-After", "999999999") }, maxCooldown},
		{"none", func(h http.Header) {}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			c.set(h)
			if got := retryAfter(h, now); got != c.want {
				t.Fatalf("retryAfter = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPenalizeCappedAtMaxCooldown(t *testing.T) {
	l := newTestLimiter(6000, 100, 10)
	now := time.Now()
	l.penalize("t|h", 10*time.Hour, now)
	l.mu.Lock()
	b := l.buckets["t|h"]
	cooldown := b.cooldown
	l.mu.Unlock()
	if cooldown.After(now.Add(maxCooldown + time.Minute)) {
		t.Fatalf("cooldown not capped: %v", cooldown.Sub(now))
	}
}

func TestPerKeyIsolation(t *testing.T) {
	// A cooldown on one (tenant, host) must not stall another.
	l := newTestLimiter(6000, 100, 10)
	ctx := context.Background()
	h := http.Header{}
	h.Set("Retry-After", "5")
	l.observe(ctx, "tA|h", http.StatusTooManyRequests, h)

	start := time.Now()
	rel, err := l.acquire(ctx, "tB|h")
	if err != nil {
		t.Fatalf("acquire other key: %v", err)
	}
	rel()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("cooldown on tA leaked into tB")
	}
}
