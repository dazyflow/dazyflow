// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

func TestCovCalBaseURLOverride(t *testing.T) {
	job := core.Job{Params: map[string]any{"base_url": "http://override.example"}}
	if got := calBaseURL(job); got != "http://override.example" {
		t.Fatalf("base_url param should win, got %q", got)
	}
	// Empty base_url falls back to the configured root.
	if got := calBaseURL(core.Job{Params: map[string]any{}}); got != calBase.Get() {
		t.Fatalf("blank base_url should fall back, got %q", got)
	}
}

func TestCovResolveSummary(t *testing.T) {
	// String input wins.
	if got := resolveSummary(core.Job{Input: map[string]core.Ref{"summary": {Inline: "  Hello  "}}}); got != "Hello" {
		t.Fatalf("string input = %q", got)
	}
	// []byte input wins.
	if got := resolveSummary(core.Job{Input: map[string]core.Ref{"summary": {Inline: []byte("Bytes")}}}); got != "Bytes" {
		t.Fatalf("byte input = %q", got)
	}
	// Blank string input falls through to param.
	job := core.Job{
		Input:  map[string]core.Ref{"summary": {Inline: "   "}},
		Params: map[string]any{"summary": "Param"},
	}
	if got := resolveSummary(job); got != "Param" {
		t.Fatalf("blank input should fall back to param, got %q", got)
	}
	// Non-string/byte input ignored → param.
	job2 := core.Job{
		Input:  map[string]core.Ref{"summary": {Inline: 42}},
		Params: map[string]any{"summary": "Fallback"},
	}
	if got := resolveSummary(job2); got != "Fallback" {
		t.Fatalf("non-text input should fall back, got %q", got)
	}
}

func TestCovResolveCalendarID(t *testing.T) {
	if got := resolveCalendarID(core.Job{Input: map[string]core.Ref{"calendar_id": {Inline: " cal@x "}}}); got != "cal@x" {
		t.Fatalf("string input = %q", got)
	}
	if got := resolveCalendarID(core.Job{Input: map[string]core.Ref{"calendar_id": {Inline: []byte("cal2")}}}); got != "cal2" {
		t.Fatalf("byte input = %q", got)
	}
	// Blank input → param default "primary".
	if got := resolveCalendarID(core.Job{Input: map[string]core.Ref{"calendar_id": {Inline: ""}}}); got != "primary" {
		t.Fatalf("blank input should default to primary, got %q", got)
	}
	// Non-text input ignored → configured calendar_id.
	job := core.Job{Input: map[string]core.Ref{"calendar_id": {Inline: 9}}, Params: map[string]any{"calendar_id": "c3"}}
	if got := resolveCalendarID(job); got != "c3" {
		t.Fatalf("non-text input should fall back, got %q", got)
	}
}

func TestCovCreateEventBadParams(t *testing.T) {
	// Missing summary.
	r, _ := executeCreateEvent(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("missing summary → bad_param, got %+v", r.Error)
	}
	// Missing start/end.
	r, _ = executeCreateEvent(context.Background(), core.Job{Params: map[string]any{"summary": "S"}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("missing start/end → bad_param, got %+v", r.Error)
	}
}

func TestCovCreateEventTokenError(t *testing.T) {
	SetTokenLookup(nil)
	t.Cleanup(func() { SetTokenLookup(nil) })
	job := core.Job{Params: map[string]any{"summary": "S", "start": "2026-06-26T10:00:00Z", "end": "2026-06-26T11:00:00Z"}}
	r, _ := executeCreateEvent(context.Background(), job, nil)
	if r.Error == nil || r.Error.Code != "gcal_error" {
		t.Fatalf("token error → gcal_error, got %+v", r.Error)
	}
}

func TestCovCreateEventNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	job := core.Job{Params: map[string]any{
		"summary": "S", "start": "2026-06-26", "end": "2026-06-27",
		"description": "d", "location": "loc", "attendees": "a@x, b@y",
	}}
	r, _ := executeCreateEvent(context.Background(), job, nil)
	if r.Error == nil || r.Error.Code != "gcal_error" {
		t.Fatalf("403 → gcal_error, got %+v", r.Error)
	}
}

func TestCovCreateEventBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	job := core.Job{Params: map[string]any{
		"summary": "S", "start": "2026-06-26T10:00:00Z", "end": "2026-06-26T11:00:00Z", "time_zone": "Europe/Stockholm",
	}}
	r, _ := executeCreateEvent(context.Background(), job, nil)
	if r.Error == nil || r.Error.Code != "gcal_error" {
		t.Fatalf("bad json → gcal_error, got %+v", r.Error)
	}
}

func TestCovCreateEventSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ev99", "htmlLink": "https://cal/ev99", "summary": "S"})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	job := core.Job{Params: map[string]any{"summary": "S", "start": "2026-06-26", "end": "2026-06-27"}}
	r, _ := executeCreateEvent(context.Background(), job, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if r.Output["event_id"].Inline != "ev99" {
		t.Fatalf("event_id = %v", r.Output["event_id"].Inline)
	}
}

func TestCovListEventsTokenError(t *testing.T) {
	SetTokenLookup(nil)
	t.Cleanup(func() { SetTokenLookup(nil) })
	if _, err := ListEvents(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("token error should propagate")
	}
}

func TestCovListEventsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	if _, err := ListEvents(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("500 should error")
	}
}

func TestCovListEventsBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	if _, err := ListEvents(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("bad json should error")
	}
}

