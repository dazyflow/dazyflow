// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
			ID:          "gcal_create_event",
			Version:     "1.0",
			Label:       "Google Calendar",
			Subtitle:    "Create event",
			Summary:     "Create an event on a Google Calendar.",
			Description: "Create an event on a Google Calendar. Provide a summary plus start and end times. Use RFC3339 timestamps (2026-06-16T15:00:00Z) for a timed event, or plain dates (2026-06-16) for an all-day event. Attendees is an optional comma-separated list of email addresses.",
			Integration: "Google Calendar",
			Category:    "network",
			Icon:        "calendar-plus",
			BrandLogo:   "/brands/google-calendar.svg",
			Color:       "#4285F4",
			Provider:    "internal",
			Tags:        []string{"calendar", "google", "events", "create"},
			Examples: []core.ParamsExample{
				{Title: "One-hour meeting", Params: json.RawMessage(`{"account":"default","calendar_id":"primary","summary":"Sync","start":"2026-06-16T15:00:00Z","end":"2026-06-16T16:00:00Z"}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — calendar.events scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Every field a booking actually varies by takes a wire, not
				// just a typed value: the whole point of creating an event
				// from a flow is that the when/who/where came from the form,
				// the row or the message that started it.
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
				{Port: "start", Label: "Start", MIME: []string{"text/plain"}},
				{Port: "end", Label: "End", MIME: []string{"text/plain"}},
				{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
				{Port: "location", Label: "Location", MIME: []string{"text/plain"}},
				{Port: "attendees", Label: "Attendees", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "event_id", Label: "Event ID", MIME: []string{"text/plain"}, Example: json.RawMessage(`"7f2ab9c4d1e0b7a3"`)},
				{Port: "html_link", Label: "Link", MIME: []string{"text/plain"}, Example: json.RawMessage(`"https://www.google.com/calendar/event?eid=N2YyYWI5YzRkMWUwYjdhMw"`)},
				{Port: "event", Label: "Event", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"calendar_id":{"type":"string","format":"google-calendar","title":"Calendar","default":"primary","description":"The calendar to add the event to — pick from your account's calendars, or 'primary' for your own."},
					"summary":{"type":"string","title":"Title","description":"Event title."},
					"description":{"type":"string","title":"Description","description":"Optional event details."},
					"location":{"type":"string","title":"Location","description":"Optional location text."},
					"start":{"type":"string","title":"Start","examples":["2026-06-16T15:00:00Z","2026-06-16","tomorrow+9h"],"description":"When it starts: a timestamp, a plain date for an all-day event, or a relative value like \"tomorrow+9h\". Overridden by the Start input when connected."},
					"end":{"type":"string","title":"End","examples":["2026-06-16T16:00:00Z","2026-06-17","tomorrow+10h"],"description":"When it ends: a timestamp, a plain date (exclusive) for an all-day event, or a relative value. Overridden by the End input when connected."},
					"time_zone":{"type":"string","title":"Time zone","examples":["America/New_York","UTC"],"description":"IANA time zone for timed events. Optional when the timestamp carries an offset."},
					"attendees":{"type":"string","title":"Attendees","description":"Comma-separated attendee email addresses."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["summary","start","end"]
			}`),
			Idempotent: false,
			// events.insert is a non-idempotent POST (a retry creates a
			// second event). No RetryPolicy is set, so the engine only
			// retries this via an explicit OnErrorRetry edge — not the
			// auto-backoff path — so we leave the policy unset rather than
			// forcing RetryNever. If auto-retry is ever wanted here, thread
			// a request id (events.insert accepts a client-supplied id)
			// before turning on backoff.
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Independent of
			// RetryPolicy — dedupe covers re-execution of the SAME job record,
			// which a reclaim causes regardless of the retry setting.
			DedupeWrites: true,
		},
		Execute: executeCreateEvent,
	})
}

