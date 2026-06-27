// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// for_each requires a BodyRunner on the context — the daemon supplies the real
// one that runs the wired body subgraph once per item. These unit tests stub
// it to exercise the fan-out skeleton (ordering, concurrency, error
// collection) in isolation; the real body execution is covered by the daemon's
// loopbody e2e tests.

// withRunner attaches a stub body runner to a fresh context.
func withRunner(fn engine.BodyRunner) context.Context {
	return engine.WithBodyRunner(context.Background(), fn)
}

// echoRunner emits the current item on body node "body", port "out" — mirroring
// a one-node loop body. The results wrapper is then {status, nodes:{body:{output:{out}}}}.
func echoRunner(_ context.Context, item core.Ref) (engine.GraphResult, error) {
	return engine.GraphResult{
		Status: core.StatusOK,
		Nodes: map[string]core.Result{
			"body": {Status: core.StatusOK, Output: map[string]core.Ref{"out": item}},
		},
	}, nil
}

// bodyOut reads body node "body" port "out" out of a results-wrapper Ref.
func bodyOut(t *testing.T, r core.Ref) string {
	t.Helper()
	payload, _ := r.Inline.(map[string]any)
	nodes, _ := payload["nodes"].(map[string]any)
	body, _ := nodes["body"].(map[string]any)
	out, _ := body["output"].(map[string]core.Ref)
	s, _ := out["out"].Inline.(string)
	return s
}

func TestForEach_RunsBodyPerItemInOrder(t *testing.T) {
	job := core.Job{
		ID:    "fe",
		Input: map[string]core.Ref{"items": {Inline: []any{"alpha", "beta", "gamma"}}},
	}
	res, err := executeForEach(withRunner(echoRunner), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	results, ok := res.Output["results"].Inline.([]core.Ref)
	if !ok || len(results) != 3 {
		t.Fatalf("results = %T len=%d, want 3 Refs", res.Output["results"].Inline, len(results))
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if got := bodyOut(t, results[i]); got != want {
			t.Errorf("results[%d] = %q, want %q", i, got, want)
		}
	}
	if errs, _ := res.Output["errors"].Inline.([]any); len(errs) != 0 {
		t.Errorf("errors = %+v, want empty", errs)
	}
}

// failRunner errors on the item "bad", echoes everything else.
func failRunner(ctx context.Context, item core.Ref) (engine.GraphResult, error) {
	if s, _ := item.Inline.(string); s == "bad" {
		return engine.GraphResult{Status: core.StatusError, Error: &core.JobError{Code: "boom", Message: s}}, nil
	}
	return echoRunner(ctx, item)
}

func TestForEach_CollectsPerItemErrorsWithoutFailFast(t *testing.T) {
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: []any{"good", "bad", "also_good"}}},
	}
	res, err := executeForEach(withRunner(failRunner), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok (fail_fast=false)", res.Status)
	}
	errs, _ := res.Output["errors"].Inline.([]any)
	if len(errs) != 1 {
		t.Fatalf("errors = %+v, want exactly one failed row", errs)
	}
	entry, ok := errs[0].(map[string]any)
	if !ok {
		t.Fatalf("errors[0] = %+v", errs[0])
	}
	if entry["row"] != 2 {
		t.Errorf("row = %v, want 2 (1-based, second item failed)", entry["row"])
	}
	if entry["data"] != "bad" {
		t.Errorf("data = %v, want the failing item back", entry["data"])
	}
	got, ok := entry["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %+v", entry["error"])
	}
	if got["code"] != "boom" {
		t.Errorf("code = %v, want boom", got["code"])
	}
}

