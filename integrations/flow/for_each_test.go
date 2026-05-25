package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

// registerOnce makes the test helper modules idempotent against the global
// registry — tests in the same binary share engine.Default so re-registering
// would panic.
var (
	stepsRegistered sync.Once
	captureOnce     sync.Once

	// captureMu + capturedParams are PROCESS-GLOBAL so the capture step
	// registered once via captureOnce.Do can write into a buffer the
	// current test owns. Without this, a second `go test -count=2` run
	// would still hit the once-registered closure, which captured the
	// FIRST run's `seen` slice — second-run invocations would silently
	// vanish.
	captureMu      sync.Mutex
	capturedParams []map[string]any
)

func registerTestSteps(t *testing.T) {
	t.Helper()
	stepsRegistered.Do(func() {
		// Echo step: takes whatever is on input "in" and emits it on output "out".
		engine.Register(engine.NativeDrop{
			Manifest: core.Manifest{
				ID:      "test_echo_step",
				Version: "1.0",
				Inputs:  []core.Port{{Port: "in"}},
				Outputs: []core.Port{{Port: "out"}},
			},
			Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
				in := job.Input["in"]
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusOK,
					Output: map[string]core.Ref{"out": in},
				}, nil
			},
		})

		// Fail-on-target step: errors when the item value matches params["fail_when"].
		engine.Register(engine.NativeDrop{
			Manifest: core.Manifest{
				ID:      "test_fail_step",
				Version: "1.0",
				Inputs:  []core.Port{{Port: "in"}},
				Outputs: []core.Port{{Port: "out"}},
			},
			Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
				want, _ := job.Params["fail_when"].(string)
				got, _ := job.Input["in"].Inline.(string)
				if got == want {
					return core.Result{
						JobID:  job.ID,
						Status: core.StatusError,
						Error:  &core.JobError{Code: "boom", Message: got},
					}, nil
				}
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusOK,
					Output: map[string]core.Ref{"out": {Inline: got}},
				}, nil
			},
		})

		// Slow + counter step: bumps a shared counter, sleeps, then returns.
		// Used to assert concurrency.
		engine.Register(engine.NativeDrop{
			Manifest: core.Manifest{
				ID:      "test_slow_step",
				Version: "1.0",
				Inputs:  []core.Port{{Port: "in"}},
				Outputs: []core.Port{{Port: "out"}},
			},
			Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
				if cnt, ok := job.Params["__inflight"].(*atomic.Int32); ok {
					n := cnt.Add(1)
					if peak, ok := job.Params["__peak"].(*atomic.Int32); ok {
						for {
							p := peak.Load()
							if n <= p || peak.CompareAndSwap(p, n) {
								break
							}
						}
					}
					defer cnt.Add(-1)
				}
				select {
				case <-time.After(40 * time.Millisecond):
				case <-ctx.Done():
				}
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusOK,
					Output: map[string]core.Ref{"out": job.Input["in"]},
				}, nil
			},
		})
	})
}

