// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops/datetime" // registers the date drop the wiring test drives
	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/caldavutil"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// The assertion every connector owes. It bites here because the URL is tenant-
// supplied and every request carries a basic-auth header — an unguarded client
// would hand the calendar credentials to whatever the address resolved to.
func TestCalDAV_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		return caldavutil.Verify(context.Background(), caldavutil.Config{
			URL: "http://127.0.0.1:9/", Username: "u", Password: "p",
		})
	})
}

func run(t *testing.T, exec func(context.Context, core.Job, chan<- core.Progress) (core.Result, error), j core.Job) core.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := exec(ctx, j, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	return res
}

func events(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	ref, ok := res.Output["events"]
	if !ok {
		return nil
	}
	rows, ok := ref.Inline.([]map[string]any)
	if !ok {
		t.Fatalf("events is %T", ref.Inline)
	}
	return rows
}

func summaries(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		s, _ := r["summary"].(string)
		out = append(out, s)
	}
	return out
}

func TestCalDAVList_NotConnectedWithoutAServer(t *testing.T) {
	res, err := executeCalDAVList(context.Background(), core.Job{ID: "j"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error == nil || res.Error.Code != "not_connected" {
		t.Fatalf("want not_connected, got %+v", res.Error)
	}
}

// The record shape is the design constraint: it must match what
// gcal_list_events emits, so a reminder flow becomes provider-neutral by
// swapping the step.
func TestCalDAVList_EmitsGoogleShapedRecords(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)
	start := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	b.put(t, "work", "evt-1", "Intro call", start, start.Add(time.Hour))

	rows := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	if len(rows) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(rows), rows)
	}
	rec := rows[0]
	for _, field := range []string{"id", "summary", "description", "location", "start", "end", "status", "attendees"} {
		if _, ok := rec[field]; !ok {
			t.Errorf("record is missing %q — the Google-shaped contract: %+v", field, rec)
		}
	}
	if rec["summary"] != "Intro call" {
		t.Errorf("summary = %q", rec["summary"])
	}
	if got, _ := rec["start"].(string); !strings.HasPrefix(got, "2026-06-16T09:00:00") {
		t.Errorf("start = %q, want RFC3339", got)
	}
	if rec["id"] != "evt-1" {
		t.Errorf("id = %q, want the event's UID", rec["id"])
	}
}

func TestCalDAVList_RelativeWindowPicksTomorrow(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	now := time.Now().In(loc)
	tomorrow := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, loc).AddDate(0, 0, 1)
	nextWeek := tomorrow.AddDate(0, 0, 6)

	b.put(t, "work", "tomorrow-evt", "Tomorrow's booking", tomorrow, tomorrow.Add(time.Hour))
	b.put(t, "work", "later-evt", "Next week", nextWeek, nextWeek.Add(time.Hour))

	rows := events(t, run(t, executeCalDAVList, job(t, url, map[string]any{
		"time_min": "tomorrow", "time_max": "tomorrow+1d", "tz": "Europe/Stockholm",
	})))
	if got := summaries(rows); len(got) != 1 || got[0] != "Tomorrow's booking" {
		t.Fatalf("window returned %v, want only tomorrow's booking", got)
	}
}

// CalDAV makes no ordering promise — events come back in whatever order the
// server's collection walk produced — so a flow sending reminders in order
// has to be given one.
func TestCalDAVList_SortsEarliestFirst(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)
	base := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC)
	b.put(t, "work", "c", "Third", base.Add(4*time.Hour), base.Add(5*time.Hour))
	b.put(t, "work", "a", "First", base, base.Add(time.Hour))
	b.put(t, "work", "b", "Second", base.Add(2*time.Hour), base.Add(3*time.Hour))

	rows := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	got := summaries(rows)
	if len(got) != 3 || got[0] != "First" || got[1] != "Second" || got[2] != "Third" {
		t.Fatalf("order was %v, want earliest first", got)
	}
}

