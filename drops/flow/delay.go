package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
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
			Description: "Pause for a configurable duration, then forward the threaded value downstream on the pass pin (or emit a control signal when nothing is threaded, so a pure pause still fires the next node).",
			Summary:     "Hold the flow for a fixed number of milliseconds before forwarding the input downstream.",
			Examples: []core.ParamsExample{
				{
					Title:  "Throttle a polling loop by one second",
					Params: json.RawMessage(`{"ms":1000}`),
				},
				{
					Title:  "Wait 30 seconds before retrying a downstream call",
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
			Inputs: []core.Port{{Port: "ms", Label: "Delay (ms)", MIME: []string{"application/json"}}},
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"ms":{"type":"integer","minimum":0}},"required":["ms"]}`,
			),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeDelay,
	})
}

func executeDelay(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	ms, ok := resolveDelayMs(job)
	if !ok {
		return params.Err(job, "bad_param", "ms is required: wire the Delay (ms) input or set the ms param"), nil
	}
	if ms < 0 {
		return params.Err(job, "bad_param", "ms must be non-negative"), nil
	}

	total := time.Duration(ms) * time.Millisecond
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
			emitProgress(progress, job, 1.0, "done")
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
			emitProgress(progress, job, pct, fmt.Sprintf("%v elapsed", time.Since(start).Round(time.Millisecond)))
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
