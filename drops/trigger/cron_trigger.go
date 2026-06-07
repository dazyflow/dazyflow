package trigger

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
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
			Description: "Fires the graph on a recurring schedule. Set the cron expression (and optional time zone) right here on the node — the scheduler reads them and runs the flow at each due time. Outputs `fired_at` (RFC3339 timestamp). Leave the schedule blank to run only on demand; running manually (Run button / 'hzctl graph run') fires it once, stamping the current time.",
			Summary:     "Trigger that fires the graph on the cron schedule set on this node, emitting the fire timestamp.",
			Examples: []core.ParamsExample{
				{
					Title:  "Every day at 09:00",
					Params: json.RawMessage(`{"cron":"0 9 * * *","tz":"Europe/Stockholm"}`),
					Notes:  "5-field cron, read in the given IANA time zone. Emits 'fired_at' downstream.",
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
				{Port: "fired_at", Label: "RFC3339 timestamp of this fire", MIME: []string{"text/plain"}},
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
						"title": "Schedule (cron)",
						"default": "0 9 * * *",
						"description": "5-field cron expression — minute hour day-of-month month day-of-week. \"0 9 * * *\" = every day at 09:00. Leave blank to run only when you press Run.",
						"examples": ["0 9 * * *", "*/15 * * * *", "0 8 * * 1"]
					},
					"tz": {
						"type": "string",
						"title": "Time zone",
						"description": "IANA time zone the schedule is read in, e.g. \"Europe/Stockholm\". Empty = UTC. The editor stamps your browser's zone here automatically.",
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
// or 'hzctl graph run') produce a valid one-off fire — the same graph
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
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"fired_at": {MIME: "text/plain", Inline: now.Format(time.RFC3339)},
		},
	}, nil
}
