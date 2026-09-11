// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

// changePageCap bounds the paging one check will do. With orderBy=updated the
// first page already holds the oldest changes, and the page size is the limit
// itself, so a second page is only ever needed when Google returns short
// pages; the cap exists so a pathological feed cannot hold a check open.
const changePageCap = 5

// changeMaxResults is the API's own ceiling for maxResults on events.list.
const changeMaxResults = 2500

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gcal_on_event_change",
			Version:     "1.0",
			Label:       "Google Calendar",
			Subtitle:    "When an event changes",
			Summary:     "Fires when an event is booked, moved or cancelled in a Google Calendar.",
			Description: "Watches a Google Calendar and starts the flow when something in it changes — an event booked, moved, renamed or cancelled. The changed events come out as a list, ready for For each, and each one carries `change`: newly booked, changed or cancelled. A cancelled event still carries its details, so a flow can say what was called off.\n\nThe first check after you publish records where the calendar is up to and fires nothing, so switching the watch on doesn't replay the calendar's history. After that a check only sees what changed since the check before it.\n\nRecurring events are reported as the series, not as expanded instances: editing a weekly standup fires once, rather than once for every week it will ever run. Use \"Before an event starts\" when you want the individual bookings.\n\nGoogle's calendar notifications carry no details of their own and need a publicly verified callback to reach at all, so this polls instead — one API call per check, whether anything changed or not.",
			Integration: "Google Calendar",
			Category:    "trigger",
			Icon:        "calendar",
			BrandLogo:   "/brands/google-calendar.svg",
			Color:       "#4285F4",
			Provider:    "internal",
			Tags:        []string{"calendar", "google", "trigger", "poll", "booking", "meeting", "cancellation"},
			Examples: []core.ParamsExample{
				{
					Title:  "Anything that changes on my own calendar",
					Params: json.RawMessage(`{"account":"default","calendar_id":"primary","interval_seconds":300}`),
				},
				{
					Title:  "Only new bookings, on a shared calendar",
					Params: json.RawMessage(`{"account":"default","calendar_id":"team@example.com","change":"created","interval_seconds":900}`),
					Notes:  "A booking that is later moved or deleted fires nothing further — 'Newly booked' means the first time the event was seen.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — calendar.readonly scope."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "events", Label: "Changed events", MIME: []string{"application/json"}, Example: json.RawMessage(`[{"id":"6mbn8l9h1a","status":"confirmed","summary":"Design review","description":"Walk through the new canvas","location":"Room 2","html_link":"https://www.google.com/calendar/event?eid=6mbn8l9h1a","start":"2026-06-18T13:00:00+02:00","end":"2026-06-18T14:00:00+02:00","all_day":false,"attendees":["ada@example.com"],"change":"created","created":"2026-06-16T09:12:01.000Z","updated":"2026-06-16T09:12:04.512Z","recurring_event_id":""}]`)},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-06-16T09:15:00Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"calendar_id":{"type":"string","format":"google-calendar","title":"Calendar","default":"primary","description":"The calendar to read — pick from your account's calendars, or 'primary' for your own."},
					"change":{
						"type":"string",
						"title":"Which changes",
						"enum":["any","created","updated","cancelled"],
						"enumNames":["Any change","Newly booked","Changed","Cancelled"],
						"default":"any",
						"description":"Which changes are worth starting the flow for. A check costs the same either way — this decides what reaches the rest of the flow, and a check whose changes are all filtered out skips it."
					},
					"limit":{"type":"integer","title":"Max events","default":250,"minimum":1,"maximum":2500,"description":"About how many changed events one check takes. A check that finds more takes the oldest changes and leaves the rest for the next one, so nothing is dropped — it just arrives a check later."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":300,
						"description":"How often to check for changes once the flow is published. Leave blank to only check when you press Run (for testing)."
					},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			// A fire is a discrete poll of the change feed: rerunning reads
			// from the stored watermark rather than re-deriving a past fire.
			Idempotent: false,
		},
		Execute: executeOnEventChange,
	})
}

