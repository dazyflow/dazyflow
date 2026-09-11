// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

const (
	// startSeenCap bounds the remembered-reminder list. Every entry is an
	// event whose start has already been announced, so entries age out of
	// relevance on their own — the cap only has to outlast the lead window,
	// and 1000 announced starts is months of a busy calendar.
	startSeenCap = 1000

	// startMaxLeadMinutes is a day. Longer notice is a different shape of
	// flow — a Schedule step reading a whole week with List events — and the
	// cap also bounds what the first check after publishing can reach.
	startMaxLeadMinutes = 1440

	// startMaxGrace caps how late a reminder may still be sent when a check
	// lands behind schedule. Past it, "your meeting is starting" is noise.
	startMaxGrace = 15 * time.Minute

	startMaxResults = 2500
	startPageCap    = 5
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gcal_on_event_start",
			Version:     "1.0",
			Label:       "Google Calendar",
			Subtitle:    "Before an event starts",
			Summary:     "Fires shortly before an event in a Google Calendar begins.",
			Description: "Starts the flow a set time before an event in a Google Calendar begins — ten minutes before a meeting, an hour before a delivery window, the evening before a booking. The events about to start come out as a list, ready for For each, each one carrying `minutes_until_start` alongside its title, time, place and attendees.\n\nEach booking is announced once. One that is moved is announced again for its new time, because the reminder worth having is the one for when the thing actually happens. Cancelled events are left alone.\n\nKeep the check interval no longer than the notice you ask for, or an event can be reached after it has already begun. A check that never happened is not made up for either: an event whose start has slipped further than a quarter of an hour into the past is left alone rather than announced late, so a daemon that was down overnight doesn't wake up and page you about yesterday's meetings.\n\nAll-day events have no clock time to count back from, so they are left out unless you ask for them; asked for, they count from midnight in the timezone you name.\n\nUnlike \"When an event changes\", the first check after publishing does fire — it reaches whatever already sits inside the notice window, usually the next event or two. The alternative is publishing at nine and silently missing the half-past-nine meeting.",
			Integration: "Google Calendar",
			Category:    "trigger",
			Icon:        "calendar-clock",
			BrandLogo:   "/brands/google-calendar.svg",
			Color:       "#4285F4",
			Provider:    "internal",
			Tags:        []string{"calendar", "google", "trigger", "poll", "reminder", "meeting", "upcoming"},
			Examples: []core.ParamsExample{
				{
					Title:  "Ten minutes before every meeting",
					Params: json.RawMessage(`{"account":"default","calendar_id":"primary","lead_minutes":10,"interval_seconds":300}`),
					Notes:  "Five-minute checks with ten minutes' notice: every meeting is reached between five and ten minutes ahead.",
				},
				{
					Title:  "The night before a booking",
					Params: json.RawMessage(`{"account":"default","calendar_id":"bookings@example.com","lead_minutes":720,"include_all_day":true,"tz":"Europe/Stockholm","interval_seconds":1800}`),
					Notes:  "Twelve hours' notice reaches a 09:00 booking at 21:00 the evening before. All-day bookings count from midnight in the timezone given.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — calendar.readonly scope."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "events", Label: "Starting soon", MIME: []string{"application/json"}, Example: json.RawMessage(`[{"id":"6mbn8l9h1a","status":"confirmed","summary":"Design review","description":"Walk through the new canvas","location":"Room 2","html_link":"https://www.google.com/calendar/event?eid=6mbn8l9h1a","start":"2026-06-18T13:00:00+02:00","end":"2026-06-18T14:00:00+02:00","all_day":false,"attendees":["ada@example.com"],"minutes_until_start":8,"recurring_event_id":"6mbn8l9h1a_R20260601T110000"}]`)},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"1"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-06-18T12:52:00Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","default":"default"},
					"calendar_id":{"type":"string","format":"google-calendar","title":"Calendar","default":"primary","description":"The calendar to read — pick from your account's calendars, or 'primary' for your own."},
					"lead_minutes":{
						"type":"integer",
						"title":"Notice",
						"default":10,
						"minimum":0,
						"maximum":1440,
						"description":"How long before the start to fire, in minutes. 0 fires as the event begins. A day is the most that can be asked for; longer notice is better built as a Schedule step reading the week ahead with List events."
					},
					"include_all_day":{"type":"boolean","title":"Include all-day events","default":false,"description":"All-day events have no start time of their own. Include them and they count from midnight, which means a day's notice reaches them at midnight the night before."},
					"tz":{"type":"string","format":"timezone","title":"Timezone","x_visible_when":{"include_all_day":[true]},"description":"IANA timezone that all-day events take their midnight from, e.g. \"Europe/Stockholm\". Empty = UTC."},
					"q":{"type":"string","title":"Search text","description":"Free-text search over event fields. Leave blank to match all."},
					"limit":{"type":"integer","title":"Max events","default":50,"minimum":1,"maximum":2500,"description":"How many events inside the notice window one check looks at. Anything past the cut is never announced, so raise it if a window can hold more events than this."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":300,
						"description":"How often to check once the flow is published. Keep it no longer than the notice asked for above, or an event can start between two checks and be reached late or not at all. Leave blank to only check when you press Run (for testing)."
					},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			// A fire is a discrete poll of the notice window, deduped against
			// what this step has already announced.
			Idempotent: false,
		},
		Execute: executeOnEventStart,
	})
}