func TestCalDAVList_RejectsABackwardsWindow(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	res, err := executeCalDAVList(context.Background(), job(t, url, map[string]any{
		"time_min": "tomorrow", "time_max": "yesterday",
	}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a window that ends before it starts should fail the step, not return nothing")
	}
}

func TestCalDAV_DiscoversCalendarsFromTheRootURL(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	if err := caldavutil.Verify(context.Background(), caldavutil.Config{
		URL: url, Username: testUser, Password: testPass,
	}); err != nil {
		t.Fatalf("discovery from the root URL failed: %v", err)
	}
}

func TestCalDAV_AmbiguousCalendarNamesTheOptions(t *testing.T) {
	b := newBackend("Work", "Personal")
	url := startCalDAV(t, b)

	err := caldavutil.Verify(context.Background(), caldavutil.Config{
		URL: url, Username: testUser, Password: testPass,
	})
	if err == nil {
		t.Fatal("two calendars and no choice should not silently pick one")
	}
	for _, want := range []string{"Work", "Personal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list %q so it can be copied: %v", want, err)
		}
	}
}

func TestCalDAV_ChoosesTheNamedCalendar(t *testing.T) {
	b := newBackend("Work", "Personal")
	url := startCalDAV(t, b)
	start := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	b.put(t, "work", "w", "Work thing", start, start.Add(time.Hour))
	b.put(t, "personal", "p", "Personal thing", start, start.Add(time.Hour))

	rows := events(t, run(t, executeCalDAVList, job(t, url, map[string]any{"calendar": "Personal"})))
	if got := summaries(rows); len(got) != 1 || got[0] != "Personal thing" {
		t.Fatalf("read %v, want only the named calendar's events", got)
	}
	rows = events(t, run(t, executeCalDAVList, job(t, url, map[string]any{"calendar": "work"})))
	if got := summaries(rows); len(got) != 1 || got[0] != "Work thing" {
		t.Fatalf("case-insensitive match read %v", got)
	}
}

func TestCalDAV_MistypedCalendarIsExplained(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	res, err := executeCalDAVList(context.Background(), job(t, url, map[string]any{"calendar": "Wrok"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a mistyped calendar should fail the step, not read the wrong one")
	}
	if !strings.Contains(res.Error.Message, "Work") {
		t.Errorf("error should list what's actually there: %q", res.Error.Message)
	}
}

func TestCalDAV_BadPasswordIsRejected(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	err := caldavutil.Verify(context.Background(), caldavutil.Config{
		URL: url, Username: testUser, Password: "wrong",
	})
	if err == nil {
		t.Fatal("a wrong password must not verify")
	}
}

func TestCalDAVCreate_PutsTheEventOnTheCalendar(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	res := run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary":     "Intro call",
		"start":       "2026-06-16T09:00:00Z",
		"end":         "2026-06-16T10:00:00Z",
		"description": "About the project",
		"location":    "Room 2",
		"attendees":   "ada@example.test, bob@example.test",
	}))
	id, _ := res.Output["event_id"].Inline.(string)
	if id == "" {
		t.Fatal("no event id emitted")
	}
	if b.count() != 1 {
		t.Fatalf("backend holds %d objects, want 1", b.count())
	}

	// Read it back through the listing, which is what a flow would do — and
	// proves the event we wrote is one a CalDAV client can parse.
	rows := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	if len(rows) != 1 {
		t.Fatalf("listing found %d events after the write", len(rows))
	}
	rec := rows[0]
	if rec["summary"] != "Intro call" || rec["location"] != "Room 2" {
		t.Errorf("event read back as %+v", rec)
	}
	if rec["description"] != "About the project" {
		t.Errorf("description = %q", rec["description"])
	}
	guests, _ := rec["attendees"].([]string)
	if len(guests) != 2 || guests[0] != "ada@example.test" || guests[1] != "bob@example.test" {
		t.Errorf("attendees = %v, want bare addresses", guests)
	}
}

func TestCalDAVCreate_DefaultsToAnHour(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary": "Quick chat",
		"start":   "2026-06-16T09:00:00Z",
	}))
	rows := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	if len(rows) != 1 {
		t.Fatalf("want 1 event, got %d", len(rows))
	}
	if got, _ := rows[0]["end"].(string); !strings.HasPrefix(got, "2026-06-16T10:00:00") {
		t.Errorf("end = %q, want an hour after the start", got)
	}
}

