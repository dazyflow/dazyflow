// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gcal_delete_event",
			Version:     "1.0",
			Label:       "Google Calendar",
			Subtitle:    "Cancel event",
			Summary:     "Remove an event from a Google Calendar — a cancelled booking, a withdrawn request.",
			Description: "Remove an event from a Google Calendar. The other half of Create event: when a booking is cancelled or a time-off request is withdrawn, the entry has to come off the calendar too. Connect List events' Events into a For each and put this step in the loop body with Event = the row's id, or use the id an earlier Create event returned. An event that's already gone is not an error — the calendar is in the state you asked for either way.",
			Integration: "Google Calendar",
			Category:    "network",
			Icon:        "calendar-x",
			BrandLogo:   "/brands/google-calendar.svg",
			Color:       "#4285F4",
			Provider:    "internal",
			Tags:        []string{"calendar", "google", "events", "delete", "cancel"},
			Examples: []core.ParamsExample{
				{Title: "Cancel the booking a form withdrew", Params: json.RawMessage(`{"account":"default","calendar_id":"primary","id":"${item.id}"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — calendar.events scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "calendar_id", Label: "Calendar ID", MIME: []string{"text/plain"}},
				{Port: "id", Label: "Event", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"calendar_id":{"type":"string","format":"google-calendar","title":"Calendar","default":"primary","description":"The calendar the event is on — pick from your account's calendars, or 'primary' for your own."},
					"id":{"type":"string","title":"Event","description":"The event's id, as emitted by List events or Create event. Overridden by the 'Event' input when connected."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["id"]
			}`),
			// Removing an event twice leaves the calendar in the same state as
			// removing it once, so a retry is safe and needs no write-dedupe.
			Idempotent: true,
		},
		Execute: executeGcalDeleteEvent,
	})
}

func executeGcalDeleteEvent(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}
	id, ok := resolveGcalEventID(job)
	if !ok {
		return params.Err(job, "bad_input", "input port 'id' must be an event id or a list of events"), nil
	}
	if id == "" {
		return params.Err(job, "bad_param", "'id' is required — set it or connect the 'Event' input"), nil
	}
	cal := calendarID(job)

	endpoint := calBaseURL(job) + "/calendars/" + url.PathEscape(cal) + "/events/" + url.PathEscape(id)
	status, body, err := googleDo(ctx, "DELETE", endpoint, token, "", nil, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gcal_http_error", err.Error()), nil
	}

	// 410 Gone (and 404) mean the event isn't there. Not an error: the
	// calendar is in the state the flow asked for, and failing would break
	// the second run of a cancellation flow.
	removed := true
	switch {
	case status == 404 || status == 410:
		removed = false
	case status < 200 || status >= 300:
		return params.Err(job, "gcal_error", calErr(body)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id":       id,
				"calendar": cal,
				"removed":  removed,
			}},
		},
	}, nil
}

// resolveGcalEventID works out which event a step was pointed at: an id
// (text, e.g. ${item.id} inside a For each) or List events' record/list wired
// straight in, in which case the first entry is used. Same shape as the
// Mailbox and Calendar steps, so the idiom is one idiom.
func resolveGcalEventID(job core.Job) (string, bool) {
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