func TestForEach_FailFastStopsEarly(t *testing.T) {
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: []any{"good", "bad", "also_good", "another"}}},
		Params: map[string]any{
			"fail_fast":   true,
			"concurrency": 1,
		},
	}
	res, err := executeForEach(withRunner(failRunner), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "item_failed" {
		t.Errorf("err = %+v, want item_failed", res.Error)
	}
	errs, _ := res.Output["errors"].Inline.([]any)
	rowOf := func(e any) any { m, _ := e.(map[string]any); return m["row"] }
	if len(errs) == 0 || rowOf(errs[0]) != 2 {
		t.Errorf("expected a failed row entry for row 2, got %+v", errs)
	}
}

func TestForEach_RespectsConcurrencyCap(t *testing.T) {
	var inflight, peak atomic.Int32
	runner := func(ctx context.Context, item core.Ref) (engine.GraphResult, error) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inflight.Add(-1)
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
		}
		return echoRunner(ctx, item)
	}
	items := make([]any, 10)
	for i := range items {
		items[i] = fmt.Sprintf("v%d", i)
	}
	job := core.Job{
		Input:  map[string]core.Ref{"items": {Inline: items}},
		Params: map[string]any{"concurrency": 3},
	}
	start := time.Now()
	res, err := executeForEach(withRunner(runner), job, nil)
	elapsed := time.Since(start)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %v", res.Status, err)
	}
	if peak.Load() > 3 {
		t.Errorf("peak in-flight = %d, want <= 3", peak.Load())
	}
	// 10 items / 3 parallel @ 40ms ≈ 4 waves ≈ 160ms minimum.
	if elapsed < 120*time.Millisecond {
		t.Errorf("ran in %v — too fast for concurrency cap of 3", elapsed)
	}
}

// TestForEach_ClampsExcessiveConcurrency guards the DoS fix: an author-set
// concurrency far above maxForEachConcurrency must not spawn one goroutine per
// item (each iteration runs a full body subgraph). Peak in-flight must stay at
// or below the hard ceiling.
func TestForEach_ClampsExcessiveConcurrency(t *testing.T) {
	var inflight, peak atomic.Int32
	runner := func(ctx context.Context, item core.Ref) (engine.GraphResult, error) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inflight.Add(-1)
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
		}
		return echoRunner(ctx, item)
	}
	items := make([]any, 500)
	for i := range items {
		items[i] = fmt.Sprintf("v%d", i)
	}
	job := core.Job{
		Input:  map[string]core.Ref{"items": {Inline: items}},
		Params: map[string]any{"concurrency": 1_000_000},
	}
	res, err := executeForEach(withRunner(runner), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %v", res.Status, err)
	}
	if got := peak.Load(); got > maxForEachConcurrency {
		t.Errorf("peak in-flight = %d, want <= %d (concurrency must be clamped)", got, maxForEachConcurrency)
	}
}

func TestForEach_AcceptsRefList(t *testing.T) {
	job := core.Job{
		Input: map[string]core.Ref{
			"items": {
				MIME:   MIMEList,
				Inline: []core.Ref{{Inline: "x"}, {Inline: "y"}},
			},
		},
	}
	res, err := executeForEach(withRunner(echoRunner), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("execute: status=%q err=%v", res.Status, err)
	}
	results, _ := res.Output["results"].Inline.([]core.Ref)
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
}

func TestForEach_MissingBodyRunnerFails(t *testing.T) {
	// No body pin wired ⇒ the daemon sets no runner ⇒ for_each has nothing to
	// run and must fail clearly rather than silently doing nothing.
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: []any{"x"}}},
	}
	res, err := executeForEach(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want bad_param (wire the body pin)", res)
	}
}

func TestForEach_MissingItemsInputFails(t *testing.T) {
	res, err := executeForEach(withRunner(echoRunner), core.Job{}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Fatalf("res = %+v, want missing_input", res)
	}
}

func TestForEach_EmptyListReturnsEmptyResults(t *testing.T) {
	job := core.Job{Input: map[string]core.Ref{"items": {Inline: []any{}}}}
	res, err := executeForEach(withRunner(echoRunner), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	results, _ := res.Output["results"].Inline.([]core.Ref)
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}