func TestCalDAVCreate_RequiresSummaryAndStart(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	for _, tc := range []struct {
		name   string
		params map[string]any
	}{
		{name: "no summary", params: map[string]any{"start": "2026-06-16T09:00:00Z"}},
		{name: "no start", params: map[string]any{"summary": "Something"}},
		{name: "backwards", params: map[string]any{"summary": "S", "start": "2026-06-16T10:00:00Z", "end": "2026-06-16T09:00:00Z"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := executeCalDAVCreate(context.Background(), job(t, url, tc.params), nil)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.Status == core.StatusOK {
				t.Fatalf("%s should fail the step", tc.name)
			}
		})
	}
	if b.count() != 0 {
		t.Errorf("a rejected event still reached the server (%d objects)", b.count())
	}
}

func TestCalDAVCreate_AcceptsRelativeTimes(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary": "Tomorrow morning",
		"start":   "tomorrow+9h",
		"end":     "tomorrow+10h",
		"tz":      "UTC",
	}))
	if b.count() != 1 {
		t.Fatalf("relative times didn't produce an event (%d objects)", b.count())
	}
	rows := events(t, run(t, executeCalDAVList, job(t, url, map[string]any{
		"time_min": "tomorrow", "time_max": "tomorrow+1d", "tz": "UTC",
	})))
	if got := summaries(rows); len(got) != 1 || got[0] != "Tomorrow morning" {
		t.Fatalf("the created event isn't in tomorrow's window: %v", got)
	}
}

func TestCalDAVCreate_InputsOverrideParams(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	j := job(t, url, map[string]any{"summary": "typed", "start": "2026-06-16T09:00:00Z"})
	j.Input = map[string]core.Ref{
		"summary": {Inline: "wired"},
		"start":   {Inline: "2026-07-01T14:00:00Z"},
	}
	run(t, executeCalDAVCreate, j)

	rows := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	if len(rows) != 1 {
		t.Fatalf("want 1 event, got %d", len(rows))
	}
	if rows[0]["summary"] != "wired" {
		t.Errorf("summary = %q, want the wired value to win", rows[0]["summary"])
	}
	if got, _ := rows[0]["start"].(string); !strings.HasPrefix(got, "2026-07-01T14:00:00") {
		t.Errorf("start = %q, want the wired value to win", got)
	}
}

// A cancelled context must return promptly rather than running the request.
func TestCalDAVList_RespectsCancelledContext(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan core.Result, 1)
	go func() {
		res, _ := executeCalDAVList(ctx, job(t, url, nil), nil)
		done <- res
	}()
	select {
	case res := <-done:
		if res.Status == core.StatusOK {
			t.Error("a cancelled listing reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listing ignored a cancelled context")
	}
}