func executeOnEventStart(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}
	loc := time.UTC
	if tz := strings.TrimSpace(params.StringDefault(job.Params, "tz", "")); tz != "" {
		l, lerr := time.LoadLocation(tz)
		if lerr != nil {
			return params.Err(job, "bad_param", fmt.Sprintf("tz: unknown timezone %q", tz)), nil
		}
		loc = l
	}
	calID := calendarID(job)
	lead := time.Duration(params.ClampInt(params.IntDefault(job.Params, "lead_minutes", 10), 0, startMaxLeadMinutes)) * time.Minute
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 50), 1, startMaxResults)
	includeAllDay := params.BoolDefault(job.Params, "include_all_day", false)
	now := time.Now().UTC()

	name := fmt.Sprintf("cursor.gcal.start.%s.%s", job.GraphID, job.NodeID)
	prior, announced, rerr := readAnnounced(ctx, job.Tenant, name)
	if rerr != nil {
		// Without the record of what has been announced this check cannot tell
		// a reminder it has already sent from one it hasn't, and every event in
		// the window would be announced again on every check.
		return cursor.FailRead(job, rerr), nil
	}

	rows, err := fetchStarting(ctx, job, token, calID, now, lead, limit)
	if err != nil {
		return params.Err(job, "gcal_error", err.Error()), nil
	}

	// A reminder is worth sending in a window that opens at the start minus the
	// notice asked for and closes shortly after the start itself. The late edge
	// is what absorbs a check that lands behind schedule; without it a
	// scheduler running a few seconds slow would drop a start that fell in the
	// gap, and with it unbounded a resumed daemon would announce the past.
	grace := min(time.Duration(params.IntDefault(job.Params, "interval_seconds", 300))*time.Second, startMaxGrace)
	grace = max(grace, time.Minute)
	earliest, latest := now.Add(-grace), now.Add(lead)

	out := make([]map[string]any, 0, len(rows))
	fired := make([]string, 0, len(rows))
	for _, e := range rows {
		if e.Status == "cancelled" {
			continue
		}
		start, ok := startInstant(e, loc, includeAllDay)
		if !ok || start.After(latest) || start.Before(earliest) {
			continue
		}
		// Keyed by the start as well as the event, so an event moved to a new
		// time is announced again for it — and a series instance, which shares
		// its id shape with its siblings, is announced per occurrence.
		key := e.ID + "@" + e.Start.when()
		if announced[key] {
			continue
		}
		row := e.normalize()
		row["minutes_until_start"] = int(start.Sub(now).Round(time.Minute) / time.Minute)
		row["recurring_event_id"] = e.RecurringEventID
		out = append(out, row)
		fired = append(fired, key)
	}

	if len(out) == 0 {
		pollstate.Report(ctx, job, false) // empty check — let the scheduler back off
		return nothingToReport(job), nil
	}

	// Record before firing. Best-effort: the reminders are on their way, so a
	// failed write means at worst the next check repeats them — a duplicate
	// reminder rather than a missed one, which is the safe direction.
	_ = writeAnnounced(ctx, job.Tenant, name, prior, fired)
	pollstate.Report(ctx, job, true)

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

