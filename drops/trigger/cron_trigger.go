// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package trigger

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "cron_trigger",
			Version:     "1.0",
			Label:       "Schedule",
			Icon:        "clock",
			Category:    "trigger",
			Provider:    "internal",
			Tags:        []string{"cron", "schedule", "trigger", "daily", "recurring", "timer"},
			Description: "Starts the flow on a recurring schedule — pick daily, weekly, monthly or hourly on the step (a custom cron expression also works). The Time output is when it fired. With no schedule set, the flow runs only when you press Run.",
			Summary:     "Starts the flow on a schedule — daily, weekly, monthly or hourly.",
			Examples: []core.ParamsExample{
				{
					Title:  "Every day at 09:00",
					Params: json.RawMessage(`{"cron":"0 9 * * *","tz":"Europe/Stockholm"}`),
					Notes:  "5-field cron, read in the given time zone. The fire time comes out on the 'Time' output.",
				},
				{
					Title:  "Every 15 minutes",
					Params: json.RawMessage(`{"cron":"*/15 * * * *"}`),
					Notes:  "No time zone = UTC.",
				},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				// Primary output: the untyped pass pin that snaps to the next
				// drop's Pass-through input (triangle → triangle) to sequence
				// the flow, so you don't have to drag the typed Time value into
				// a generic pin. See poll_trigger for the full rationale; it
				// carries the fire timestamp too.
				{Port: core.PassPort, Label: "Pass-through"},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}},
			},
			// cron + tz live on the node (Phase 2: schedule config is on the
			// entry point). Neither is required — a blank schedule means
			// "manual only", which the trigger lint flags as a soft warning
			// rather than a hard error so a half-built flow still saves.
			ParamsSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"cron": {
						"type": "string",
						"title": "Runs",
						"format": "cron",
						"default": "0 9 * * *",
						"description": "Set this to run the flow automatically on a schedule — picked with the schedule editor. (Stored as a 5-field cron expression; \"0 9 * * *\" = every day at 09:00.) Leave it empty and the flow never runs on its own; it runs only when you press Run.",
						"examples": ["0 9 * * *", "*/15 * * * *", "0 8 * * 1"]
					},
					"tz": {
						"type": "string",
						"title": "Time zone",
						"format": "timezone",
						"description": "IANA time zone the schedule is read in, e.g. \"Europe/Stockholm\". Empty = UTC. The editor stamps your browser's zone here automatically; search the list to change it.",
						"x_advanced": true
					}
				}
			}`),
			// Retry-safe but operationally meaningless — a schedule fire is
			// a discrete event; rerunning doesn't re-derive a "fired at"
			// from the original tick. Mirrors poll_trigger.
			Idempotent: false,
		},
		Execute: executeCronTrigger,
	})
}

// executeCronTrigger emits the current timestamp, exactly like
// poll_trigger. The scheduler fires the whole graph on the graph's cron
// schedule; this node runs as a root and stamps the fire moment so
// downstream steps can read when they ran. Manual runs (the Run button
// or 'dzctl graph run') produce a valid one-off fire — the same graph
// behaves identically whether the scheduler or a user fired it, which is
// the natural mental model for "test this scheduled workflow now."
func executeCronTrigger(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// Stamp the fire moment in the schedule's OWN time zone (the tz set on
	// this node — the editor auto-stamps the author's browser zone), so
	// reading fired_at downstream shows the wall-clock time the schedule
	// fired at (e.g. 09:00+02:00), not a UTC value the author has to convert
	// in their head. Empty/invalid tz falls back to UTC, matching how the
	// scheduler interprets a zone-less schedule. Still RFC3339 — just with an
	// offset instead of "Z" — so downstream time parsing is unaffected.
	// (poll_trigger stays UTC: it has no zone and its tests pin that.)
	now := time.Now().UTC()
	if tz, _ := job.Params["tz"].(string); strings.TrimSpace(tz) != "" {
		if loc, err := time.LoadLocation(strings.TrimSpace(tz)); err == nil {
			now = now.In(loc)
		}
	}
	stamp := now.Format(time.RFC3339)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			// pass mirrors fired_at (see poll_trigger): the sequencing wire
			// that also forwards the fire moment in the schedule's own zone.
			core.PassPort: {MIME: "text/plain", Inline: stamp},
			"fired_at":    {MIME: "text/plain", Inline: stamp},
		},
	}, nil
}