// The Date & time step is the most likely thing wired into these ports — "book
// it for tomorrow at 09:00" is a Date step, not a typed timestamp — so the
// contract between the two is pinned here by running the REAL step and feeding
// its output in. Asserting against hand-written strings would only test what I
// assume Date emits.
//
// Both sides share one list of input layouts (drops/internal/reltime), so the
// machine formats interoperate by construction. The display formats
// deliberately do not, and that half is asserted too: a weekday name is not a
// time, and guessing at a locale's date order would be worse than saying so.
func TestCalDAV_AcceptsWhatTheDateStepEmits(t *testing.T) {
	dateStep, ok := engine.Default.Get("date")
	if !ok {
		t.Fatal("the date drop isn't registered")
	}

	render := func(t *testing.T, format, custom string) string {
		t.Helper()
		p := map[string]any{
			"in":     "2026-06-16T09:00:00Z",
			"format": format,
			"tz":     "UTC",
		}
		if custom != "" {
			p["custom_format"] = custom
		}
		res, err := dateStep.Execute(context.Background(), core.Job{
			ID: "date-1", Params: p,
		}, nil)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("date step failed for format %q: %v %+v", format, err, res.Error)
		}
		out, _ := res.Output["out"].Inline.(string)
		if out == "" {
			t.Fatalf("date step emitted nothing for format %q", format)
		}
		return out
	}

	b := newBackend("Work")
	url := startCalDAV(t, b)

	t.Run("machine formats flow in", func(t *testing.T) {
		for _, format := range []string{"iso", "date", "datetime", "unix"} {
			t.Run(format, func(t *testing.T) {
				stamp := render(t, format, "")
				j := job(t, url, map[string]any{"summary": "From a Date step", "tz": "UTC"})
				j.Input = map[string]core.Ref{"start": {Inline: stamp}}

				res, err := executeCalDAVCreate(context.Background(), j, nil)
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				if res.Status != core.StatusOK {
					t.Fatalf("the Date step's %q output (%q) was rejected: %+v", format, stamp, res.Error)
				}
			})
		}
	})

	// The display formats: rendered for a person, not a machine. These must
	// fail, and the failure has to name the value rather than book something
	// arbitrary.
	t.Run("display formats are refused by name", func(t *testing.T) {
		for _, tc := range []struct{ format, custom string }{
			{format: "weekday"},
			{format: "rfc1123"},
			{format: "custom", custom: "DD/MM/YYYY"},
		} {
			t.Run(tc.format+tc.custom, func(t *testing.T) {
				stamp := render(t, tc.format, tc.custom)
				j := job(t, url, map[string]any{"summary": "Should not book", "tz": "UTC"})
				j.Input = map[string]core.Ref{"start": {Inline: stamp}}

				res, err := executeCalDAVCreate(context.Background(), j, nil)
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				if res.Status == core.StatusOK {
					t.Fatalf("the Date step's %q output (%q) was accepted — a display string is not a time", tc.format, stamp)
				}
				if !strings.Contains(res.Error.Message, stamp) {
					t.Errorf("error should quote the value that couldn't be read (%q): %q", stamp, res.Error.Message)
				}
				// One "couldn't read", not two: reltime's message is already
				// the good one and must not be wrapped in a copy of itself.
				if n := strings.Count(res.Error.Message, "couldn't read"); n != 1 {
					t.Errorf("message says \"couldn't read\" %d times: %q", n, res.Error.Message)
				}
			})
		}
	})

	t.Run("the listing window takes them too", func(t *testing.T) {
		stamp := render(t, "iso", "")
		j := job(t, url, map[string]any{"time_max": "+30d", "tz": "UTC"})
		j.Input = map[string]core.Ref{"time_min": {Inline: stamp}}

		res, err := executeCalDAVList(context.Background(), j, nil)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if res.Status != core.StatusOK {
			t.Fatalf("the Date step's output was rejected as a window bound: %+v", res.Error)
		}
	})
}

func TestCalDAVCreate_AllDayEvent(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary": "Midsummer", "start": "2026-06-19", "all_day": true,
	}))

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.objects) != 1 {
		t.Fatalf("want 1 object, got %d", len(b.objects))
	}
	for _, obj := range b.objects {
		ev := obj.Data.Events()[0]
		prop := ev.Props.Get(ical.PropDateTimeStart)
		if prop == nil {
			t.Fatal("no DTSTART")
		}
		if got := prop.Params.Get(ical.ParamValue); got != "DATE" {
			t.Errorf("DTSTART VALUE = %q, want DATE — otherwise it's a midnight timed event", got)
		}
		if strings.Contains(prop.Value, "T") {
			t.Errorf("DTSTART = %q, want a bare date", prop.Value)
		}
		endProp := ev.Props.Get(ical.PropDateTimeEnd)
		if endProp == nil || endProp.Value != "20260620" {
			t.Errorf("DTEND = %#v, want 20260620 (the day after)", endProp)
		}
	}
}

// A timed event must NOT become date-valued — the regression guard for the
// flag leaking into the default path.
func TestCalDAVCreate_TimedEventStaysTimed(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)
	run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary": "Call", "start": "2026-06-19T09:00:00Z",
	}))

	b.mu.Lock()
	defer b.mu.Unlock()
	for _, obj := range b.objects {
		prop := obj.Data.Events()[0].Props.Get(ical.PropDateTimeStart)
		if got := prop.Params.Get(ical.ParamValue); got == "DATE" {
			t.Error("a timed event came out date-valued")
		}
	}
}

