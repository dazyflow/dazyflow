// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "delay",
			Version:     "1.0",
			Label:       "Delay",
			Icon:        "timer",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"timing", "delay", "sleep", "wait", "passthrough"},
			Description: "Pause for a configurable duration, then forward the threaded value on the pass-through output (or emit a control signal when nothing is threaded, so a pure pause still fires the next step).",
			Summary:     "Hold the flow for a fixed number of milliseconds before passing the input on to the next step.",
			Examples: []core.ParamsExample{
				{
					Title:  "Throttle a polling loop by one second",
					Params: json.RawMessage(`{"ms":1000}`),
				},
				{
					Title:  "Wait 30 seconds before retrying a later call",
					Params: json.RawMessage(`{"ms":30000}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// Delay hand-rolls its own passthrough (in→out, plus a control
			// signal on out when there's no input — see passthrough()), so it
			// opts out of the universal `pass` pin that WithPassthrough would
			// otherwise prepend. Without this it'd show two passthroughs: its
			// own in/out ports and the redundant auto exec pin.
			// `ms` is both a param and an input port (same id) so the wait can
			// be a literal typed inline on the pin OR computed by an upstream
			// node and wired in. The value being delayed rides the universal
			// `pass` pin (prepended by WithPassthrough) — Delay no longer
			// declares its own in/out passthrough ports; it threads through
			// like any other node.
			Inputs: []core.Port{{Port: "ms", Label: "Delay (milliseconds)", MIME: []string{"application/json"}}},
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"ms":{"type":"integer","minimum":0,"title":"Wait (milliseconds)","description":"How long to pause before continuing, in milliseconds (1000 = 1 second)."}},"required":["ms"]}`,
			),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeDelay,
	})
}

// maxDelayMs bounds the wait a single step may ask for. Past this the
// duration arithmetic overflows; well before it, the worker's node timeout
// has ended the step anyway.
const maxDelayMs = 365 * 24 * 60 * 60 * 1000

// maxInlineDelay is the longest wait this step serves by sleeping on the
// worker. Anything longer is deferred (core.StatusDeferred) so the slot goes
// back to the pool. A second is short enough that holding a slot for it costs
// nothing and long enough that the common sub-second pause — a hand-rolled
// rate-limit gap between two API calls — never pays for a requeue.
const maxInlineDelay = time.Second

func executeDelay(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	ms, ok := resolveDelayMs(job)
	if !ok {
		return params.Err(job, "bad_param", "ms is required: connect the Delay (ms) input or set the ms param"), nil
	}
	if ms < 0 {
		return params.Err(job, "bad_param", "ms must be non-negative"), nil
	}
	// Above ~9.2e12 ms the nanosecond conversion overflows int64 and the
	// timer fires at once — "wait a very long time" silently became "don't
	// wait". Refuse anything past a year instead: a longer pause belongs to
	// a Schedule trigger, not a step holding a worker slot.
	if ms > maxDelayMs {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"ms must be at most %d (one year); use a Schedule trigger for longer waits", maxDelayMs)), nil
	}

	total := time.Duration(ms) * time.Millisecond

	// A wait is pure waiting, and a worker is a serial claim → process loop
	// out of a small pool — so sleeping here holds one of the daemon's few
	// execution slots for the whole duration, and enough Waits in one flow
	// stall every tenant. Hand the slot back instead and ask to be re-claimed
	// at the deadline.
	//
	// The deadline is anchored on when the step became due, not on when this
	// attempt started: the anchor survives the requeue, so the re-execution
	// after the horizon passes computes the same deadline, finds no time left
	// and finishes. Anchoring on now would restart the wait every hop.
	//
	// Short waits stay inline either way — a sub-second pause is not worth a
	// store write and a re-claim, and it cannot starve anything. So does any
	// wait with no anchor: that means no job record is behind this step (a
	// loop body, a unit harness) and nothing would ever resume a deferral.
	anchor, deferrable := core.NodeEnqueuedAt(ctx)
	deadline := time.Now().Add(total)
	if deferrable {
		deadline = anchor.Add(total)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		// The horizon already passed — this is the re-execution after a
		// deferral, or a zero wait.
		params.EmitProgress(progress, job, 1.0, "done")
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: passthrough(job.Input),
		}, nil
	}
	// A timeout the author DECLARED still binds: deferring is not executing, so
	// the worker's context deadline no longer bounds the wait, and a Wait that
	// quietly outlived `timeout_seconds` would be a worse answer than the
	// blocking version gave. A wait longer than its own budget cannot finish
	// inside it whatever we do, so say so now rather than sleeping to find out.
	if budget := core.NodeTimeout(ctx); budget > 0 && remaining > budget {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error: &core.JobError{
				Code: "timeout",
				Message: fmt.Sprintf("waiting %v exceeds this step's %v timeout — raise the step's timeout, or shorten the wait",
					remaining.Round(time.Millisecond), budget),
			},
		}, nil
	}
	if deferrable && remaining > maxInlineDelay {
		params.EmitProgress(progress, job, 0, fmt.Sprintf("waiting until %v", deadline.UTC().Format(time.RFC3339)))
		return core.Deferred(job.ID, deadline), nil
	}

	total = remaining
	timer := time.NewTimer(total)
	defer timer.Stop()

	tickInterval := total / 10
	if tickInterval < 50*time.Millisecond {
		tickInterval = 50 * time.Millisecond
	}
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return params.Err(job, "cancelled", ctx.Err().Error()), ctx.Err()
		case <-timer.C:
			params.EmitProgress(progress, job, 1.0, "done")
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: passthrough(job.Input),
			}, nil
		case <-ticker.C:
			pct := float64(time.Since(start)) / float64(total)
			if pct > 1 {
				pct = 1
			}
			params.EmitProgress(progress, job, pct, fmt.Sprintf("%v elapsed", time.Since(start).Round(time.Millisecond)))
		}
	}
}

// resolveDelayMs reads the delay duration from the wired `ms` input when
// connected, else from the `ms` param. Lets an upstream node compute the
// wait dynamically (e.g. an exponential backoff) instead of a fixed literal.
// Returns false when neither source supplies a usable number.
func resolveDelayMs(job core.Job) (int, bool) {
	if ref, ok := job.Input["ms"]; ok {
		if n, ok := coerceInt(ref.Inline); ok {
			return n, true
		}
	}
	return coerceInt(job.Params["ms"])
}

// passthrough always emits on the universal pass pin so downstream nodes are
// activated even when the delay is used as a pure pause (no value threaded
// in). When a value rides the pass input we forward it; otherwise we emit a
// control-signal ref so the edge classifier still sees an active output.
// (We set the pass output here rather than leaving it to the engine's
// ApplyPassthrough so the empty-input control-signal case is covered too —
// ApplyPassthrough only forwards an input that's actually present.)
func passthrough(input map[string]core.Ref) map[string]core.Ref {
	if ref, ok := input[core.PassPort]; ok {
		return map[string]core.Ref{core.PassPort: ref}
	}
	return map[string]core.Ref{core.PassPort: {MIME: "application/x-control"}}
}