func executeCreateEvent(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	summary := resolveSummary(job)
	if summary == "" {
		return params.Err(job, "bad_param", "'summary' is required"), nil
	}
	// Each field: the wired input wins, the typed setting is the fallback.
	field := func(port string) (string, *core.Result) {
		v, ok := params.TextInputOr(job, port, params.StringDefault(job.Params, port, ""))
		if !ok {
			r := params.Err(job, "bad_input", fmt.Sprintf("the %q input must be text", port))
			return "", &r
		}
		return strings.TrimSpace(v), nil
	}
	start, bad := field("start")
	if bad != nil {
		return *bad, nil
	}
	end, bad := field("end")
	if bad != nil {
		return *bad, nil
	}
	if start == "" || end == "" {
		return params.Err(job, "bad_param", "'start' and 'end' are required"), nil
	}
	// A relative value ("tomorrow+9h") becomes a concrete timestamp; an
	// absolute one is left exactly as written, so a plain date still means an
	// all-day event rather than midnight.
	loc := time.UTC
	if tzName := strings.TrimSpace(params.StringDefault(job.Params, "time_zone", "")); tzName != "" {
		if l, lerr := time.LoadLocation(tzName); lerr == nil {
			loc = l
		} else {
			return params.Err(job, "bad_param", fmt.Sprintf("time_zone: unknown timezone %q", tzName)), nil
		}
	}
	now := time.Now()
	for name, val := range map[string]*string{"start": &start, "end": &end} {
		if !reltime.IsRelative(*val) {
			continue
		}
		resolved, rerr := reltime.ResolveRFC3339(*val, loc, now)
		if rerr != nil {
			return params.Err(job, "bad_param", fmt.Sprintf("%s: %v", name, rerr)), nil
		}
		*val = resolved
	}
	descr, bad := field("description")
	if bad != nil {
		return *bad, nil
	}
	location, bad := field("location")
	if bad != nil {
		return *bad, nil
	}
	attendeesRaw, bad := field("attendees")
	if bad != nil {
		return *bad, nil
	}

	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}

	tz := strings.TrimSpace(params.StringDefault(job.Params, "time_zone", ""))
	event := map[string]any{
		"summary": summary,
		"start":   eventTimeField(start, tz),
		"end":     eventTimeField(end, tz),
	}
	if descr != "" {
		event["description"] = descr
	}
	if location != "" {
		event["location"] = location
	}
	if att := parseAttendees(attendeesRaw); len(att) > 0 {
		event["attendees"] = att
	}

	body, err := json.Marshal(event)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}
	endpoint := calBaseURL(job) + "/calendars/" + url.PathEscape(calendarID(job)) + "/events"
	status, respBody, err := googleDo(ctx, "POST", endpoint, token, "application/json", body, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gcal_error", calErr(respBody)), nil
	}

	var created rawEvent
	if err := json.Unmarshal(respBody, &created); err != nil {
		return params.Err(job, "gcal_error", fmt.Sprintf("events.insert decode: %v", err)), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"event_id":  {MIME: "text/plain", Inline: created.ID},
			"html_link": {MIME: "text/plain", Inline: created.HTMLLink},
			"event":     {MIME: "application/json", Inline: created.normalize()},
		},
	}, nil
}

// resolveSummary prefers a wired 'summary' input port over the param.
func resolveSummary(job core.Job) string {
	if in, ok := job.Input["summary"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case []byte:
			if s := strings.TrimSpace(string(v)); s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(params.StringDefault(job.Params, "summary", ""))
}

// eventTimeField builds the Calendar API start/end object. A value containing
// "T" is treated as a timed instant ({dateTime[, timeZone]}); otherwise it's an
// all-day date ({date}). timeZone is attached only to timed events, and only
// when supplied.
func eventTimeField(value, tz string) map[string]any {
	if strings.Contains(value, "T") {
		f := map[string]any{"dateTime": value}
		if tz != "" {
			f["timeZone"] = tz
		}
		return f
	}
	return map[string]any{"date": value}
}

// parseAttendees splits a comma-separated address list into the API's
// [{email}] form, trimming whitespace and dropping blanks.
func parseAttendees(raw string) []map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []map[string]any
	for _, part := range strings.Split(raw, ",") {
		email := strings.TrimSpace(part)
		if email == "" {
			continue
		}
		out = append(out, map[string]any{"email": email})
	}
	return out
}
