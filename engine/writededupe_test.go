// SPDX-FileCopyrightText: 2026 Angels' Ware
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

// fanDedupeManifest is a non-idempotent write with a typed single-value input,
// so a list wired into it auto-fans (one send per item).
var fanDedupeManifest = core.Manifest{
	ID:           "send_fan",
	Summary:      "Test fixture fanned non-idempotent write.",
	Examples:     []core.ParamsExample{{Title: "default"}},
	Inputs:       []core.Port{{Port: "item", MIME: []string{"application/json"}}},
	Outputs:      []core.Port{{Port: "out"}},
	Idempotent:   false,
	RetryPolicy:  core.RetryNever,
	DedupeWrites: true,
}

// TestWriteDedupe_FannedPartialFailureDoesNotReFire is the regression test for
// the CRITICAL fan-out re-fire bug: a list fans the send node (one send per
// recipient); the first run "sends" items 0..2, then fails on item 3 (a crash /
// transient error mid-fan). On reclaim of the SAME record ID, items 0..2 must
// NOT be sent again — only 3..4. With the old whole-node dedupe key, the first
// run recorded nothing (the node never reached StatusOK), so the reclaim
// re-fired every item, double-sending 0..2.
func TestWriteDedupe_FannedPartialFailureDoesNotReFire(t *testing.T) {
	var sent []string                       // items whose side effect (send) completed
	failFirst := map[string]bool{"d": true} // item "d" (index 3) fails once
	e := newEngineWith(t, NativeDrop{
		Manifest: fanDedupeManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			item, _ := job.Input["item"].Inline.(string)
			if failFirst[item] {
				failFirst[item] = false // a failed send delivers nothing → not recorded
				return core.Result{Status: core.StatusError, Error: &core.JobError{Code: "transient"}}, nil
			}
			sent = append(sent, item)
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"out": {MIME: "text/plain", Inline: item},
			}}, nil
		},
	})
	e.WriteDedupe = NewMemoryWriteDedupe()

	g := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "src", Module: "send_fan"}, {ID: "n", Module: "send_fan"}},
		Edges: []core.Edge{{From: "src", FromPort: "out", To: "n", ToPort: "item"}},
	}
	// The send node's "item" input is the list of recipients, supplied as the
	// upstream "src" result so RunNode assembles it via the edge.
	prior := map[string]core.Result{"src": {Status: core.StatusOK, Output: map[string]core.Ref{
		"out": {Inline: []any{"a", "b", "c", "d", "e"}},
	}}}

	// First run fails fast on "d"; "a","b","c" were sent and recorded per item.
	r1, _ := e.RunNode(context.Background(), g, g.ID, "n", "job-1", prior, nil)
	if r1.Status != core.StatusError {
		t.Fatalf("first run should fail fast on item d, got %s", r1.Status)
	}
	// Reclaim: same record ID re-fans. "a","b","c" are dedupe hits (not re-sent);
	// "d" (now succeeds) and "e" fire.
	r2, _ := e.RunNode(context.Background(), g, g.ID, "n", "job-1", prior, nil)
	if r2.Status != core.StatusOK {
		t.Fatalf("reclaim run should succeed, got %s", r2.Status)
	}

	want := []string{"a", "b", "c", "d", "e"}
	if len(sent) != len(want) {
		t.Fatalf("sent %v, want each item exactly once %v", sent, want)
	}
	seen := map[string]int{}
	for _, s := range sent {
		seen[s]++
	}
	for _, w := range want {
		if seen[w] != 1 {
			t.Errorf("item %q sent %d times, want exactly 1 (re-fire bug)", w, seen[w])
		}
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

// TestMemoryWriteDedupe_GetReturnsIsolatedCopy is the regression test for the
// HIGH aliasing bug: Get must not hand back the stored entry's map. The engine
// mutates a dedupe-hit result in place (ApplyPassthrough writes a "pass" key,
// redactResult rewrites ports), which previously corrupted the stored entry and
// raced other readers.
func TestMemoryWriteDedupe_GetReturnsIsolatedCopy(t *testing.T) {
	d := NewMemoryWriteDedupe()
	ctx := context.Background()
	d.Put(ctx, "k", core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
		"out": {Inline: "original"},
	}})

	got, ok := d.Get(ctx, "k")
	if !ok {
		t.Fatal("Get miss after Put")
	}
	// Mutate the returned result the way the engine does post-hit.
	got.Output["out"] = core.Ref{Inline: "mutated"}
	got.Output["pass"] = core.Ref{Inline: "injected"}

	again, _ := d.Get(ctx, "k")
	if again.Output["out"].Inline != "original" {
		t.Errorf("stored entry corrupted: out=%v, want original", again.Output["out"].Inline)
	}
	if _, leaked := again.Output["pass"]; leaked {
		t.Error("caller's injected key leaked into the stored entry")
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
