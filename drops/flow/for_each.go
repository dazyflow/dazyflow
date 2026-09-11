// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "for_each",
			Version:  "1.0",
			Label:    "For each",
			Icon:     "repeat",
			Category: "flow_control",
			Provider: "internal",
			Tags: []string{"iterate", "loop", "fan_out", "map", "every", "each row",
				"one at a time", "per row", "repeat"},
			Description: "Run the loop body — the steps connected to the Loop body input — once per item in an input list. Items execute in parallel up to the concurrency setting. Outputs `results` (one entry per item, in order) and `errors` (a list of failed rows: {row, data, error}, row is 1-based). Set fail_fast=true to abort on the first failure; otherwise the iteration continues and failures surface on the errors port. If MOST items fail the step fails anyway — that is an outage, not a partial success, and a later step shouldn't record the work as done. A few failures among many still succeed, and the count is written to the run log either way.",
			Summary:     "Fan out a list and run the connected Loop body on every item, optionally in parallel, collecting results in order.",
			Examples: []core.ParamsExample{
				{
					Title:  "Handle each new form response one at a time",
					Params: json.RawMessage(`{}`),
					Notes:  "A trigger like Google Forms emits a LIST of new responses. Connect that list into 'List' here, then connect the steps that handle one response (AI reply → save → notify → send) to the 'Loop body' input. Inside the body, refer to the current response as ${item.email}, ${item.Message}, etc. Without this, the next step would run once on the whole batch.",
				},
				{
					Title:  "POST each row to a webhook, 5 at a time",
					Params: json.RawMessage(`{"concurrency":5}`),
					Notes:  "Connect the 'Loop body' input to an HTTP step whose body is ${item.} (the whole item as JSON) or ${item.field.subfield} for a nested value. concurrency caps how many run at once.",
				},
				{
					Title:  "Send a templated email per recipient, stop on first failure",
					Params: json.RawMessage(`{"fail_fast":true}`),
					Notes:  "Connect the 'Loop body' input to an Email step using ${item.email} / ${item.name}. fail_fast aborts the loop on the first failed item.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:  "items",
				Label: "List",
			}},
			Outputs: []core.Port{
				{Port: "body", Label: "Loop body", MIME: []string{"application/x-dazyflow-exec"}},
				{Port: "results", Label: "Results", MIME: []string{MIMEList}},
				{Port: "errors", Label: "Failed rows", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"concurrency":{"type":"integer","minimum":1,"title":"Concurrency","description":"How many items to process at once. Higher is faster but hits rate limits sooner."},
						"fail_fast":{"type":"boolean","default":false,"title":"Stop on first error","description":"Stop the whole loop as soon as one item fails, instead of continuing with the rest."},
						"max_per_second":{"type":"number","minimum":0.01,"maximum":1000,"title":"Start at most (per second)","description":"Pace how fast items are STARTED, for a loop that calls someone else's API — 5 means a fifth of a second between starts, 0.5 means one every two seconds. Leave blank for no pacing. Concurrency still bounds how many run at once; this bounds how often a new one begins."}
					}
				}`,
			),
			Idempotent: true,
		},
		Execute: executeForEach,
	})
}

func executeForEach(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	failFast, _ := job.Params["fail_fast"].(bool)
	concurrency, _ := paramInt(job.Params, "concurrency")
	if concurrency < 0 {
		concurrency = 0
	}

	itemsRef, ok := job.Input["items"]
	if !ok {
		return params.Err(job, "missing_input", "items input is required"), nil
	}
	items, err := normalizeItems(itemsRef)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	runner, ok := engine.BodyRunnerFromContext(ctx)
	if !ok {
		return params.Err(job, "bad_param", "connect the For each `body` pin to the step(s) that handle one item"), nil
	}
	return runForEachItems(ctx, job, progress, items, concurrency, failFast,
		func(rctx context.Context, _ int, item core.Ref) (core.Ref, any) {
			gr, execErr := runner(rctx, item)
			res := core.Ref{MIME: "application/json", Inline: bodyResultPayload(gr)}
			if execErr != nil {
				return res, execErr.Error()
			}
			if gr.Status == core.StatusError {
				return res, errorPayload(gr.Error)
			}
			return res, nil
		}), nil
}

// defaultForEachConcurrency bounds parallelism when the author leaves
// concurrency unset. Small enough to keep memory/goroutine pressure sane on
// the shared daemon, large enough that independent I/O-bound iterations still
// overlap. Authors who want more set the concurrency param explicitly.
const defaultForEachConcurrency = 8

// maxForEachConcurrency is the hard ceiling on parallel iterations regardless
// of what the author requests. Each iteration runs a full in-process body
// subgraph (its own goroutine, ctx, and a JSON deep-copy of the body graph),
// so an unclamped explicit concurrency over a large items list (items is
// capped at limits.MaxRows = 1,000,000) would spawn up to that many concurrent
// subgraph runs and exhaust the shared daemon's goroutines/heap from a single
// run. 64 keeps a generous amount of real parallelism while bounding the blast
// radius on the multi-tenant daemon.
const maxForEachConcurrency = 64

// paceInterval turns "at most N starts a second" into the gap between two of
// them. Zero when the author asked for no pacing, which is the default: most
// loops are over the flow's own data and have nobody to be polite to.
func paceInterval(p map[string]any) time.Duration {
	rate, ok := paramFloat(p, "max_per_second")
	if !ok || rate <= 0 {
		return 0
	}
	if rate > 1000 {
		rate = 1000
	}
	return time.Duration(float64(time.Second) / rate)
}

type itemFunc func(ctx context.Context, idx int, item core.Ref) (core.Ref, any)

// runForEachItems is the shared fan-out skeleton for both modes: it runs
// `run` once per item up to `concurrency` at a time, collects results in
// order and failures keyed by index, and (when fail_fast) cancels the
// remaining iterations on the first failure.
func runForEachItems(
	ctx context.Context,
	job core.Job,
	progress chan<- core.Progress,
	items []core.Ref,
	concurrency int,
	failFast bool,
	run itemFunc,
) core.Result {
	if len(items) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"results": {MIME: MIMEList, Inline: []core.Ref{}},
				"errors":  {MIME: "application/json", Inline: []any{}},
			},
		}
	}

	results := make([]core.Ref, len(items))
	type failure struct {
		idx   int
		entry map[string]any
	}
	var failures []failure
	var errsMu sync.Mutex

	// When the author doesn't set concurrency, cap parallelism at a modest
	// default rather than fanning out one goroutine per item: each iteration
	// runs a full in-process body subgraph, so an unset concurrency on a
	// large items list (items is itself capped at limits.MaxRows) would
	// otherwise spawn up to that many concurrent subgraph runs and exhaust
	// the daemon. An explicit concurrency is clamped to maxForEachConcurrency
	// so a malicious/careless author can't request a million-wide fan-out.
	gate := concurrency
	if gate == 0 {
		gate = defaultForEachConcurrency
	}
	if gate > maxForEachConcurrency {
		gate = maxForEachConcurrency
	}
	if gate > len(items) {
		gate = len(items)
	}
	sem := make(chan struct{}, gate)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Spacing the STARTS is what a rate limit means to the service on the other
	// end: concurrency says how many calls may be in flight, and says nothing
	// about how often a new one begins. A loop of 500 rows against an API that
	// allows five a second needs both.
	pace := paceInterval(job.Params)

	var wg sync.WaitGroup
	for i, item := range items {
		if runCtx.Err() != nil {
			break
		}
		if pace > 0 && i > 0 {
			t := time.NewTimer(pace)
			select {
			case <-t.C:
			case <-runCtx.Done():
				t.Stop()
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, value core.Ref) {
			defer wg.Done()
			defer func() { <-sem }()
			// The engine's recover wraps Execute on the CALLING goroutine, so
			// it cannot catch a panic raised here — an unrecovered one takes
			// the whole daemon down instead of failing one row. Convert it
			// into an ordinary per-item failure, which the caller already
			// knows how to report.
			defer func() {
				if r := recover(); r != nil {
					errsMu.Lock()
					failures = append(failures, failure{idx: idx, entry: map[string]any{
						"row":   idx + 1,
						"data":  value.Inline,
						"error": fmt.Sprintf("internal error while processing this item: %v", r),
					}})
					errsMu.Unlock()
					if failFast {
						cancel()
					}
				}
			}()
			if runCtx.Err() != nil {
				return
			}
			res, errEntry := run(runCtx, idx, value)
			params.EmitProgress(progress, job, float64(idx+1)/float64(len(items)),
				fmt.Sprintf("item %d/%d done", idx+1, len(items)))
			results[idx] = res
			if errEntry != nil {
				errsMu.Lock()
				failures = append(failures, failure{idx: idx, entry: map[string]any{
					"row":   idx + 1,
					"data":  value.Inline,
					"error": errEntry,
				}})
				errsMu.Unlock()
				if failFast {
					cancel()
				}
			}
		}(i, item)
	}
	wg.Wait()

	sort.Slice(failures, func(a, b int) bool { return failures[a].idx < failures[b].idx })
	failedRows := make([]any, len(failures))
	for i, f := range failures {
		failedRows[i] = f.entry
	}

	if len(failures) > 0 {
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"%d of %d item(s) failed — see the Failed rows output", len(failures), len(items)))
	}

	status := core.StatusOK
	var jobErr *core.JobError
	switch {
	case len(failures) > 0 && failFast:
		status = core.StatusError
		jobErr = &core.JobError{
			Code:    "item_failed",
			Message: fmt.Sprintf("for_each aborted: %d/%d items failed", len(failures), len(items)),
		}
	case len(failures) == len(items):
		// Nothing worked. "Carry on past a failed item" is a reasonable
		// default for one bad row among many; reporting SUCCESS when every
		// single item failed is not — that is an outage, and a flow whose
		// next step marks the work done would mark work that never happened.
		status = core.StatusError
		jobErr = &core.JobError{
			Code:    "all_items_failed",
			Message: fmt.Sprintf("every item failed (%d/%d) — see the failed rows", len(failures), len(items)),
		}
	case len(failures)*2 > len(items):
		status = core.StatusError
		jobErr = &core.JobError{
			Code: "most_items_failed",
			Message: fmt.Sprintf("%d of %d items failed — see the failed rows. "+
				"The %d that succeeded are on the results output.",
				len(failures), len(items), len(items)-len(failures)),
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: status,
		Output: map[string]core.Ref{
			"results": {MIME: MIMEList, Inline: results},
			"errors":  {MIME: "application/json", Inline: failedRows},
		},
		Error: jobErr,
	}
}

func bodyResultPayload(gr engine.GraphResult) map[string]any {
	nodes := make(map[string]any, len(gr.Nodes))
	for id, r := range gr.Nodes {
		entry := map[string]any{"status": r.Status}
		if len(r.Output) > 0 {
			entry["output"] = r.Output
		}
		if r.Error != nil {
			entry["error"] = errorPayload(r.Error)
		}
		nodes[id] = entry
	}
	payload := map[string]any{"status": gr.Status, "nodes": nodes}
	if gr.Error != nil {
		payload["error"] = errorPayload(gr.Error)
	}
	return payload
}

// capItems rejects an items list bigger than the row ceiling. for_each
// allocates a result slot (and runs a sub-job) per item, so an unbounded list
// is an OOM vector; fail fast before materializing it.
func capItems(n int) error {
	if max := limits.MaxRows(); n > max {
		return fmt.Errorf("items list has %d entries, exceeds the %d-item limit (raise DAZYFLOW_MAX_ROWS to iterate larger lists)", n, max)
	}
	return nil
}

func normalizeItems(ref core.Ref) ([]core.Ref, error) {
	switch v := ref.Inline.(type) {
	case []core.Ref:
		if err := capItems(len(v)); err != nil {
			return nil, err
		}
		return v, nil
	case []any:
		if err := capItems(len(v)); err != nil {
			return nil, err
		}
		out := make([]core.Ref, len(v))
		for i, item := range v {
			out[i] = core.Ref{Inline: item}
		}
		return out, nil
	case []map[string]any:
		if err := capItems(len(v)); err != nil {
			return nil, err
		}
		out := make([]core.Ref, len(v))
		for i, item := range v {
			out[i] = core.Ref{Inline: item}
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("items input has no inline value")
	default:
		return nil, fmt.Errorf("items input must be a list, got %T", v)
	}
}

func errorPayload(e *core.JobError) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{"code": e.Code, "message": e.Message}
}
