// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

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

// The regression test for the CRITICAL fan-out re-fire bug: a list fans the
// send node (one send per recipient); the first run "sends" items 0..2, then
// fails on item 3 (a crash / transient error mid-fan). On reclaim of the SAME
// record ID, items 0..2 must NOT be sent again — only 3..4. With the old
// whole-node dedupe key, the first run recorded nothing (the node never
// reached StatusOK), so the reclaim re-fired every item, double-sending 0..2.
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
	prior := map[string]core.Result{"src": {Status: core.StatusOK, Output: map[string]core.Ref{
		"out": {Inline: []any{"a", "b", "c", "d", "e"}},
	}}}

	r1, _ := e.RunNode(context.Background(), g, g.ID, "n", "job-1", prior, nil)
	if r1.Status != core.StatusError {
		t.Fatalf("first run should fail fast on item d, got %s", r1.Status)
	}
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

// The regression test for the HIGH aliasing bug: Get must not hand back the
// stored entry's map. The engine mutates a dedupe-hit result in place
// (ApplyPassthrough writes a "pass" key, redactResult rewrites ports), which
// previously corrupted the stored entry and raced other readers.
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

// The TTL is a strict ceiling: an entry aged exactly writeDedupeTTL is
// still a hit. Expiring one tick early would let a reclaim re-fire a
// write that was in fact recorded.
func TestMemoryWriteDedupe_ExactTTLAgeIsStillAHit(t *testing.T) {
	d := NewMemoryWriteDedupe().(*memoryWriteDedupe)
	clock := time.Unix(1_000_000, 0)
	d.now = func() time.Time { return clock }
	ctx := context.Background()

	d.Put(ctx, "k", core.Result{Status: core.StatusOK})

	clock = clock.Add(writeDedupeTTL) // exactly at the TTL, not past it
	if _, ok := d.Get(ctx, "k"); !ok {
		t.Error("entry aged exactly the TTL was treated as stale")
	}
	clock = clock.Add(time.Nanosecond) // one tick past
	if _, ok := d.Get(ctx, "k"); ok {
		t.Error("entry one tick past the TTL should be stale")
	}
}

// Put stores a deep copy, so a mutation of the map the caller still holds
// cannot reach back into the stored entry. Get clones on the way out too,
// which is what makes the entry fully isolated — but Get's clone alone
// cannot protect against a write through the caller's own reference.
func TestMemoryWriteDedupe_PutStoresIsolatedCopy(t *testing.T) {
	d := NewMemoryWriteDedupe()
	ctx := context.Background()
	out := map[string]core.Ref{"out": {Inline: "original"}}
	d.Put(ctx, "k", core.Result{Status: core.StatusOK, Output: out})

	out["out"] = core.Ref{Inline: "mutated"}
	out["injected"] = core.Ref{Inline: "x"}

	got, ok := d.Get(ctx, "k")
	if !ok {
		t.Fatal("Get miss after Put")
	}
	if got.Output["out"].Inline != "original" {
		t.Errorf("stored output = %v, want original: the caller's map was aliased", got.Output["out"].Inline)
	}
	if _, leaked := got.Output["injected"]; leaked {
		t.Error("the caller's later insertion reached the stored entry")
	}
}

// The cap is inclusive: exactly writeDedupeMaxItems entries fit. Evicting
// at the cap rather than past it would discard a live record, and with it
// the protection against a re-fired write.
func TestMemoryWriteDedupe_CapKeepsExactlyMaxItems(t *testing.T) {
	d := NewMemoryWriteDedupe().(*memoryWriteDedupe)
	ctx := context.Background()
	for i := range writeDedupeMaxItems {
		d.Put(ctx, "k-"+strconv.Itoa(i), core.Result{Status: core.StatusOK})
	}

	d.mu.Lock()
	n := len(d.entries)
	d.mu.Unlock()
	if n != writeDedupeMaxItems {
		t.Errorf("entries = %d, want %d (nothing evicted while at the cap)", n, writeDedupeMaxItems)
	}
	if _, ok := d.Get(ctx, "k-0"); !ok {
		t.Error("oldest entry evicted while still exactly at the cap")
	}
}

// A stale Get drops the key from the FIFO order list as well as the map.
// Removing the wrong key would leave a dead name in the order and lose a
// live entry's place, so the cap would later evict the wrong record.
func TestMemoryWriteDedupe_StaleGetRemovesOnlyThatKeyFromOrder(t *testing.T) {
	d := NewMemoryWriteDedupe().(*memoryWriteDedupe)
	clock := time.Unix(1_000_000, 0)
	d.now = func() time.Time { return clock }
	ctx := context.Background()

	d.Put(ctx, "a", core.Result{Status: core.StatusOK})
	clock = clock.Add(time.Minute)
	d.Put(ctx, "b", core.Result{Status: core.StatusOK})

	clock = clock.Add(writeDedupeTTL)
	if _, ok := d.Get(ctx, "a"); ok {
		t.Fatal("a should be stale")
	}

	d.mu.Lock()
	order := slices.Clone(d.order)
	d.mu.Unlock()
	if !slices.Equal(order, []string{"b"}) {
		t.Errorf("order = %v, want [b]: the stale read removed the wrong key", order)
	}
	if _, ok := d.Get(ctx, "b"); !ok {
		t.Error("b should still be present")
	}
}

func TestWriteDedupe_KeysAreJobIDWithAscendingFanIndex(t *testing.T) {
	e := newEngineWith(t, NativeDrop{
		Manifest: fanDedupeManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			item, _ := job.Input["item"].Inline.(string)

			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"out": {MIME: "text/plain", Inline: item},
			}}, nil
		},
	})
	d := NewMemoryWriteDedupe().(*memoryWriteDedupe)
	e.WriteDedupe = d

	g := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "src", Module: "send_fan"}, {ID: "n", Module: "send_fan"}},
		Edges: []core.Edge{{From: "src", FromPort: "out", To: "n", ToPort: "item"}},
	}
	prior := map[string]core.Result{"src": {Status: core.StatusOK, Output: map[string]core.Ref{
		"out": {Inline: []any{"a", "b", "c"}},
	}}}

	if r, _ := e.RunNode(context.Background(), g, g.ID, "n", "job-1", prior, nil); r.Status != core.StatusOK {
		t.Fatalf("run status = %s, want ok", r.Status)
	}

	d.mu.Lock()
	keys := make([]string, 0, len(d.entries))
	for k := range d.entries {
		keys = append(keys, k)
	}
	d.mu.Unlock()
	slices.Sort(keys)

	if want := []string{"job-1#0", "job-1#1", "job-1#2"}; !slices.Equal(keys, want) {
		t.Errorf("dedupe keys = %v, want %v", keys, want)
	}
}
