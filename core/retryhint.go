// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"sync"
	"time"
)

type retryHintKey struct{}

// Carries a Retry-After observed at the outbound HTTP choke point up to the
// worker's retry scheduler, so a third-party 429 delays the requeue by the
// interval the server asked for rather than by blind backoff. A node may issue
// several outbound calls concurrently, so writes are guarded and the longest wins.
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
	if d > h.after {
		h.after = d
	}
}

func (h *RetryHint) After() time.Duration {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.after
}

func WithRetryHint(ctx context.Context) (context.Context, *RetryHint) {
	h := &RetryHint{}
	return context.WithValue(ctx, retryHintKey{}, h), h
}

func SetRetryAfter(ctx context.Context, d time.Duration) {
	if h, ok := ctx.Value(retryHintKey{}).(*RetryHint); ok {
		h.set(d)
	}
}