func TestCovListEventsNonSingle(t *testing.T) {
	// single_events=false drops orderBy=startTime; also exercises the
	// time_max branch.
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	job := core.Job{Params: map[string]any{"single_events": false, "time_max": "2026-07-01T00:00:00Z"}}
	if _, err := ListEvents(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if want := "singleEvents=false"; !strings.Contains(gotQuery, want) {
		t.Fatalf("query %q missing %q", gotQuery, want)
	}
	if strings.Contains(gotQuery, "orderBy=startTime") {
		t.Fatalf("non-single query should omit orderBy, got %q", gotQuery)
	}
}

func TestCovListCalendars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "primary", "summary": "Me", "primary": true},
				{"id": "team@x", "summaryOverride": "Team (mine)", "summary": "Team"},
				{"id": "plain@x", "summary": "Plain"},
				{"id": "noname@x"},
			},
		})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	out, err := ListCalendars(context.Background(), core.Job{Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic "primary" first; the real primary skipped; override and
	// id-fallback names applied.
	if len(out) != 4 || out[0].ID != "primary" || out[0].Name != "Primary calendar" {
		t.Fatalf("calendars = %+v", out)
	}
	byID := map[string]string{}
	for _, c := range out {
		byID[c.ID] = c.Name
	}
	if byID["team@x"] != "Team (mine)" {
		t.Fatalf("summaryOverride should win: %q", byID["team@x"])
	}
	if byID["plain@x"] != "Plain" {
		t.Fatalf("summary fallback: %q", byID["plain@x"])
	}
	if byID["noname@x"] != "noname@x" {
		t.Fatalf("id fallback: %q", byID["noname@x"])
	}
}

func TestCovListCalendarsErrors(t *testing.T) {
	SetTokenLookup(nil)
	if _, err := ListCalendars(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("token error should propagate")
	}
	SetTokenLookup(nil)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	if _, err := ListCalendars(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("401 should error")
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv2.Close()
	withCalEnv(t, srv2.URL)
	if _, err := ListCalendars(context.Background(), core.Job{Params: map[string]any{}}); err == nil {
		t.Fatal("bad json should error")
	}
}

// A nightly reminder flow has to be able to say "tomorrow" and mean it on
// every run — an RFC3339-only field can't express a window that moves with
// the schedule. Day boundaries are taken in the step's timezone.
func TestListEvents_RelativeWindow(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	if _, err := ListEvents(context.Background(), core.Job{Params: map[string]any{
		"account": "default", "calendar_id": "primary",
		"time_min": "tomorrow", "time_max": "tomorrow+1d", "tz": "Europe/Stockholm",
	}}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}

	q, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	min, max := q.Get("timeMin"), q.Get("timeMax")
	tMin, err := time.Parse(time.RFC3339, min)
	if err != nil {
		t.Fatalf("timeMin %q is not RFC3339: %v", min, err)
	}
	tMax, err := time.Parse(time.RFC3339, max)
	if err != nil {
		t.Fatalf("timeMax %q is not RFC3339: %v", max, err)
	}
	if d := tMax.Sub(tMin); d != 24*time.Hour {
		t.Errorf("window is %v wide, want exactly 24h (%s → %s)", d, min, max)
	}
	sthlm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	local := tMin.In(sthlm)
	if local.Hour() != 0 || local.Minute() != 0 {
		t.Errorf("window does not start at local midnight: %s", local)
	}
	if want := time.Now().In(sthlm).AddDate(0, 0, 1).Day(); local.Day() != want {
		t.Errorf("window starts on day %d, want tomorrow (%d)", local.Day(), want)
	}
}

// Either end can be computed upstream, so both take a wire that overrides
// the typed setting.
func TestListEvents_WindowInputOverridesParam(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	if _, err := ListEvents(context.Background(), core.Job{
		Params: map[string]any{"account": "default", "time_min": "2020-01-01T00:00:00Z"},
		Input:  map[string]core.Ref{"time_min": {Inline: "2026-06-16T00:00:00Z"}},
	}); err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	q, _ := url.ParseQuery(gotQuery)
	if got := q.Get("timeMin"); got != "2026-06-16T00:00:00Z" {
		t.Errorf("timeMin = %q, want the wired value", got)
	}
}

func TestListEvents_BadWindowValue(t *testing.T) {
	withCalEnv(t, "http://127.0.0.1:1")
	_, err := ListEvents(context.Background(), core.Job{Params: map[string]any{
		"account": "default", "time_min": "next thursday",
	}})
	if err == nil || !strings.Contains(err.Error(), "time_min") {
		t.Errorf("err = %v, want one naming time_min", err)
	}
}

// Creating an event from a flow means the when/who/where came from whatever
// started it — a form, a row, a message — so those fields have to take a wire,
// not just a typed value.
func TestCreateEvent_WiredFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ev1", "htmlLink": "https://cal/ev1"})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	res, err := executeCreateEvent(context.Background(), core.Job{
		Params: map[string]any{"account": "default", "summary": "typed", "start": "2020-01-01T00:00:00Z", "end": "2020-01-01T01:00:00Z"},
		Input: map[string]core.Ref{
			"summary":     {Inline: "Intro with Ida"},
			"start":       {Inline: "2026-06-16T09:00:00Z"},
			"end":         {Inline: "2026-06-16T09:30:00Z"},
			"description": {Inline: "First-day welcome"},
			"location":    {Inline: "Room 2"},
			"attendees":   {Inline: "ida@example.com, chef@example.com"},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if got["summary"] != "Intro with Ida" || got["description"] != "First-day welcome" || got["location"] != "Room 2" {
		t.Errorf("event = %v", got)
	}
	start, _ := got["start"].(map[string]any)
	if start["dateTime"] != "2026-06-16T09:00:00Z" {
		t.Errorf("start = %v, want the wired value", got["start"])
	}
	att, _ := got["attendees"].([]any)
	if len(att) != 2 {
		t.Errorf("attendees = %v, want both", got["attendees"])
	}
}

// A relative start/end becomes a concrete timestamp; an absolute one is left
// exactly as written so a plain date still means an all-day event.
func TestCreateEvent_RelativeAndAllDay(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ev1"})
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)

	if _, err := executeCreateEvent(context.Background(), core.Job{Params: map[string]any{
		"account": "default", "summary": "Intro", "start": "tomorrow+9h", "end": "tomorrow+10h",
		"time_zone": "Europe/Stockholm",
	}}, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	start, _ := got["start"].(map[string]any)
	ts, _ := start["dateTime"].(string)
	parsed, perr := time.Parse(time.RFC3339, ts)
	if perr != nil {
		t.Fatalf("start %q is not a timestamp: %v", ts, perr)
	}
	if !parsed.After(time.Now()) {
		t.Errorf("relative start resolved into the past: %s", ts)
	}

	if _, err := executeCreateEvent(context.Background(), core.Job{Params: map[string]any{
		"account": "default", "summary": "Semester", "start": "2026-07-01", "end": "2026-07-15",
	}}, nil); err != nil {
		t.Fatalf("create all-day: %v", err)
	}
	start, _ = got["start"].(map[string]any)
	if start["date"] != "2026-07-01" {
		t.Errorf("all-day start = %v, want an untouched plain date", got["start"])
	}
}
