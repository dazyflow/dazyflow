// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	dav "github.com/emersion/go-webdav/caldav"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/caldavutil"
	"github.com/dazyflow/dazyflow/pollstate"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "caldav_list_events",
			Version:     "1.0",
			Label:       "Calendar",
			Subtitle:    "List events",
			Summary:     "List events from any calendar that isn't Google's — Fastmail, iCloud, Nextcloud, your own server.",
			Description: "List events from a calendar over CalDAV, which nearly every calendar except Google's speaks. Bound it to a time window that moves with the schedule — \"tomorrow\" to \"tomorrow+1d\" for tomorrow's bookings, \"-7d\" to \"now\" for last week — or give absolute timestamps; both ends can also be connected in. Each event comes out with id, summary, description, location, start/end, status and attendees, in start-time order, ready to loop over with For each and text or email each person.",
			Integration: integration,
			Category:    "network",
			Icon:        "calendar",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"calendar", "caldav", "events", "list", "bookings", "reminders"},
			Examples: []core.ParamsExample{
				{
					Title:  "Tomorrow's bookings, for a reminder run",
					Params: json.RawMessage(`{"time_min":"tomorrow","time_max":"tomorrow+1d","tz":"Europe/Stockholm"}`),
					Notes:  "Day boundaries are taken in the given timezone, so a nightly schedule always picks up exactly the next day.",
				},
				{
					Title:  "What's on this week",
					Params: json.RawMessage(`{"time_min":"now","time_max":"+7d","limit":50}`),
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// Both ends of the window take a wire, so it can be computed
				// upstream (a Date step, a row's own field) — same as the
				// Google Calendar step.
				{Port: "time_min", Label: "Start of window", MIME: []string{"text/plain"}},
				{Port: "time_max", Label: "End of window", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "events", Label: "Events", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"calendar":{"type":"string","title":"Calendar","description":"Which calendar to read when the account has several. Leave blank to use the one set on the Calendar page."},
					"time_min":{"type":"string","title":"Start of window","examples":["tomorrow","-7d","2026-06-16T00:00:00Z"],"description":"Only events ending at or after this time. Accepts a relative value — now, today, tomorrow, yesterday, +3d, -2h30m, tomorrow+9h — or an absolute timestamp. Leave blank for no lower bound."},
					"time_max":{"type":"string","title":"End of window","examples":["tomorrow+1d","now","2026-06-23T00:00:00Z"],"description":"Only events starting before this time. Same forms as the start of the window. Leave blank for no upper bound."},
					"tz":{"type":"string","format":"timezone","title":"Timezone","description":"IANA timezone the day boundaries of \"today\"/\"tomorrow\" are taken in, e.g. \"Europe/Stockholm\". Empty = UTC. Ignored for absolute timestamps."},
					"limit":{"type":"integer","title":"Max events","default":50,"minimum":1,"maximum":500,"description":"How many events to bring back at most, earliest first."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeCalDAVList,
	})
}

func executeCalDAVList(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	loc, err := location(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	now := time.Now()
	start, hasStart, err := resolveWindowEnd(job, "time_min", "time_min", loc, now)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	end, hasEnd, err := resolveWindowEnd(job, "time_max", "time_max", loc, now)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if hasStart && hasEnd && !end.After(start) {
		return params.Err(job, "bad_param", fmt.Sprintf("the window ends before it starts (%s to %s)", start.Format(time.RFC3339), end.Format(time.RFC3339))), nil
	}
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 50), 1, 500)

	timeout := time.Duration(params.TimeoutMS(job, 30000)) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, err := caldavutil.Client(cfg, timeout)
	if err != nil {
		return params.Err(job, "caldav_error", err.Error()), nil
	}
	path, err := caldavutil.ResolveCalendar(ctx, client, cfg)
	if err != nil {
		return params.Err(job, "caldav_error", err.Error()), nil
	}

	// A time-range filter on the VEVENT component. The server does the
	// window, including expanding recurring events into the instances that
	// fall inside it — which is why this is a REPORT query rather than
	// fetching everything and filtering here.
	comp := dav.CompFilter{Name: "VEVENT"}
	if hasStart {
		comp.Start = start
	}
	if hasEnd {
		comp.End = end
	}
	query := &dav.CalendarQuery{
		CompRequest: dav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Props: []string{"VERSION"},
			Comps: []dav.CalendarCompRequest{{
				Name:  "VEVENT",
				Props: []string{"SUMMARY", "UID", "DESCRIPTION", "LOCATION", "STATUS", "DTSTART", "DTEND", "ATTENDEE"},
			}},
		},
		CompFilter: dav.CompFilter{
			Name:  "VCALENDAR",
			Comps: []dav.CompFilter{comp},
		},
	}
	objects, err := client.QueryCalendar(ctx, path, query)
	if err != nil {
		return params.Err(job, "caldav_error", fmt.Sprintf("couldn't read the calendar: %v", err)), nil
	}

	rows := make([]map[string]any, 0, len(objects))
	for _, obj := range objects {
		if obj.Data == nil {
			continue
		}
		for _, event := range obj.Data.Events() {
			rows = append(rows, eventRecord(&event, loc))
		}
	}
	// Earliest first. CalDAV makes no ordering promise — the events come back
	// in whatever order the server's collection walk produced — so a flow
	// sending reminders in order has to be given one.
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := rows[i]["start"].(string)
		b, _ := rows[j]["start"].(string)
		if a != b {
			return a < b
		}
		as, _ := rows[i]["summary"].(string)
		bs, _ := rows[j]["summary"].(string)
		return as < bs
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}

	pollstate.Report(ctx, job, len(rows) > 0)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"events": {MIME: "application/json", Inline: rows},
			"count":  {MIME: "text/plain", Inline: fmt.Sprint(len(rows))},
		},
	}, nil
}