func TestCalDAVUpdate_PartialChangeKeepsEverythingElse(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)
	run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary": "Intro call", "start": "2026-06-16T09:00:00Z", "end": "2026-06-16T10:00:00Z",
		"description": "About the project", "location": "Room 2",
		"attendees": "ada@example.test, bob@example.test",
	}))
	before := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	id, _ := before[0]["id"].(string)

	run(t, executeCalDAVUpdate, job(t, url, map[string]any{"id": id, "location": "Room 4"}))

	after := events(t, run(t, executeCalDAVList, job(t, url, nil)))
	if len(after) != 1 {
		t.Fatalf("want 1 event after the update, got %d", len(after))
	}
	rec := after[0]
	if rec["location"] != "Room 4" {
		t.Errorf("location = %v, want the change applied", rec["location"])
	}
	// Everything not mentioned must survive. A step that replaced the event
	// with only the supplied fields would silently drop the guest list and
	// the description — data loss nobody notices until someone doesn't turn up.
	if rec["summary"] != "Intro call" {
		t.Errorf("summary was lost: %v", rec["summary"])
	}
	if rec["description"] != "About the project" {
		t.Errorf("description was lost: %v", rec["description"])
	}
	guests, _ := rec["attendees"].([]string)
	if len(guests) != 2 {
		t.Errorf("attendees were lost: %v", guests)
	}
	if got, _ := rec["start"].(string); !strings.HasPrefix(got, "2026-06-16T09:00:00") {
		t.Errorf("start moved when it shouldn't have: %v", got)
	}
}

// "Push it back an hour" is the common reschedule, and it must keep the
// event's LENGTH — re-deriving the end from a default hour would silently
// shorten a two-hour meeting.
func TestCalDAVUpdate_MovingStartKeepsDuration(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)
	run(t, executeCalDAVCreate, job(t, url, map[string]any{
		"summary": "Long meeting",
		"start":   "2026-06-16T09:00:00Z", "end": "2026-06-16T11:00:00Z",
	}))
	id, _ := events(t, run(t, executeCalDAVList, job(t, url, nil)))[0]["id"].(string)

	run(t, executeCalDAVUpdate, job(t, url, map[string]any{"id": id, "start": "2026-06-16T10:00:00Z"}))

	rec := events(t, run(t, executeCalDAVList, job(t, url, nil)))[0]
	if got, _ := rec["start"].(string); !strings.HasPrefix(got, "2026-06-16T10:00:00") {
		t.Errorf("start = %v", got)
	}
	if got, _ := rec["end"].(string); !strings.HasPrefix(got, "2026-06-16T12:00:00") {
		t.Errorf("end = %v, want the original two-hour length preserved", got)
	}
}

func TestCalDAVUpdate_MissingEventIsNamed(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	res, err := executeCalDAVUpdate(context.Background(), job(t, url, map[string]any{"id": "no-such-event", "summary": "x"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("updating a missing event should fail the step")
	}
	if res.Error.Code != "not_found" {
		t.Errorf("code = %q: %v", res.Error.Code, res.Error.Message)
	}
}

func TestCalDAVDelete_RemovesTheEvent(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)
	run(t, executeCalDAVCreate, job(t, url, map[string]any{"summary": "Cancelled thing", "start": "2026-06-16T09:00:00Z"}))
	id, _ := events(t, run(t, executeCalDAVList, job(t, url, nil)))[0]["id"].(string)

	res := run(t, executeCalDAVDelete, job(t, url, map[string]any{"id": id}))
	meta, _ := res.Output["meta"].Inline.(map[string]any)
	if meta["removed"] != true {
		t.Errorf("meta.removed = %v, want true", meta["removed"])
	}
	if left := events(t, run(t, executeCalDAVList, job(t, url, nil))); len(left) != 0 {
		t.Fatalf("the event is still there: %+v", left)
	}
}

// Deleting something already gone is not an error: the calendar is in the
// state the flow asked for, and failing would break the second run of a
// cancellation flow.
func TestCalDAVDelete_AlreadyGoneIsNotAnError(t *testing.T) {
	b := newBackend("Work")
	url := startCalDAV(t, b)

	res := run(t, executeCalDAVDelete, job(t, url, map[string]any{"id": "never-existed"}))
	meta, _ := res.Output["meta"].Inline.(map[string]any)
	if meta["removed"] != false {
		t.Errorf("meta.removed = %v, want false so a flow can tell the two apart", meta["removed"])
	}
}