// startInstant is when the event actually begins. A timed event carries its own
// offset; an all-day one carries a date and nothing else, so midnight has to be
// taken somewhere — in the timezone the step was given, which is the only place
// "the night before" means anything.
func startInstant(e rawEvent, loc *time.Location, includeAllDay bool) (time.Time, bool) {
	if e.Start.DateTime != "" {
		t, err := time.Parse(time.RFC3339, e.Start.DateTime)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	if e.Start.Date == "" || !includeAllDay {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", e.Start.Date, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// fetchStarting reads the events whose start falls inside the notice window.
//
// timeMin/timeMax bound it server-side, but only loosely: Google returns
// anything OVERLAPPING the window, so a long meeting that began hours ago comes
// back too. The window check in the caller is what decides, and it reads the
// start rather than the overlap.
func fetchStarting(ctx context.Context, job core.Job, token, calID string, now time.Time, lead time.Duration, limit int) ([]rawEvent, error) {
	q := url.Values{}
	q.Set("timeMin", now.Format(time.RFC3339))
	// timeMax excludes its own instant, so an event starting exactly at the
	// far edge of the notice window needs the extra second to be included.
	q.Set("timeMax", now.Add(lead).Add(time.Second).Format(time.RFC3339))
	q.Set("singleEvents", "true") // a recurring series is announced per occurrence
	q.Set("orderBy", "startTime") // soonest first, so a truncated check keeps the imminent ones
	q.Set("maxResults", strconv.Itoa(limit))
	if v := strings.TrimSpace(params.StringDefault(job.Params, "q", "")); v != "" {
		q.Set("q", v)
	}

	out := make([]rawEvent, 0, limit)
	pageToken := ""
	for page := 0; page < startPageCap && len(out) < limit; page++ {
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		items, next, err := eventsPage(ctx, job, token, calID, q)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		pageToken = next
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type announcedState struct {
	Keys []string `json:"keys"`
}

// readAnnounced returns the recorded reminder keys (oldest first) and the same
// as a set for lookups. Nothing stored yet is a normal state here rather than a
// baseline to learn: the notice window bounds what a first check can reach, and
// suppressing it would mean publishing a flow that silently skips the very next
// event. A stored value that no longer parses is treated the same way — the
// alternative is a step that never fires again.
//
// A failed READ is the case that must not collapse into either: the record is
// probably still there next time, so the error goes back to the caller, which
// stops without overwriting it.
func readAnnounced(ctx context.Context, tenant, name string) ([]string, map[string]bool, error) {
	raw, err := cursor.Read(ctx, tenant, name)
	if err != nil {
		return nil, nil, err
	}
	var s announcedState
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	set := make(map[string]bool, len(s.Keys))
	for _, k := range s.Keys {
		set[k] = true
	}
	return s.Keys, set, nil
}

// writeAnnounced appends the reminders just sent after the ones already
// recorded, capped with the oldest dropped first. Only reminders that actually
// fired are recorded: an event still ahead of its notice window must stay
// unannounced, or the check that should announce it would find it already
// spoken for.
func writeAnnounced(ctx context.Context, tenant, name string, prior, fired []string) error {
	justFired := make(map[string]bool, len(fired))
	for _, k := range fired {
		justFired[k] = true
	}
	kept := make([]string, 0, len(prior)+len(fired))
	for _, k := range prior {
		if !justFired[k] {
			kept = append(kept, k)
		}
	}
	kept = append(kept, fired...)
	if len(kept) > startSeenCap {
		kept = kept[len(kept)-startSeenCap:]
	}
	b, err := json.Marshal(announcedState{Keys: kept})
	if err != nil {
		return err
	}
	return cursor.Write(ctx, tenant, name, string(b))
}