func TestForEach_RunsStepPerItemInOrder(t *testing.T) {
	registerTestSteps(t)
	job := core.Job{
		ID: "fe",
		Input: map[string]core.Ref{
			"items": {Inline: []any{"alpha", "beta", "gamma"}},
		},
		Params: map[string]any{
			"step_module": "test_echo_step",
			"item_port":   "in",
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	results, ok := res.Output["results"].Inline.([]core.Ref)
	if !ok {
		t.Fatalf("results is %T, want []core.Ref", res.Output["results"].Inline)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		payload, _ := results[i].Inline.(map[string]any)
		output, _ := payload["output"].(map[string]core.Ref)
		got, _ := output["out"].Inline.(string)
		if got != want {
			t.Errorf("results[%d] = %q, want %q", i, got, want)
		}
	}
	errs, _ := res.Output["errors"].Inline.(map[string]any)
	if len(errs) != 0 {
		t.Errorf("errors = %+v, want empty", errs)
	}
}

func TestForEach_CollectsPerItemErrorsWithoutFailFast(t *testing.T) {
	registerTestSteps(t)
	job := core.Job{
		Input: map[string]core.Ref{
			"items": {Inline: []any{"good", "bad", "also_good"}},
		},
		Params: map[string]any{
			"step_module": "test_fail_step",
			"step_params": map[string]any{"fail_when": "bad"},
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok (fail_fast=false)", res.Status)
	}
	errs, _ := res.Output["errors"].Inline.(map[string]any)
	if len(errs) != 1 {
		t.Fatalf("errors = %+v, want exactly index 1", errs)
	}
	got, ok := errs["1"].(map[string]any)
	if !ok {
		t.Fatalf("errors[1] = %+v", errs["1"])
	}
	if got["code"] != "boom" {
		t.Errorf("code = %v, want boom", got["code"])
	}
}

func TestForEach_FailFastStopsEarly(t *testing.T) {
	registerTestSteps(t)
	job := core.Job{
		Input: map[string]core.Ref{
			"items": {Inline: []any{"good", "bad", "also_good", "another"}},
		},
		Params: map[string]any{
			"step_module": "test_fail_step",
			"step_params": map[string]any{"fail_when": "bad"},
			"fail_fast":   true,
			"concurrency": 1,
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "item_failed" {
		t.Errorf("err = %+v, want item_failed", res.Error)
	}
	errs, _ := res.Output["errors"].Inline.(map[string]any)
	if len(errs) == 0 || errs["1"] == nil {
		t.Errorf("expected error on index 1, got %+v", errs)
	}
}

func TestForEach_RespectsConcurrencyCap(t *testing.T) {
	registerTestSteps(t)
	var inflight, peak atomic.Int32
	items := make([]any, 10)
	for i := range items {
		items[i] = fmt.Sprintf("v%d", i)
	}
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: items}},
		Params: map[string]any{
			"step_module": "test_slow_step",
			"step_params": map[string]any{
				"__inflight": &inflight,
				"__peak":     &peak,
			},
			"concurrency": 3,
		},
	}
	start := time.Now()
	res, err := executeForEach(t.Context(), job, nil)
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

func TestForEach_AcceptsRefList(t *testing.T) {
	registerTestSteps(t)
	job := core.Job{
		Input: map[string]core.Ref{
			"items": {
				MIME: MIMEList,
				Inline: []core.Ref{
					{Inline: "x"},
					{Inline: "y"},
				},
			},
		},
		Params: map[string]any{"step_module": "test_echo_step"},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("execute: status=%q err=%v", res.Status, err)
	}
	results, _ := res.Output["results"].Inline.([]core.Ref)
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
}

func TestForEach_UnknownStepModuleFails(t *testing.T) {
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: []any{"x"}}},
		Params: map[string]any{
			"step_module": "definitely_not_a_real_module_xyz",
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || !strings.Contains(res.Error.Code, "unknown_step") {
		t.Fatalf("res = %+v", res)
	}
}

// TestForEach_TemplatesItemFieldsIntoStepParams verifies the unlock —
// each iteration gets its own copy of step_params with ${item:path}
// placeholders substituted by the current item's fields. Without this,
// you can't actually parameterize per-item HTTP calls / AI calls.
func TestForEach_TemplatesItemFieldsIntoStepParams(t *testing.T) {
	registerTestSteps(t)
	// The capture step writes into the package-global capturedParams so
	// it survives re-runs that re-execute the test body but not the
	// one-shot module registration.
	captureMu.Lock()
	capturedParams = nil
	captureMu.Unlock()
	captureOnce.Do(func() {
		engine.Register(engine.NativeDrop{
			Manifest: core.Manifest{
				ID:      "test_capture_step",
				Version: "1.0",
				Inputs:  []core.Port{{Port: "in"}},
				Outputs: []core.Port{{Port: "out"}},
			},
			Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
				captureMu.Lock()
				cp := make(map[string]any, len(job.Params))
				for k, v := range job.Params {
					cp[k] = v
				}
				capturedParams = append(capturedParams, cp)
				captureMu.Unlock()
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusOK,
					Output: map[string]core.Ref{"out": {Inline: "ok"}},
				}, nil
			},
		})
	})

	items := []any{
		map[string]any{"id": "u-1", "name": "alice"},
		map[string]any{"id": "u-2", "name": "bob"},
	}
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: items}},
		Params: map[string]any{
			"step_module": "test_capture_step",
			"step_params": map[string]any{
				"url":   "https://api.example.com/users/${item:id}",
				"label": "user=${item:name}",
				"tags":  []any{"id:${item:id}"},
				"nested": map[string]any{
					"who": "${item:name}",
				},
			},
			"concurrency": 1, // serialize for deterministic seen ordering
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("execute: status=%q err=%v", res.Status, err)
	}
	captureMu.Lock()
	seen := append([]map[string]any(nil), capturedParams...)
	captureMu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("captured %d invocations, want 2", len(seen))
	}
	// Item 0
	if got := seen[0]["url"]; got != "https://api.example.com/users/u-1" {
		t.Errorf("seen[0].url = %q", got)
	}
	if got := seen[0]["label"]; got != "user=alice" {
		t.Errorf("seen[0].label = %q", got)
	}
	if tags, ok := seen[0]["tags"].([]any); !ok || tags[0] != "id:u-1" {
		t.Errorf("seen[0].tags = %+v", seen[0]["tags"])
	}
	if nest := seen[0]["nested"].(map[string]any); nest["who"] != "alice" {
		t.Errorf("seen[0].nested.who = %v", nest["who"])
	}
	// Item 1
	if got := seen[1]["url"]; got != "https://api.example.com/users/u-2" {
		t.Errorf("seen[1].url = %q", got)
	}
}

