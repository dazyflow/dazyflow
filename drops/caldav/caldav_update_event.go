// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
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
			ID:          "caldav_update_event",
			Version:     "1.0",
			Label:       "Calendar",
			Subtitle:    "Move or amend event",
			Summary:     "Change an event that's already on the calendar — reschedule it, rename it, move the room.",
			Description: "Change an event that's already on the calendar. Fill in only what should change: a new Start and End reschedules it, a new Summary renames it, a new Location moves the room. Anything you leave blank is left exactly as it is, so this is safe to use for a partial change — it reads the event first and writes it back amended, rather than replacing it with only the fields you supplied. Connect List events' Events into a For each and put this step in the loop body with Event = the row's id.",
			Integration: integration,
			Category:    "network",
			Icon:        "calendar-clock",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"calendar", "caldav", "events", "update", "reschedule", "move"},
			Examples: []core.ParamsExample{
				{
					Title:  "Push a booking back an hour",
					Params: json.RawMessage(`{"id":"${item.id}","start":"${item.start}","end":"${item.end}"}`),
					Notes:  "Compute the new times with a Date & time step and wire them into Start and End.",
				},
				{
					Title:  "Just move the room",
					Params: json.RawMessage(`{"id":"${item.id}","location":"Room 4"}`),
					Notes:  "Times, title and guests are untouched.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "id", Label: "Event", MIME: []string{"text/plain", "application/json"}},
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
				{Port: "start", Label: "Start", MIME: []string{"text/plain"}},
				{Port: "end", Label: "End", MIME: []string{"text/plain"}},
				{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
				{Port: "location", Label: "Location", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "event_id", Label: "Event ID", MIME: []string{"text/plain"}},
				{Port: "event", Label: "Event", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"calendar":{"type":"string","title":"Calendar","description":"Which calendar the event is on when the account has several. Leave blank to use the one set on the Calendar page."},
					"id":{"type":"string","title":"Event","description":"The event's id, as emitted by List events or Create event. Overridden by the 'Event' input when connected."},
					"summary":{"type":"string","title":"Summary","description":"New title. Leave blank to keep the current one."},
					"start":{"type":"string","title":"Start","examples":["tomorrow+9h"],"description":"New start. Accepts the same relative forms as the other calendar steps. Leave blank to keep the current one."},
					"end":{"type":"string","title":"End","description":"New end. Leave blank to keep the current one — or, if you moved the start and left this blank, the event keeps its original length."},
					"description":{"type":"string","title":"Description","format":"multiline","description":"New notes. Leave blank to keep the current ones."},
					"location":{"type":"string","title":"Location","description":"New location. Leave blank to keep the current one."},
					"tz":{"type":"string","format":"timezone","title":"Timezone","description":"IANA timezone relative times are resolved in. Empty = UTC."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["id"]
			}`),
			// Applying the same change twice leaves the event in the state the
			// first attempt produced — the write goes to the event's own path
			// and replaces it — so a retry is safe and needs no dedupe.
			Idempotent: true,
		},
		Execute: executeCalDAVUpdate,
	})
}

func executeCalDAVUpdate(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	loc, err := location(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
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

	// Read-modify-write, not replace. A step that wrote only the fields the
	// author filled in would silently drop the guest list, the description
	// and the recurrence rule off any event it touched — which is the kind of
	// data loss nobody notices until someone doesn't turn up.
	objPath, err := caldavutil.FindEventPath(ctx, client, dir, uid)
	if err != nil {
		return params.Err(job, "caldav_error", err.Error()), nil
	}
	if objPath == "" {
		return params.Err(job, "not_found", fmt.Sprintf("there's no event %q on this calendar — it may have been cancelled since the listing found it", uid)), nil
	}
	obj, err := client.GetCalendarObject(ctx, objPath)
	if err != nil || obj == nil || obj.Data == nil {
		return params.Err(job, "caldav_error", fmt.Sprintf("couldn't read event %q back before changing it: %v", uid, err)), nil
	}
	events := obj.Data.Events()
	if len(events) == 0 {
		return params.Err(job, "caldav_error", fmt.Sprintf("event %q holds no VEVENT to change", uid)), nil
	}
	event := &events[0]

	changed, jerr := applyEventChanges(job, event, loc)
	if jerr != nil {
		return *jerr, nil
	}
	if !changed {
		// Nothing to do is not a failure — a flow that only sometimes has a
		// change to make shouldn't have to branch around this step. The
		// current event goes out on the pins so downstream still sees it.
		return updateResult(job, uid, event, loc), nil
	}

	// DTSTAMP marks when the event was last assembled; SEQUENCE is how
	// iCalendar tells attendees' clients that a version is newer than the one
	// they hold. Without bumping it, some clients ignore the change.
	event.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	bumpSequence(event)

	if _, err := client.PutCalendarObject(ctx, objPath, obj.Data); err != nil {
		return params.Err(job, "caldav_error", fmt.Sprintf("couldn't save the change: %v", err)), nil
	}
	return updateResult(job, uid, event, loc), nil
}

func updateResult(job core.Job, uid string, event *ical.Event, loc *time.Location) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"event_id": {MIME: "text/plain", Inline: uid},
			"event":    {MIME: "application/json", Inline: eventRecord(event, loc)},
		},
	}
}

// applyEventChanges folds the step's filled-in fields onto an existing event
// and reports whether anything actually changed.
//
// A blank field means "leave it alone", not "clear it". That is the only
// reading that makes a partial change safe, and it costs the ability to blank
// a description — which a flow can do by writing a single space, and which is
// a far rarer thing to want than moving a booking.
func applyEventChanges(job core.Job, event *ical.Event, loc *time.Location) (bool, *core.Result) {
	changed := false

	for _, f := range []struct {
		port, param, prop string
	}{
		{"summary", "summary", ical.PropSummary},
		{"description", "description", ical.PropDescription},
		{"location", "location", ical.PropLocation},
	} {
		val, ok := params.TextInputOr(job, f.port, params.StringDefault(job.Params, f.param, ""))
		if !ok {
			res := params.Err(job, "bad_input", fmt.Sprintf("input port %q must be text", f.port))
			return false, &res
		}
		if strings.TrimSpace(val) == "" {
			continue
		}
		event.Props.SetText(f.prop, val)
		changed = true
	}

	now := time.Now()
	start, hasStart, err := resolveWindowEnd(job, "start", "start", loc, now)
	if err != nil {
		res := params.Err(job, "bad_param", err.Error())
		return false, &res
	}
	end, hasEnd, err := resolveWindowEnd(job, "end", "end", loc, now)
	if err != nil {
		res := params.Err(job, "bad_param", err.Error())
		return false, &res
	}

	// Moving the start without giving a new end keeps the event's LENGTH:
	// "push it back an hour" is the common ask, and re-deriving the end from
	// a default hour would silently shorten a two-hour meeting.
	if hasStart && !hasEnd {
		oldStart, sErr := event.DateTimeStart(loc)
		oldEnd, eErr := event.DateTimeEnd(loc)
		if sErr == nil && eErr == nil && oldEnd.After(oldStart) {
			end = start.Add(oldEnd.Sub(oldStart))
			hasEnd = true
		}
	}
	if hasStart {
		event.Props.SetDateTime(ical.PropDateTimeStart, start)
		changed = true
	}
	if hasEnd {
		if hasStart && !end.After(start) {
			res := params.Err(job, "bad_param", fmt.Sprintf("the event would end before it starts (%s to %s)", start.Format(time.RFC3339), end.Format(time.RFC3339)))
			return false, &res
		}
		event.Props.SetDateTime(ical.PropDateTimeEnd, end)
		changed = true
	}
	return changed, nil
}

// bumpSequence increments the event's SEQUENCE, which is iCalendar's version
// counter for a change other people's clients should pick up.
func bumpSequence(event *ical.Event) {
	next := 1
	if prop := event.Props.Get(ical.PropSequence); prop != nil {
		if n, err := prop.Int(); err == nil {
			next = n + 1
		}
	}
	event.Props.SetText(ical.PropSequence, fmt.Sprint(next))
}
