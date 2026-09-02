// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/caldavutil"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "caldav_create_event",
			Version:     "1.0",
			Label:       "Calendar",
			Subtitle:    "Create event",
			Summary:     "Put an event on any calendar that isn't Google's — a booking, an intro call, a reminder.",
			Description: "Create an event on a calendar over CalDAV. Every field a booking varies by — when, who, where, what — takes a connection as well as a typed value, because the point of booking from a flow is that those came from a form or a row. Start and End accept the same relative forms as the listing (\"tomorrow+9h\"), so a slot can be computed rather than typed. Attendees are comma-separated addresses; whether they get an invitation email is up to the calendar server, not this step. Turn on \"All-day event\" for a holiday or a deadline; otherwise a plain date like \"2026-06-16\" makes a timed event starting at midnight.",
			Integration: integration,
			Category:    "network",
			Icon:        "calendar-plus",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"calendar", "caldav", "events", "create", "booking"},
			Examples: []core.ParamsExample{
				{
					Title:  "Book an intro call from a form submission",
					Params: json.RawMessage(`{"summary":"Intro call — ${item.name}","start":"tomorrow+9h","end":"tomorrow+10h","tz":"Europe/Stockholm"}`),
					Notes:  "Wire the form's fields into Summary and Attendees rather than typing them.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// Every field a booking actually varies by takes a wire, not
				// just a typed value — same shape as the Google Calendar step,
				// so a flow swaps between them without rewiring.
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
				{Port: "start", Label: "Start", MIME: []string{"text/plain"}},
				{Port: "end", Label: "End", MIME: []string{"text/plain"}},
				{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
				{Port: "location", Label: "Location", MIME: []string{"text/plain"}},
				{Port: "attendees", Label: "Attendees", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "event_id", Label: "Event ID", MIME: []string{"text/plain"}},
				{Port: "event", Label: "Event", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"calendar":{"type":"string","title":"Calendar","description":"Which calendar to write to when the account has several. Leave blank to use the one set on the Calendar page."},
					"summary":{"type":"string","title":"Summary","description":"The event's title, as it appears in the calendar. Overridden by the 'Summary' input."},
					"start":{"type":"string","title":"Start","examples":["tomorrow+9h","2026-06-16T09:00:00Z"],"description":"When it starts. Accepts a relative value — now, tomorrow+9h, +2h — or an absolute timestamp. Overridden by the 'Start' input."},
					"end":{"type":"string","title":"End","examples":["tomorrow+10h"],"description":"When it ends. Same forms as Start. Leave blank for an hour after the start."},
					"description":{"type":"string","title":"Description","format":"multiline","description":"Longer notes on the event. Overridden by the 'Description' input."},
					"location":{"type":"string","title":"Location","description":"Where it is — a room, an address, a call link. Overridden by the 'Location' input."},
					"attendees":{"type":"string","title":"Attendees","description":"Comma-separated email addresses to invite. Whether they receive an invitation is up to the calendar server. Overridden by the 'Attendees' input."},
					"all_day":{"type":"boolean","title":"All-day event","default":false,"description":"Make it an all-day entry rather than a timed one — a holiday, a deadline, a delivery date. The time of day in Start and End is ignored; End is the day AFTER the last day, which is how calendars represent a span."},
					"tz":{"type":"string","format":"timezone","title":"Timezone","description":"IANA timezone relative times are resolved in, e.g. \"Europe/Stockholm\". Empty = UTC."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["summary","start"]
			}`),
			// Creating an event is not idempotent in the general sense — run
			// the step twice and someone has two bookings — so it opts into
			// engine-side dedupe, exactly as gcal_create_event and the send
			// steps do: an expired-lease reclaim or crash recovery replays
			// the recorded result instead of writing again.
			//
			// Retries are deliberately NOT turned off, unlike the send steps.
			// The event's path on the server is built from a UID this step
			// mints, so a retried PUT overwrites the same event rather than
			// adding a second — the protocol makes the write safe to repeat
			// in a way an SMTP send never is.
			Idempotent:   false,
			DedupeWrites: true,
		},
		Execute: executeCalDAVCreate,
	})
}

func executeCalDAVCreate(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	loc, err := location(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	summary, ok := params.TextInputOr(job, "summary", params.StringDefault(job.Params, "summary", ""))
	if !ok {
		return params.Err(job, "bad_input", "input port 'summary' must be text"), nil
	}
	if summary = strings.TrimSpace(summary); summary == "" {
		return params.Err(job, "bad_param", "'summary' is required — an event with no title is unreadable in a calendar"), nil
	}

	now := time.Now()
	start, hasStart, err := resolveWindowEnd(job, "start", "start", loc, now)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if !hasStart {
		return params.Err(job, "bad_param", "'start' is required — set it or connect the 'Start' input"), nil
	}
	end, hasEnd, err := resolveWindowEnd(job, "end", "end", loc, now)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	allDay := params.BoolDefault(job.Params, "all_day", false)
	if !hasEnd {
		if allDay {
			// One day, expressed the way iCalendar wants it: DTEND is the day
			// AFTER the last day of the span.
			end = start.AddDate(0, 0, 1)
		} else {
			// An hour is the convention every calendar UI uses for a new
			// event, and a zero-length event renders as a point most clients
			// hide.
			end = start.Add(time.Hour)
		}
	}
	// An all-day event is not a timed one with a convenient duration: it is a
	// date-VALUED DTSTART (DTSTART;VALUE=DATE:20260616). The Google step gets
	// there by passing a plain date through untouched, which this step can't
	// do because CalDAV needs a real time.Time — hence the explicit switch
	// rather than an inference from the input's shape. Inferring would also
	// be wrong: "tomorrow" is a date and almost never means all day.
	if !end.After(start) {
		return params.Err(job, "bad_param", fmt.Sprintf("the event ends before it starts (%s to %s)", start.Format(time.RFC3339), end.Format(time.RFC3339))), nil
	}

	description, ok := params.TextInputOr(job, "description", params.StringDefault(job.Params, "description", ""))
	if !ok {
		return params.Err(job, "bad_input", "input port 'description' must be text"), nil
	}
	where, ok := params.TextInputOr(job, "location", params.StringDefault(job.Params, "location", ""))
	if !ok {
		return params.Err(job, "bad_input", "input port 'location' must be text"), nil
	}
	guests, ok := params.TextInputOr(job, "attendees", params.StringDefault(job.Params, "attendees", ""))
	if !ok {
		return params.Err(job, "bad_input", "input port 'attendees' must be text"), nil
	}

	uid, err := newUID()
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	cal := buildCalendar(uid, summary, description, where, guests, start, end, allDay)

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

	// CalDAV writes an event to a path the CLIENT chooses, so the UID doubles
	// as the filename — which is what makes the engine's write-dedupe work:
	// a replayed run with the same recorded result cannot land a second copy
	// under a different name.
	objPath := caldavutil.EventPath(dir, uid)
	if _, err := client.PutCalendarObject(ctx, objPath, cal); err != nil {
		return params.Err(job, "caldav_error", fmt.Sprintf("couldn't add the event: %v", err)), nil
	}

	event := cal.Events()[0]
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"event_id": {MIME: "text/plain", Inline: uid},
			"event":    {MIME: "application/json", Inline: eventRecord(&event, loc)},
		},
	}, nil
}

// buildCalendar assembles the VCALENDAR one event goes out in.
func buildCalendar(uid, summary, description, where, guests string, start, end time.Time, allDay bool) *ical.Calendar {
	event := ical.NewEvent()
	event.Props.SetText(ical.PropUID, uid)
	// DTSTAMP is when the event was created, and is required by RFC 5545 —
	// some servers reject an event without it rather than filling it in.
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	event.Props.SetText(ical.PropSummary, summary)
	if allDay {
		// SetDate writes VALUE=DATE, which is the whole difference between an
		// all-day entry and a midnight-to-midnight timed one.
		event.Props.SetDate(ical.PropDateTimeStart, start)
		event.Props.SetDate(ical.PropDateTimeEnd, end)
	} else {
		event.Props.SetDateTime(ical.PropDateTimeStart, start)
		event.Props.SetDateTime(ical.PropDateTimeEnd, end)
	}
	if description != "" {
		event.Props.SetText(ical.PropDescription, description)
	}
	if where != "" {
		event.Props.SetText(ical.PropLocation, where)
	}
	for _, addr := range strings.Split(guests, ",") {
		if addr = strings.TrimSpace(addr); addr != "" {
			// Addresses go out as mailto: URIs, which is the only form RFC
			// 5545 defines for an ATTENDEE — a bare address is silently
			// dropped by some clients.
			event.Props.Add(&ical.Prop{Name: ical.PropAttendee, Value: "mailto:" + addr})
		}
	}

	cal := ical.NewCalendar()
	// PRODID and VERSION are both required. A missing VERSION is the more
	// common reason a server refuses a hand-built VCALENDAR.
	cal.Props.SetText(ical.PropProductID, "-//Dazyflow//Dazyflow//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Children = append(cal.Children, event.Component)
	return cal
}

// newUID mints the event's unique identifier, which also becomes its filename
// on the server.
func newUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("couldn't generate an event id: %w", err)
	}
	return "dazyflow-" + hex.EncodeToString(b[:]), nil
}
