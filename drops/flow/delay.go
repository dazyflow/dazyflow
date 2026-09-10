// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/reltime"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "delay",
			Version:  "1.0",
			Label:    "Delay",
			Icon:     "timer",
			Category: "flow_control",
			Provider: "internal",
			Tags:     []string{"timing", "delay", "sleep", "wait", "passthrough"},
			Description: "Pause, then carry on — the threaded value comes out the other side (or a control signal when there is nothing threaded, so a pure pause still fires the next step).\n\n" +
				"Say how long with 'Wait', in milliseconds, for the short pauses: a gap between two API calls, a moment for a system to catch up.\n\n" +
				"Say WHEN instead with 'Wait until', for the long ones. It takes the same time words the calendar steps do — \"tomorrow\", \"tomorrow+9h\" for tomorrow morning, \"now+2h\", \"+3d\", or a timestamp the Date & time step handed you — and 'Timezone' decides which day \"tomorrow\" means. A moment that has already passed does not fail; the flow simply carries straight on.\n\n" +
				"A wait longer than a second hands its worker back and asks to be woken at the deadline, so a flow parked until Monday costs nothing while it waits. A year is the ceiling either way — for longer, use a Schedule trigger.",
			Summary: "Hold the flow for a set time, or until a given moment, then pass the input on.",
			Examples: []core.ParamsExample{
				{
					Title:  "Throttle a polling loop by one second",
					Params: json.RawMessage(`{"ms":1000}`),
				},
				{
					Title:  "Wait 30 seconds before retrying a later call",
					Params: json.RawMessage(`{"ms":30000}`),
				},
				{
					Title:  "Hold until tomorrow morning",
					Params: json.RawMessage(`{"until":"tomorrow+9h","tz":"Europe/Stockholm"}`),
					Notes:  "\"tomorrow\" is midnight in the given zone, so +9h is 09:00 there. The step hands its worker back while it waits.",
				},
				{
					Title:  "Hold until a moment an earlier step worked out",
					Params: json.RawMessage(`{}`),
					Notes:  "Wire a timestamp into the 'Wait until' input — what the Date & time step emits fits as it is.",
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
			Inputs: []core.Port{
				{Port: "ms", Label: "Delay (milliseconds)", MIME: []string{"application/json"}},
				{Port: "until", Label: "Wait until", MIME: []string{"text/plain"}},
			},
			// Neither wait is `required`: exactly one of them must be set, which
			// a schema cannot say and Execute reports instead.
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{` +
					`"ms":{"type":"integer","minimum":0,"title":"Wait (milliseconds)","description":"How long to pause before continuing, in milliseconds (1000 = 1 second). Use 'Wait until' instead to hold for a named moment."},` +
					`"until":{"type":"string","title":"Wait until","description":"The moment to carry on at: \"tomorrow\", \"tomorrow+9h\", \"now+2h\", \"+3d\", or a timestamp (2026-06-16T09:00:00Z). A moment already past carries straight on. The 'Wait until' input overrides this when connected."},` +
					`"tz":{"type":"string","format":"timezone","title":"Timezone","description":"Which zone the day boundaries of \"today\" and \"tomorrow\" are taken in, e.g. \"Europe/Stockholm\". Empty = UTC. Ignored for a full timestamp."}` +
					`}}`,
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
	ms, hasMS := resolveDelayMs(job)
	until := resolveUntil(job)

	// Absolute and relative say different things about a requeue, so a step
	// carrying both has no single answer to give.
	if until != "" && hasMS {
		return params.Err(job, "bad_param",
			"'Wait' and 'Wait until' are both set — keep the one you meant"), nil
	}
	if until == "" && !hasMS {
		return params.Err(job, "bad_param",
			"set 'Wait' (how long, in milliseconds) or 'Wait until' (the moment to carry on at)"), nil
	}

	var target time.Time
	if until != "" {
		loc, lerr := delayLocation(job)
		if lerr != nil {
			return params.Err(job, "bad_param", lerr.Error()), nil
		}
		t, ok, rerr := reltime.Resolve(until, loc, time.Now())
		if rerr != nil || !ok {
			if rerr == nil {
				rerr = fmt.Errorf("'Wait until' is empty")
			}
			return params.Err(job, "bad_param", rerr.Error()), nil
		}
		target = t
		// A moment already gone is not an error — a flow that ran late should
		// carry on, not fail — so it becomes a zero wait.
		ms = int(max(0, time.Until(target).Milliseconds()))
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

	// A wait is pure waiting, and a worker is a serial claim → process loop out of
	// a small pool — so sleeping here holds one of the daemon's few execution slots
	// for the whole duration, and enough Waits in one flow stall every tenant. Hand
	// the slot back instead and ask to be re-claimed at the deadline.
	//
	// The deadline is anchored on when the step became due, not on when this
	// attempt started: the anchor survives the requeue, so the re-execution
	// computes the same deadline and finishes. Anchoring on now would restart the
	// wait every hop.
	//
	// Short waits stay inline — not worth a store write and a re-claim, and they
	// cannot starve anything. So does any wait with no anchor: no job record is
	// behind the step, so nothing would ever resume a deferral.
	anchor, deferrable := core.NodeEnqueuedAt(ctx)
	deadline := time.Now().Add(total)
	if deferrable {
		deadline = anchor.Add(total)
	}
	// A named moment IS the deadline: re-deriving it from an anchor would move
	// it, and "tomorrow at nine" must mean the same instant on every hop.
	if !target.IsZero() {
		deadline = target
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
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

// resolveUntil reads the moment to wait for, preferring a wired value over the
// typed one so an earlier step can work it out.
func resolveUntil(job core.Job) string {
	if v, ok := params.TextInputOr(job, "until", params.StringDefault(job.Params, "until", "")); ok {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(params.StringDefault(job.Params, "until", ""))
}

// delayLocation resolves the step's timezone, matching the calendar steps:
// empty means UTC, and it decides which day "today" and "tomorrow" name.
func delayLocation(job core.Job) (*time.Location, error) {
	tz := strings.TrimSpace(params.StringDefault(job.Params, "tz", ""))
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%q isn't a timezone name — use an IANA one like \"Europe/Stockholm\"", tz)
	}
	return loc, nil
}

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
