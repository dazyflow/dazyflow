// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestDelay_Duration(t *testing.T) {
	start := time.Now()
	res, err := executeDelay(t.Context(), core.Job{
		Params: map[string]any{"ms": 100},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err=%+v", res.Status, res.Error)
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Errorf("slept only %v, want ≥100ms", elapsed)
	}
}

// A wait short enough to serve inline still honors cancellation. (A longer one
// never blocks at all — it defers, and a cancelled RUN completes its queued
// node records; see TestDelay_DefersLongWaits.)
func TestDelay_Cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := executeDelay(ctx, core.Job{
		Params: map[string]any{"ms": maxInlineDelay.Milliseconds()},
	}, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("sleep did not honor cancellation; elapsed=%v", time.Since(start))
	}
}

func TestDelay_BadParam(t *testing.T) {
	res, _ := executeDelay(t.Context(), core.Job{
		Params: map[string]any{},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
}

func TestDelay_MsFromInput(t *testing.T) {
	// The wired `ms` input overrides the param: the param asks for 5s but
	// the input (a JSON-decoded number, so float64) says 10ms, so the node
	// must return fast — proving the input wins.
	start := time.Now()
	res, err := executeDelay(t.Context(), core.Job{
		Params: map[string]any{"ms": 5000},
		Input:  map[string]core.Ref{"ms": {Inline: float64(10)}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err=%+v", res.Status, res.Error)
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("input ms ignored; elapsed=%v, expected ~10ms", elapsed)
	}
}

func TestDelay_Passthrough(t *testing.T) {
	// A value wired into the universal pass pin is forwarded on pass out.
	res, err := executeDelay(t.Context(), core.Job{
		Params: map[string]any{"ms": 10},
		Input:  map[string]core.Ref{core.PassPort: {Ref: "x", MIME: "text/plain"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Output[core.PassPort].Ref != "x" {
		t.Errorf("passthrough failed: %+v", res.Output)
	}
}

func TestDelay_EmitsControlSignalOnEmpty(t *testing.T) {
	// Pure pause: nothing threaded in, but pass out still carries a control
	// signal so a downstream node wired to it still fires.
	res, err := executeDelay(t.Context(), core.Job{
		Params: map[string]any{"ms": 10},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	ref, ok := res.Output[core.PassPort]
	if !ok {
		t.Fatalf("no pass output on empty-input delay: %+v", res.Output)
	}
	if ref.MIME != "application/x-control" {
		t.Errorf("pass MIME = %q, want application/x-control", ref.MIME)
	}
}

// Above ~9.2e12 ms the nanosecond conversion overflows int64 and the timer
// fires at once, so "wait 292 years" silently became "don't wait". Both the
// overflowing values and the merely absurd ones are refused instead.
func TestDelay_RejectsAbsurdDurations(t *testing.T) {
	for _, ms := range []int64{maxDelayMs + 1, 9223372036854775, 1 << 62} {
		res, err := executeDelay(t.Context(), core.Job{
			Params: map[string]any{"ms": ms},
		}, nil)
		if err != nil {
			t.Fatalf("ms=%d: execute: %v", ms, err)
		}
		if res.Status != core.StatusError {
			t.Errorf("ms=%d: status = %q, want an error rather than an instant success", ms, res.Status)
		}
	}
}

// deferrableCtx is a context that looks like a worker's: a job record backs the
// step, so it may hand its slot back. Without one a wait runs inline.
func deferrableCtx(t *testing.T) context.Context {
	t.Helper()
	return core.WithNodeEnqueuedAt(t.Context(), time.Now())
}

// The cap itself is still accepted — it is a ceiling, not a rejection of long
// waits. A wait that long is deferred rather than served, which is the whole
// point: a year of waiting must not sit on a worker.
func TestDelay_AcceptsTheCap(t *testing.T) {
	res, err := executeDelay(deferrableCtx(t), core.Job{Params: map[string]any{"ms": maxDelayMs}}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	at, ok := core.ResumeAt(res)
	if !ok {
		t.Fatalf("status=%q, want a deferral carrying a resume time", res.Status)
	}
	if d := time.Until(at); d < 300*24*time.Hour {
		t.Errorf("resume_at is %v away, want about a year", d)
	}
}

// A wait past the inline threshold hands the worker slot back instead of
// sleeping on it: the pool is serial and small, so a step that only waits used
// to be able to stop every tenant's runs for as long as its author typed.
func TestDelay_DefersLongWaits(t *testing.T) {
	start := time.Now()
	res, err := executeDelay(deferrableCtx(t), core.Job{
		Params: map[string]any{"ms": 30_000},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("a deferred wait blocked for %v", elapsed)
	}
	at, ok := core.ResumeAt(res)
	if !ok {
		t.Fatalf("status=%q, want %q", res.Status, core.StatusDeferred)
	}
	if d := time.Until(at); d < 25*time.Second || d > 31*time.Second {
		t.Errorf("resume_at is %v away, want ~30s", d)
	}
}

// The deadline is anchored on when the step became due, not on when the
// attempt started — so the re-execution after the horizon passes finds no time
// left and completes, instead of restarting the wait on every hop.
func TestDelay_ResumesFromTheEnqueueAnchor(t *testing.T) {
	ctx := core.WithNodeEnqueuedAt(t.Context(), time.Now().Add(-time.Hour))
	res, err := executeDelay(ctx, core.Job{
		Params: map[string]any{"ms": 30_000},
		Input:  map[string]core.Ref{"pass": {MIME: "text/plain", Inline: "x"}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok — the wait was already over", res.Status)
	}
	if _, deferred := core.ResumeAt(res); deferred {
		t.Errorf("a wait whose horizon has passed deferred again")
	}
}

// Deferring is not executing, so the context deadline no longer bounds the
// wait — but a timeout the AUTHOR set still does, and a wait that cannot fit
// inside it is refused at once rather than after sleeping to find out.
func TestDelay_DeclaredTimeoutStillBinds(t *testing.T) {
	ctx := core.WithNodeTimeout(deferrableCtx(t), time.Second)
	res, err := executeDelay(ctx, core.Job{Params: map[string]any{"ms": 30_000}}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "timeout" {
		t.Errorf("status=%q err=%+v, want a timeout", res.Status, res.Error)
	}
	// A wait that fits inside the declared timeout still defers normally.
	ctx = core.WithNodeTimeout(deferrableCtx(t), time.Hour)
	res, _ = executeDelay(ctx, core.Job{Params: map[string]any{"ms": 30_000}}, nil)
	if _, ok := core.ResumeAt(res); !ok {
		t.Errorf("a wait inside its timeout did not defer: status=%q", res.Status)
	}
}

// Engine.Run executes a for_each body on the worker's context but has no queue
// to requeue into, and reads any status but "error" as success — so a body step
// that deferred would report done without ever waiting. The worker clears the
// anchor there; with none, a wait runs inline however long it is.
func TestDelay_WithoutARecordWaitsInline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	res, _ := executeDelay(ctx, core.Job{Params: map[string]any{"ms": 30_000}}, nil)
	if _, deferred := core.ResumeAt(res); deferred {
		t.Fatalf("deferred with no job record behind the step")
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "cancelled" {
		t.Errorf("status=%q err=%+v, want the wait to have been in progress", res.Status, res.Error)
	}
}
