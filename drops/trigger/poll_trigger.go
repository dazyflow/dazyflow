// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package trigger

import (
	"context"
	"encoding/json"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "poll_trigger",
			Version:     "1.0",
			Label:       "Interval",
			Icon:        "timer",
			Category:    "trigger",
			Provider:    "internal",
			Tags:        []string{"poll", "trigger", "interval", "schedule"},
			Description: "Starts the flow over and over at a fixed pace — every few minutes, hours or days. The Time output is when it fired. With no interval set, the flow runs only when you press Run.",
			Summary:     "Starts the flow over and over at a fixed pace — e.g. every 5 minutes.",
			Examples: []core.ParamsExample{
				{
					Title:  "Every 5 minutes",
					Params: json.RawMessage(`{"interval_seconds":300}`),
					Notes:  "The interval lives on the node; the scheduler reads it. Emits 'fired_at' downstream.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				// The pass pin is the trigger's primary output: an untyped
				// sequencing wire that snaps to the next drop's Pass-through
				// input (triangle → triangle), so "run this next" is a
				// self-evident connection rather than dragging the typed Time
				// value into a generic pin. WithPassthrough leaves triggers
				// alone (they originate flows, so no pass INPUT), so we declare
				// the pass OUTPUT here and fill it in Execute. It carries the
				// fire timestamp, so threading it also forwards "when it fired".
				{Port: core.PassPort, Label: "Pass-through"},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}},
			},
			// interval_seconds lives on the node (like cron_trigger's schedule),
			// read by the scheduler. Max mirrors core.MaxPollIntervalSeconds
			// (366 days) — past it the duration math overflows; the scheduler
			// and lint reject it. Blank = manual-only, matching cron_trigger.
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"interval_seconds":{
						"type":"integer",
						"title":"Every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":300,
						"description":"Set this to run the flow automatically every N minutes/hours/days — e.g. every 5 minutes. Leave it empty and the flow never runs on its own; it runs only when you press Run."
					}
				}
			}`),
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
// Manual runs (e.g. `dzctl graph run` or the UI's Run button)
// produce a perfectly valid result, treated as a one-off fire of
// the poll graph. That's by design: the same graph should work
// whether the scheduler fired it or the user did, which is the
// natural mental model for "test this poll workflow now."
func executePollTrigger(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// pass carries the same fire moment as fired_at — it's the
			// sequencing wire AND forwards "when it fired" to whatever it
			// threads into. (ApplyPassthrough can't fill it: a trigger has no
			// pass INPUT to copy from, so we emit it directly.)
			core.PassPort: {MIME: "text/plain", Inline: now},
			"fired_at":    {MIME: "text/plain", Inline: now},
		},
	}, nil
}
