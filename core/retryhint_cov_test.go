// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"testing"
	"time"
)

func TestRetryHint_NilReceiver(t *testing.T) {
	var h *RetryHint
	h.set(5 * time.Second) // must not panic
	if got := h.After(); got != 0 {
		t.Errorf("nil hint After = %v, want 0", got)
	}
}

func TestRetryHint_KeepsLongest(t *testing.T) {
	h := &RetryHint{}
	h.set(2 * time.Second)
	h.set(10 * time.Second)
	h.set(3 * time.Second)
	if got := h.After(); got != 10*time.Second {
		t.Errorf("After = %v, want 10s (longest wins)", got)
	}
	// Non-positive durations are ignored.
	h.set(0)
	h.set(-1 * time.Second)
	if got := h.After(); got != 10*time.Second {
		t.Errorf("After after no-op sets = %v, want 10s", got)
	}
}

func TestWithRetryHint_RoundTrip(t *testing.T) {
	ctx, h := WithRetryHint(context.Background())
	if h == nil {
		t.Fatal("WithRetryHint returned nil hint")
	}
	SetRetryAfter(ctx, 7*time.Second)
	if got := h.After(); got != 7*time.Second {
		t.Errorf("After = %v, want 7s", got)
	}
}

func TestSetRetryAfter_NoHintAttached(t *testing.T) {
	// No hint on the context — must be a silent no-op (no panic).
	SetRetryAfter(context.Background(), 5*time.Second)
}
