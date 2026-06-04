package trigger

import (
	"context"
	"encoding/json"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "poll_trigger",
			Version:     "1.0",
			Label:       "Poll trigger",
			Icon:        "timer",
			Category:    "trigger",
			Provider:    "internal",
			Tags:        []string{"poll", "trigger", "interval", "schedule"},
			Description: "Marks a graph as poll-driven — fires every N seconds (configured on the GRAPH'S 'poll' trigger, not on this node). Outputs `fired_at` (RFC3339 timestamp). For dedupe-against-seen behavior, store a cursor in the tenant secret store with a downstream node and read it back on the next fire.",
			Summary:     "Trigger that fires the graph on the workspace's poll schedule, emitting the fire timestamp.",
			Examples: []core.ParamsExample{
				{
					Title:  "Poll trigger (interval lives on the graph trigger, not this node)",
					Params: json.RawMessage(`{}`),
					Notes:  "Configure the polling interval on the graph's 'poll' trigger; this node just emits 'fired_at' downstream.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "fired_at", Label: "RFC3339 timestamp of this fire", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			// Retry-safe but operationally meaningless — a poll fire is
			// a discrete event; rerunning doesn't re-derive a "fired at"
			// from the original tick.
			Idempotent: false,
		},
		Execute: executePollTrigger,
	})
}

// executePollTrigger emits the current timestamp. Unlike
// webhook_input (which the daemon pre-completes outside the worker
// path), poll_trigger runs normally — there's no per-fire data to
// inject, and "the time" is intrinsic to the fire moment itself.
// Manual runs (e.g. `hzctl graph run` or the UI's Run button)
// produce a perfectly valid result, treated as a one-off fire of
// the poll graph. That's by design: the same graph should work
// whether the scheduler fired it or the user did, which is the
// natural mental model for "test this poll workflow now."
func executePollTrigger(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"fired_at": {MIME: "text/plain", Inline: time.Now().UTC().Format(time.RFC3339)},
		},
	}, nil
}
