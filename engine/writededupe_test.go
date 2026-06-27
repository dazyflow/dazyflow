// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// dedupeManifest is a non-idempotent write that opts into engine dedupe.
var dedupeManifest = core.Manifest{
	ID:           "send_thing",
	Summary:      "Test fixture non-idempotent write.",
	Examples:     []core.ParamsExample{{Title: "default"}},
	Outputs:      []core.Port{{Port: "out"}},
	Idempotent:   false,
	RetryPolicy:  core.RetryNever,
	DedupeWrites: true,
}

func dedupeGraph() core.Graph {
	return core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "send_thing"}},
	}
}

func TestWriteDedupe_SecondRunOfSameJobSkipsExecute(t *testing.T) {
	var calls atomic.Int32
	e := newEngineWith(t, NativeDrop{
		Manifest: dedupeManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			calls.Add(1)
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"out": {MIME: "text/plain", Inline: "sent"},
			}}, nil
		},
	})
	e.WriteDedupe = NewMemoryWriteDedupe()
	g := dedupeGraph()

	r1, err := e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	if err != nil || r1.Status != core.StatusOK {
		t.Fatalf("first run: status=%s err=%v", r1.Status, err)
	}
	// Same job ID re-executes (an expired-lease reclaim): must NOT call Execute
	// again, and must return the recorded result.
	r2, err := e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	if err != nil {
		t.Fatalf("second run err: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Execute called %d times, want 1 (dedupe)", got)
	}
	if r2.Output["out"].Inline != "sent" {
		t.Fatalf("deduped result not replayed: %+v", r2.Output)
	}
}

func TestWriteDedupe_DistinctJobIDsBothRun(t *testing.T) {
	var calls atomic.Int32
	e := newEngineWith(t, NativeDrop{
		Manifest: dedupeManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			calls.Add(1)
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	e.WriteDedupe = NewMemoryWriteDedupe()
	g := dedupeGraph()

	_, _ = e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	_, _ = e.RunNode(context.Background(), g, g.ID, "n", "job-2", nil, nil)
	if got := calls.Load(); got != 2 {
		t.Fatalf("Execute called %d times, want 2 (distinct jobs)", got)
	}
}

func TestWriteDedupe_FailureNotRecorded(t *testing.T) {
	var calls atomic.Int32
	e := newEngineWith(t, NativeDrop{
		Manifest: dedupeManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			calls.Add(1)
			// First attempt fails; a re-run must be allowed to actually retry
			// the side effect (a failed write was never delivered).
			if calls.Load() == 1 {
				return core.Result{Status: core.StatusError, Error: &core.JobError{Code: "boom"}}, nil
			}
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	e.WriteDedupe = NewMemoryWriteDedupe()
	g := dedupeGraph()

	r1, _ := e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	if r1.Status != core.StatusError {
		t.Fatalf("first run should fail, got %s", r1.Status)
	}
	r2, _ := e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	if r2.Status != core.StatusOK {
		t.Fatalf("re-run after failure should execute and succeed, got %s", r2.Status)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Execute called %d times, want 2 (failure not deduped)", got)
	}
}

func TestWriteDedupe_DisabledWithoutStore(t *testing.T) {
	var calls atomic.Int32
	e := newEngineWith(t, NativeDrop{
		Manifest: dedupeManifest,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			calls.Add(1)
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	// No WriteDedupe store wired → every run executes.
	g := dedupeGraph()
	_, _ = e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	_, _ = e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	if got := calls.Load(); got != 2 {
		t.Fatalf("Execute called %d times, want 2 (no store = no dedupe)", got)
	}
}

func TestWriteDedupe_NonOptedInModuleNotDeduped(t *testing.T) {
	var calls atomic.Int32
	m := dedupeManifest
	m.DedupeWrites = false // opt out
	e := newEngineWith(t, NativeDrop{
		Manifest: m,
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			calls.Add(1)
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	e.WriteDedupe = NewMemoryWriteDedupe()
	g := dedupeGraph()
	_, _ = e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	_, _ = e.RunNode(context.Background(), g, g.ID, "n", "job-1", nil, nil)
	if got := calls.Load(); got != 2 {
		t.Fatalf("Execute called %d times, want 2 (module not opted in)", got)
	}
}

func TestMemoryWriteDedupe_TTLExpiry(t *testing.T) {
	d := &memoryWriteDedupe{entries: map[string]dedupeEntry{}, now: time.Now}
	clock := time.Now()
	d.now = func() time.Time { return clock }

	d.Put(context.Background(), "k", core.Result{Status: core.StatusOK})
	if _, ok := d.Get(context.Background(), "k"); !ok {
		t.Fatal("entry should be present immediately")
	}
	clock = clock.Add(writeDedupeTTL + time.Minute)
	if _, ok := d.Get(context.Background(), "k"); ok {
		t.Fatal("entry should be expired past TTL")
	}
}

func TestMemoryWriteDedupe_CapEviction(t *testing.T) {
	d := NewMemoryWriteDedupe().(*memoryWriteDedupe)
	// Fill past the cap; the oldest should be evicted (FIFO).
	for i := range writeDedupeMaxItems + 5 {
		d.Put(context.Background(), "k-"+strconv.Itoa(i), core.Result{Status: core.StatusOK})
	}
	if _, ok := d.Get(context.Background(), "k-0"); ok {
		t.Fatal("oldest entry should have been evicted at the cap")
	}
	if _, ok := d.Get(context.Background(), "k-"+strconv.Itoa(writeDedupeMaxItems+4)); !ok {
		t.Fatal("newest entry should be retained")
	}
}