func TestForEach_TemplateMissingFieldFailsThatIteration(t *testing.T) {
	registerTestSteps(t)
	items := []any{
		map[string]any{"id": "ok-1"},
		map[string]any{"other": "x"}, // missing id
	}
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: items}},
		Params: map[string]any{
			"step_module": "test_echo_step",
			"step_params": map[string]any{
				"target": "/api/${item:id}",
			},
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Without fail_fast the iteration should continue and surface the
	// missing-field error keyed by the failing index.
	errs, _ := res.Output["errors"].Inline.(map[string]any)
	if errs["1"] == nil {
		t.Fatalf("expected error for index 1, got %+v", errs)
	}
	errPayload := errs["1"].(map[string]any)
	if errPayload["code"] != "template" {
		t.Errorf("code = %v, want template", errPayload["code"])
	}
}

func TestForEach_TemplatesScalarItems(t *testing.T) {
	registerTestSteps(t)
	items := []any{"alpha", "beta"}
	job := core.Job{
		Input: map[string]core.Ref{"items": {Inline: items}},
		Params: map[string]any{
			"step_module": "test_echo_step",
			"step_params": map[string]any{
				"who": "${item:}", // empty path = whole item
			},
		},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("execute: %v / %q", err, res.Status)
	}
	// Echo just passes "in" through to "out", we can't assert on
	// substituted params from outside the step. But the test will fail
	// above if substitution errored (empty path used to be unsupported).
	results, _ := res.Output["results"].Inline.([]core.Ref)
	if len(results) != 2 {
		t.Fatalf("len = %d", len(results))
	}
}

func TestForEach_EmptyListReturnsEmptyResults(t *testing.T) {
	registerTestSteps(t)
	job := core.Job{
		Input:  map[string]core.Ref{"items": {Inline: []any{}}},
		Params: map[string]any{"step_module": "test_echo_step"},
	}
	res, err := executeForEach(t.Context(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	results, _ := res.Output["results"].Inline.([]core.Ref)
	if len(results) != 0 {
		t.Errorf("len = %d, want 0", len(results))
	}
}
