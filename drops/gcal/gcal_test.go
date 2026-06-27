// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func withCalEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase(calendarAPIBase)
		SetTokenLookup(nil)
	})
}

func TestListEvents_NormalizesItems(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id":       "ev1",
					"status":   "confirmed",
					"summary":  "Standup",
					"htmlLink": "https://calendar.google.com/ev1",
					"location": "Room 2",
					"start":    map[string]any{"dateTime": "2026-06-16T09:00:00Z"},
					"end":      map[string]any{"dateTime": "2026-06-16T09:30:00Z"},
					"attendees": []map[string]any{
						{"email": "a@x"}, {"email": "b@y"}, {"email": ""},
					},
				},
				{
					"id":      "ev2",
					"summary": "Holiday",
					"start":   map[string]any{"date": "2026-06-17"},
					"end":     map[string]any{"date": "2026-06-18"},
				},
			},
		})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	res, err := executeListEvents(context.Background(), core.Job{
		Params: map[string]any{"calendar_id": "primary", "time_min": "2026-06-16T00:00:00Z", "q": "x"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	if gotPath != "/calendars/primary/events" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ya29-default" {
		t.Errorf("auth = %q", gotAuth)
	}
	// singleEvents=true → orderBy=startTime present; time_min/q forwarded.
	for _, want := range []string{"singleEvents=true", "orderBy=startTime", "timeMin=", "q=x"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}

	events := res.Output["events"].Inline.([]map[string]any)
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0]["summary"] != "Standup" || events[0]["start"] != "2026-06-16T09:00:00Z" {
		t.Errorf("event0 = %+v", events[0])
	}
	if events[0]["all_day"] != false {
		t.Errorf("event0 all_day = %v, want false", events[0]["all_day"])
	}
	// Blank attendee email is dropped.
	if got := events[0]["attendees"].([]string); len(got) != 2 || got[0] != "a@x" {
		t.Errorf("attendees = %v", got)
	}
	// All-day event: date populates start, all_day true.
	if events[1]["start"] != "2026-06-17" || events[1]["all_day"] != true {
		t.Errorf("event1 = %+v", events[1])
	}
	if res.Output["count"].Inline != "2" {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
}

func TestListEvents_SingleEventsFalseOmitsOrderBy(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	if _, err := ListEvents(context.Background(), core.Job{
		Params: map[string]any{"single_events": false},
	}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if strings.Contains(gotQuery, "orderBy") {
		t.Errorf("orderBy must be omitted when single_events=false: %q", gotQuery)
	}
	if !strings.Contains(gotQuery, "singleEvents=false") {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestListEvents_APIErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient scope"}}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	res, err := executeListEvents(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "insufficient scope") {
		t.Errorf("error = %+v", res.Error)
	}
}

func TestCreateEvent_TimedWithAttendees(t *testing.T) {
	var sent map[string]any
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       "new1",
			"htmlLink": "https://calendar.google.com/new1",
			"summary":  sent["summary"],
			"start":    sent["start"],
			"end":      sent["end"],
		})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	res, err := executeCreateEvent(context.Background(), core.Job{
		Params: map[string]any{
			"calendar_id": "work@grp",
			"summary":     "Sync",
			"start":       "2026-06-16T15:00:00Z",
			"end":         "2026-06-16T16:00:00Z",
			"time_zone":   "UTC",
			"attendees":   "a@x, b@y , ",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	if gotMethod != "POST" || gotPath != "/calendars/work@grp/events" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	start := sent["start"].(map[string]any)
	if start["dateTime"] != "2026-06-16T15:00:00Z" || start["timeZone"] != "UTC" {
		t.Errorf("start = %+v", start)
	}
	att := sent["attendees"].([]any)
	if len(att) != 2 || att[0].(map[string]any)["email"] != "a@x" {
		t.Errorf("attendees = %+v (blank should be dropped)", att)
	}
	if res.Output["event_id"].Inline != "new1" {
		t.Errorf("event_id = %v", res.Output["event_id"].Inline)
	}
	if res.Output["html_link"].Inline != "https://calendar.google.com/new1" {
		t.Errorf("html_link = %v", res.Output["html_link"].Inline)
	}
}

func TestCreateEvent_AllDayUsesDate(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	res, err := executeCreateEvent(context.Background(), core.Job{
		Params: map[string]any{
			"summary": "Holiday",
			"start":   "2026-06-17",
			"end":     "2026-06-18",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	start := sent["start"].(map[string]any)
	if start["date"] != "2026-06-17" {
		t.Errorf("all-day start should use date: %+v", start)
	}
	if _, hasDateTime := start["dateTime"]; hasDateTime {
		t.Errorf("all-day start must not carry dateTime: %+v", start)
	}
}

func TestCreateEvent_RequiresSummaryAndTimes(t *testing.T) {
	withCalEnv(t, "http://unused.invalid")
	cases := []map[string]any{
		{"start": "2026-06-16T15:00:00Z", "end": "2026-06-16T16:00:00Z"}, // no summary
		{"summary": "x", "end": "2026-06-16T16:00:00Z"},                  // no start
		{"summary": "x", "start": "2026-06-16T15:00:00Z"},                // no end
	}
	for i, p := range cases {
		res, err := executeCreateEvent(context.Background(), core.Job{Params: p}, nil)
		if err != nil {
			t.Fatalf("case %d: unexpected err %v", i, err)
		}
		if res.Status != core.StatusError {
			t.Errorf("case %d: status = %q, want error", i, res.Status)
		}
	}
}

func TestListCalendars_PrependsPrimaryAndSkipsDuplicate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "me@x", "summary": "Me", "primary": true},
				{"id": "team@x", "summary": "Team"},
				{"id": "proj@x", "summary": "Project", "summaryOverride": "My Project"},
			},
		})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	got, err := ListCalendars(context.Background(), core.Job{Params: map[string]any{}})
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	// Synthetic "primary" first; the primary item (me@x) is skipped; override wins.
	if len(got) != 3 {
		t.Fatalf("options = %+v", got)
	}
	if got[0].ID != "primary" || got[0].Name != "Primary calendar" {
		t.Errorf("first = %+v, want synthetic primary", got[0])
	}
	if got[1].ID != "team@x" || got[1].Name != "Team" {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].ID != "proj@x" || got[2].Name != "My Project" {
		t.Errorf("summaryOverride should win: %+v", got[2])
	}
	for _, o := range got {
		if o.ID == "me@x" {
			t.Errorf("primary calendar must not appear twice: %+v", got)
		}
	}
}

func TestResolveCalendarID_InputWinsThenParamThenPrimary(t *testing.T) {
	// Wired input port wins.
	job := core.Job{
		Params: map[string]any{"calendar_id": "from-param"},
		Input:  map[string]core.Ref{"calendar_id": {Inline: "from-input"}},
	}
	if got := resolveCalendarID(job); got != "from-input" {
		t.Errorf("got %q, want from-input", got)
	}
	// Param used when no input.
	if got := resolveCalendarID(core.Job{Params: map[string]any{"calendar_id": "from-param"}}); got != "from-param" {
		t.Errorf("got %q, want from-param", got)
	}
	// Defaults to primary.
	if got := resolveCalendarID(core.Job{Params: map[string]any{}}); got != "primary" {
		t.Errorf("got %q, want primary", got)
	}
}