func executeOnEventChange(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	want := params.StringDefault(job.Params, "change", "any")
	switch want {
	case "any", "created", "updated", "cancelled":
	default:
		return params.Err(job, "bad_param", fmt.Sprintf("'change' must be any, created, updated or cancelled (got %q)", want)), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}
	calID := calendarID(job)
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 250), 1, changeMaxResults)

	name := fmt.Sprintf("cursor.gcal.change.%s.%s", job.GraphID, job.NodeID)
	last, rerr := cursor.Read(ctx, job.Tenant, name)
	if rerr != nil {
		// Without the watermark this check cannot tell a change it has already
		// reported from one it hasn't, and re-baselining would silently swallow
		// everything that changed since the last successful check.
		return cursor.FailRead(job, rerr), nil
	}
	since, ok := parseStamp(last)

	// First check after publishing — and the same for a watermark that no
	// longer parses, which is the one case where carrying on would mean
	// re-reporting the whole calendar. Record the moment and emit NOTHING, so
	// the watch starts from here.
	if !ok {
		if werr := cursor.Write(ctx, job.Tenant, name, nowStamp()); werr != nil {
			// Nothing fired, so there is no batch to lose — but left
			// unreported a persistent failure here would re-baseline for
			// ever and never fire anything, while looking healthy.
			return cursor.FailBaseline(job, werr), nil
		}
		params.EmitProgress(progress, job, 1, "baseline: noted where "+calID+" is up to — now watching it for changes")
		pollstate.Report(ctx, job, false)
		return nothingToReport(job), nil
	}

	rows, newest, err := fetchChanged(ctx, job, token, calID, last, limit)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}

	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		change := classifyChange(e, since)
		if want != "any" && want != change {
			continue
		}
		row := e.normalize()
		row["change"] = change
		row["created"] = e.Created
		row["updated"] = e.Updated
		row["recurring_event_id"] = e.RecurringEventID
		out = append(out, row)
	}

	// Advance past everything this check LOOKED at, filtered-out changes
	// included: they were seen, and the filter is a statement about what the
	// flow wants, not about what the watch has read.
	//
	// Best-effort from here: the changes are on their way downstream, so a
	// failed write means at worst the next check reports them again, which is
	// the safe direction. The baseline write above is the one that must not be
	// ignored, and it isn't.
	if newest != "" && newest != last {
		_ = cursor.Write(ctx, job.Tenant, name, newest)
	}

	pollstate.Report(ctx, job, len(out) > 0)
	if len(out) == 0 {
		return nothingToReport(job), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"events":   {MIME: "application/json", Inline: out},
			"count":    {MIME: "text/plain", Inline: strconv.Itoa(len(out))},
			"fired_at": {MIME: "text/plain", Inline: nowStamp()},
		},
	}, nil
}

// classifyChange labels a change from the event's own timestamps. A cancelled
// event says so in its status; otherwise the question is whether the event was
// created after the last check (so this is the first time it has been seen) or
// merely edited since.
func classifyChange(e rawEvent, since time.Time) string {
	if e.Status == "cancelled" {
		return "cancelled"
	}
	if created, ok := parseStamp(e.Created); ok && !created.Before(since) {
		return "created"
	}
	return "updated"
}

// fetchChanged reads the changes recorded after the watermark, oldest first,
// and returns them with the watermark to store next.
//
// updatedMin is INCLUSIVE, so the event sitting exactly on the watermark comes
// back on every check; the strict comparison below is what stops it being
// reported twice. showDeleted carries cancellations, which are the whole point
// of watching a calendar rather than listing it, and singleEvents=false keeps
// a recurring series one event instead of expanding an edit to the series into
// an instance per occurrence.
func fetchChanged(ctx context.Context, job core.Job, token, calID, since string, limit int) ([]rawEvent, string, error) {
	sinceT, ok := parseStamp(since)
	if !ok {
		return nil, "", fmt.Errorf("stored position %q is not a timestamp", since)
	}
	q := url.Values{}
	q.Set("updatedMin", since)
	q.Set("showDeleted", "true")
	q.Set("singleEvents", "false")
	q.Set("orderBy", "updated") // oldest change first, so a truncated check resumes cleanly
	q.Set("maxResults", strconv.Itoa(limit))

	taken := make([]rawEvent, 0, limit)
	pageToken := ""
	done := false
	for page := 0; page < changePageCap && !done; page++ {
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		items, next, err := eventsPage(ctx, job, token, calID, q)
		if err != nil {
			return nil, "", err
		}
		for _, it := range items {
			updated, uok := parseStamp(it.Updated)
			if !uok || !updated.After(sinceT) {
				continue // the event sitting on the watermark, already reported
			}
			// The limit is a soft one, and deliberately: the next check
			// resumes strictly PAST the newest stamp taken here, so cutting
			// inside a group of events that share a stamp — a script that
			// booked six meetings, an import — would skip the rest of that
			// group for ever. The cut is allowed to run past the limit until
			// the stamp changes, which with orderBy=updated is the first row
			// of the next group.
			if len(taken) >= limit && it.Updated != taken[len(taken)-1].Updated {
				done = true
				break
			}
			taken = append(taken, it)
		}
		if next == "" {
			break
		}
		pageToken = next
	}
	newest, newestT := since, sinceT
	for _, e := range taken {
		if t, tok := parseStamp(e.Updated); tok && t.After(newestT) {
			newest, newestT = e.Updated, t
		}
	}
	return taken, newest, nil
}
