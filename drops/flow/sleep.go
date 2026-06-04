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
			ID:          "sleep",
			Version:     "1.0",
			Label:       "Sleep",
			Icon:        "timer",
			Category:    "flow_control",
			Provider:    "internal",
			Tags:        []string{"timing", "delay", "passthrough"},
			Description: "Pause for a configurable duration. Forwards any input on the in port to out (or emits a control signal if input is empty).",
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
			Inputs:         []core.Port{{Port: "in", Label: "Passthrough"}},
			Outputs:        []core.Port{{Port: "out", Label: "Passthrough"}},
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"ms":{"type":"integer","minimum":0}},"required":["ms"]}`,
			),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSleep,
	})
}

func executeSleep(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	ms, err := paramInt(job.Params, "ms")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
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

// passthrough always emits on "out" so downstream nodes are activated
// even when sleep is used as a pure delay (no input). When upstream did
// feed something in we forward it; otherwise we emit a control-signal
// ref so the edge classifier sees an active output.
func passthrough(input map[string]core.Ref) map[string]core.Ref {
	if ref, ok := input["in"]; ok {
		return map[string]core.Ref{"out": ref}
	}
	return map[string]core.Ref{"out": {MIME: "application/x-control"}}
}
