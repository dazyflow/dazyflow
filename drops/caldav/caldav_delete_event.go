// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/caldavutil"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "caldav_delete_event",
			Version:     "1.0",
			Label:       "Calendar",
			Subtitle:    "Cancel event",
			Summary:     "Remove an event from the calendar — a cancelled booking, a withdrawn request.",
			Description: "Remove an event from the calendar. The other half of Create event: when a booking is cancelled or a time-off request is withdrawn, the entry has to come off the calendar too. Connect List events' Events into a For each and put this step in the loop body with Event = the row's id, or use the id an earlier Create event returned. An event that's already gone is not an error — the calendar is in the state you asked for either way.",
			Integration: integration,
			Category:    "network",
			Icon:        "calendar-x",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"calendar", "caldav", "events", "delete", "cancel"},
			Examples: []core.ParamsExample{
				{
					Title:  "Cancel the booking a form withdrew",
					Params: json.RawMessage(`{"id":"${item.id}"}`),
					Notes:  "The id is what List events and Create event both emit.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "id", Label: "Event", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}, Example: json.RawMessage(`{"id":"9f2ab7c4-d1e0-b7a3-8f12-4471e0b7a39f","calendar":"personal"}`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"calendar":{"type":"string","title":"Calendar","description":"Which calendar the event is on when the account has several. Leave blank to use the one set on the Calendar page."},
					"id":{"type":"string","title":"Event","description":"The event's id, as emitted by List events or Create event. Overridden by the 'Event' input when connected."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["id"]
			}`),
			// Removing an event twice leaves the calendar in the same state as
			// removing it once, so a retry is safe and needs no write-dedupe.
			// This is the difference from Create event, where a retry would
			// double-book.
			Idempotent: true,
		},
		Execute: executeCalDAVDelete,
	})
}

func executeCalDAVDelete(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	uid, ok := resolveEventID(job)
	if !ok {
		return params.Err(job, "bad_input", "input port 'id' must be an event id or a list of events"), nil
	}
	if uid == "" {
		return params.Err(job, "bad_param", "'id' is required — set it or connect the 'Event' input"), nil
	}

	timeout := time.Duration(params.TimeoutMS(job, 30000)) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := caldavutil.Client(cfg, timeout)
	if err != nil {
		return params.Err(job, "caldav_error", err.Error()), nil
	}
	dir, err := caldavutil.ResolveCalendar(ctx, client, cfg)
	if err != nil {
		return params.Err(job, "caldav_error", err.Error()), nil
	}

	objPath, err := caldavutil.FindEventPath(ctx, client, dir, uid)
	if err != nil {
		return params.Err(job, "caldav_error", err.Error()), nil
	}
	if objPath == "" {
		// Already gone. Not an error: the calendar is in the state the flow
		// asked for, and failing here would make a re-run of a cancellation
		// flow fail on the second pass.
		return deleteResult(job, uid, cfg.Calendar, false), nil
	}
	if err := client.RemoveAll(ctx, objPath); err != nil {
		return params.Err(job, "caldav_error", fmt.Sprintf("couldn't remove the event: %v", err)), nil
	}
	return deleteResult(job, uid, cfg.Calendar, true), nil
}

func deleteResult(job core.Job, uid, calendar string, removed bool) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id":       uid,
				"calendar": calendar,
				// Says which of the two happened, so a flow can report "3
				// cancelled, 1 already gone" rather than guessing.
				"removed": removed,
			}},
		},
	}
}

// resolveEventID works out which event a step was pointed at: an id (text,
// e.g. ${item.id} inside a For each) or List events' record/list wired
// straight in, in which case the first entry is used.
func resolveEventID(job core.Job) (string, bool) {
	fallback := strings.TrimSpace(params.StringDefault(job.Params, "id", ""))
	in, present := job.Input["id"]
	if !present || in.Inline == nil {
		return fallback, true
	}
	recordID := func(v any) string {
		m, isMap := v.(map[string]any)
		if !isMap {
			return ""
		}
		s, _ := m["id"].(string)
		return s
	}
	switch v := in.Inline.(type) {
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return s, true
		}
		return fallback, true
	case []byte:
		if s := strings.TrimSpace(string(v)); s != "" {
			return s, true
		}
		return fallback, true
	case map[string]any:
		if s := recordID(v); s != "" {
			return s, true
		}
		return "", false
	case []any:
		for _, item := range v {
			if s := recordID(item); s != "" {
				return s, true
			}
		}
		return "", false
	default:
		return "", false
	}
}
