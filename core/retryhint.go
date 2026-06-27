// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"time"
)

type retryHintKey struct{}

// RetryHint carries a server-requested retry delay (an HTTP Retry-After /
// RateLimit-Reset observed by the outbound HTTP choke point) up to the
// worker's retry scheduler, so a 429 from a third-party API can delay the
// requeue by the interval the server actually asked for instead of the
// blind exponential backoff. It is attached to the node's execution context
// by the worker and written by the net package; connectors need no changes.
//
// Concurrency: a node may issue several outbound calls (including from a
// fanned/loop body running in parallel goroutines), so writes are guarded
// and the longest hint seen during the attempt wins.
type RetryHint struct {
	mu    sync.Mutex
	after time.Duration
}

func (h *RetryHint) set(d time.Duration) {
	if h == nil || d <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Keep the LONGEST hint: a node that touched several hosts should wait
	// for the most-constrained one before retrying.
	if d > h.after {
		h.after = d
	}
}

// After returns the longest server-requested retry delay recorded during
// the attempt, or zero if none. Safe on a nil receiver.
func (h *RetryHint) After() time.Duration {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.after
}

// WithRetryHint attaches a fresh RetryHint to ctx and returns both. The
// worker calls this before executing a node and reads hint.After() after,
// using it to lengthen the retry backoff when a downstream API asked for a
// specific wait.
func WithRetryHint(ctx context.Context) (context.Context, *RetryHint) {
	h := &RetryHint{}
	return context.WithValue(ctx, retryHintKey{}, h), h
}

// SetRetryAfter records a server-requested retry delay on the hint carried
// by ctx, if any. A no-op when no hint is attached (e.g. test / in-process
// calls), so the choke point can call it unconditionally.
func SetRetryAfter(ctx context.Context, d time.Duration) {
	if h, ok := ctx.Value(retryHintKey{}).(*RetryHint); ok {
		h.set(d)
	}
}
