// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
)

// withTriggerEnv is withCalEnv plus a cursor store, which every watch needs to
// tell a change it has reported from one it hasn't. The returned map IS the
// store, so a test can seed a starting position or read back where the watch
// got to.
func withTriggerEnv(t *testing.T, base string) map[string]string {
	t.Helper()
	withCalEnv(t, base)
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })
	return store
}

func triggerJob(params map[string]any) core.Job {
	return core.Job{ID: "j1", Tenant: "t", GraphID: "G", NodeID: "N", Params: params}
}

const changeCursorKey = "t/cursor.gcal.change.G.N"
const startCursorKey = "t/cursor.gcal.start.G.N"

func eventsJSON(t *testing.T, w http.ResponseWriter, items ...map[string]any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(map[string]any{"items": items}); err != nil {
		t.Errorf("encode fixture: %v", err)
	}
}

func rows(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	ref, ok := res.Output["events"]
	if !ok {
		return nil
	}
	got, ok := ref.Inline.([]map[string]any)
	if !ok {
		t.Fatalf("events output is %T, want []map[string]any", ref.Inline)
	}
	return got
}

// ---------- gcal_on_event_change ----------

func TestOnEventChange_FirstCheckBaselinesWithoutFiring(t *testing.T) {
	// The point of the baseline is that publishing a watch does not replay the
	// calendar's history — and it costs nothing, so it must not even call the
	// API to work out where "now" is.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the first check must not read the calendar at all")
		eventsJSON(t, w)
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)

	res, err := executeOnEventChange(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("baseline emitted %v, want nothing", res.Output)
	}
	if _, ok := parseStamp(store[changeCursorKey]); !ok {
		t.Errorf("baseline recorded %q, want a timestamp", store[changeCursorKey])
	}
}

func TestOnEventChange_ClassifiesEveryKindOfChange(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		eventsJSON(t, w,
			map[string]any{
				"id": "new", "status": "confirmed", "summary": "Design review",
				"created": "2026-06-16T09:05:00.000Z", "updated": "2026-06-16T09:05:00.000Z",
				"start": map[string]any{"dateTime": "2026-06-18T13:00:00Z"},
				"end":   map[string]any{"dateTime": "2026-06-18T14:00:00Z"},
			},
			map[string]any{
				"id": "moved", "status": "confirmed", "summary": "Standup",
				"created": "2026-01-02T08:00:00.000Z", "updated": "2026-06-16T09:06:00.000Z",
				"start": map[string]any{"dateTime": "2026-06-17T09:30:00Z"},
				"end":   map[string]any{"dateTime": "2026-06-17T09:45:00Z"},
			},
			map[string]any{
				"id": "gone", "status": "cancelled", "summary": "Retro",
				"created": "2026-01-02T08:00:00.000Z", "updated": "2026-06-16T09:07:00.000Z",
				"recurringEventId": "retro_R20260101T090000",
			},
		)
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[changeCursorKey] = "2026-06-16T09:00:00Z"

	res, err := executeOnEventChange(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	// Cancellations are the reason for showDeleted, and singleEvents=false is
	// what keeps an edit to a series one change instead of one per occurrence.
	for _, want := range []string{
		"updatedMin=2026-06-16T09%3A00%3A00Z", "showDeleted=true",
		"singleEvents=false", "orderBy=updated",
	} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}

	got := rows(t, res)
	if len(got) != 3 {
		t.Fatalf("emitted %d changes, want 3: %+v", len(got), got)
	}
	for i, want := range []string{"created", "updated", "cancelled"} {
		if got[i]["change"] != want {
			t.Errorf("change #%d = %v, want %q", i, got[i]["change"], want)
		}
	}
	if got[0]["summary"] != "Design review" || got[0]["start"] != "2026-06-18T13:00:00Z" {
		t.Errorf("first change = %+v", got[0])
	}
	if got[2]["recurring_event_id"] != "retro_R20260101T090000" {
		t.Errorf("cancelled instance lost its series: %+v", got[2])
	}
	if res.Output["count"].Inline != "3" {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
	// The watermark lands on the newest change seen, so the next check starts
	// strictly past it.
	if store[changeCursorKey] != "2026-06-16T09:07:00.000Z" {
		t.Errorf("watermark = %q", store[changeCursorKey])
	}
}

func TestOnEventChange_ChangeOnTheWatermarkIsNotReportedTwice(t *testing.T) {
	// updatedMin is inclusive, so the newest change always comes back on the
	// next check. Reporting it again would mean every fire duplicated its last
	// event for ever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, map[string]any{
			"id": "same", "status": "confirmed", "summary": "Standup",
			"created": "2026-01-02T08:00:00.000Z", "updated": "2026-06-16T09:07:00.000Z",
			"start": map[string]any{"dateTime": "2026-06-17T09:30:00Z"},
		})
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[changeCursorKey] = "2026-06-16T09:07:00.000Z"

	res, err := executeOnEventChange(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("re-reported the change on the watermark: %v", res.Output)
	}
	if store[changeCursorKey] != "2026-06-16T09:07:00.000Z" {
		t.Errorf("watermark moved to %q on an empty check", store[changeCursorKey])
	}
}

func TestOnEventChange_FilterHidesChangesButStillAdvances(t *testing.T) {
	// A filtered-out change has still been READ, so the watermark must pass it:
	// leaving it behind would re-read it on every check for ever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w,
			map[string]any{
				"id": "new", "status": "confirmed", "summary": "Design review",
				"created": "2026-06-16T09:05:00.000Z", "updated": "2026-06-16T09:05:00.000Z",
			},
			map[string]any{
				"id": "moved", "status": "confirmed", "summary": "Standup",
				"created": "2026-01-02T08:00:00.000Z", "updated": "2026-06-16T09:06:00.000Z",
			},
		)
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[changeCursorKey] = "2026-06-16T09:00:00Z"

	res, err := executeOnEventChange(context.Background(),
		triggerJob(map[string]any{"change": "created"}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := rows(t, res)
	if len(got) != 1 || got[0]["id"] != "new" {
		t.Fatalf("emitted %+v, want just the newly booked event", got)
	}
	if store[changeCursorKey] != "2026-06-16T09:06:00.000Z" {
		t.Errorf("watermark = %q, want it past the filtered-out change", store[changeCursorKey])
	}
}

func TestOnEventChange_LimitDoesNotCutThroughASharedStamp(t *testing.T) {
	// Six meetings booked by one script share an update stamp. The next check
	// resumes strictly past the watermark, so a cut inside that group would
	// lose the rest of it — the limit gives way until the stamp changes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w,
			map[string]any{"id": "a", "status": "confirmed", "created": "2026-06-16T09:05:00.000Z", "updated": "2026-06-16T09:05:00.000Z"},
			map[string]any{"id": "b", "status": "confirmed", "created": "2026-06-16T09:05:00.000Z", "updated": "2026-06-16T09:05:00.000Z"},
			map[string]any{"id": "c", "status": "confirmed", "created": "2026-06-16T09:06:00.000Z", "updated": "2026-06-16T09:06:00.000Z"},
		)
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[changeCursorKey] = "2026-06-16T09:00:00Z"

	res, err := executeOnEventChange(context.Background(),
		triggerJob(map[string]any{"limit": 1}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := rows(t, res)
	if len(got) != 2 || got[0]["id"] != "a" || got[1]["id"] != "b" {
		t.Fatalf("emitted %+v, want the whole tied group and nothing past it", got)
	}
	if store[changeCursorKey] != "2026-06-16T09:05:00.000Z" {
		t.Errorf("watermark = %q, want the tied stamp so 'c' is next", store[changeCursorKey])
	}
}

func TestOnEventChange_UnparseableWatermarkRebaselines(t *testing.T) {
	// A position that no longer parses is the one case where carrying on would
	// mean re-reporting the whole calendar. Re-baseline instead of firing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a re-baseline must not read the calendar")
		eventsJSON(t, w)
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[changeCursorKey] = "not-a-timestamp"

	res, err := executeOnEventChange(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("emitted %v on a re-baseline", res.Output)
	}
	if _, ok := parseStamp(store[changeCursorKey]); !ok {
		t.Errorf("re-baseline left %q behind", store[changeCursorKey])
	}
}

func TestOnEventChange_CursorReadFailureStopsWithoutOverwriting(t *testing.T) {
	// The failure that must NOT be read as "first check": the position is
	// probably still there, and re-baselining over it would swallow every
	// change since the last successful check.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w)
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	var wrote bool
	cursor.SetStore(
		func(_ context.Context, _, _ string) (string, error) { return "", errors.New("store down") },
		func(_ context.Context, _, _, _ string) error { wrote = true; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })

	res, err := executeOnEventChange(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "cursor_unavailable" {
		t.Fatalf("result = %q %+v, want a cursor_unavailable error", res.Status, res.Error)
	}
	if wrote {
		t.Error("overwrote the stored position after failing to read it")
	}
}

func TestOnEventChange_RejectsUnknownChangeFilter(t *testing.T) {
	withTriggerEnv(t, "http://127.0.0.1:1")
	res, err := executeOnEventChange(context.Background(),
		triggerJob(map[string]any{"change": "renamed"}), nil)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("result = %q %+v, want bad_param", res.Status, res.Error)
	}
}

func TestOnEventChange_APIErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"insufficient scope"}}`))
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[changeCursorKey] = "2026-06-16T09:00:00Z"

	res, err := executeOnEventChange(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "gcal_error" {
		t.Fatalf("result = %q %+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "insufficient scope") {
		t.Errorf("error message = %q", res.Error.Message)
	}
	if store[changeCursorKey] != "2026-06-16T09:00:00Z" {
		t.Errorf("a failed read moved the watermark to %q", store[changeCursorKey])
	}
}

// ---------- gcal_on_event_start ----------

// startingAt is an event fixture whose start is a given distance from now, in
// the shape events.list returns with singleEvents=true.
func startingAt(id string, in time.Duration) map[string]any {
	return map[string]any{
		"id": id, "status": "confirmed", "summary": id,
		"start": map[string]any{"dateTime": time.Now().UTC().Add(in).Format(time.RFC3339)},
		"end":   map[string]any{"dateTime": time.Now().UTC().Add(in + time.Hour).Format(time.RFC3339)},
	}
}

func TestOnEventStart_AnnouncesOnlyWhatIsAboutToStart(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		// timeMin/timeMax bound this loosely — Google returns anything
		// OVERLAPPING the window — so the fixture includes what the step has to
		// rule out itself.
		soon := startingAt("soon", 5*time.Minute)
		soon["location"] = "Room 2"
		cancelled := startingAt("cancelled", 2*time.Minute)
		cancelled["status"] = "cancelled"
		eventsJSON(t, w, soon, startingAt("later", 30*time.Minute),
			startingAt("in-progress", -time.Hour), cancelled)
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)

	res, err := executeOnEventStart(context.Background(),
		triggerJob(map[string]any{"lead_minutes": 10, "interval_seconds": 300}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	for _, want := range []string{"singleEvents=true", "orderBy=startTime", "timeMin=", "timeMax="} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}

	got := rows(t, res)
	if len(got) != 1 || got[0]["id"] != "soon" {
		t.Fatalf("announced %+v, want just the event starting in five minutes", got)
	}
	if got[0]["minutes_until_start"] != 5 {
		t.Errorf("minutes_until_start = %v, want 5", got[0]["minutes_until_start"])
	}
	if got[0]["location"] != "Room 2" {
		t.Errorf("announcement lost the event's details: %+v", got[0])
	}
	if res.Output["count"].Inline != "1" {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
	if !strings.Contains(store[startCursorKey], `"soon@`) {
		t.Errorf("stored reminders = %q, want the announced start", store[startCursorKey])
	}
}

func TestOnEventStart_AnnouncesEachStartOnce(t *testing.T) {
	start := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, map[string]any{
			"id": "ev1", "status": "confirmed", "summary": "Standup",
			"start": map[string]any{"dateTime": start},
		})
	}))
	defer srv.Close()
	withTriggerEnv(t, srv.URL)

	job := triggerJob(map[string]any{"lead_minutes": 10, "interval_seconds": 300})
	first, err := executeOnEventStart(context.Background(), job, nil)
	if err != nil || len(rows(t, first)) != 1 {
		t.Fatalf("first check announced %+v, err=%v", first.Output, err)
	}
	second, err := executeOnEventStart(context.Background(), job, nil)
	if err != nil || second.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", second.Status, second.Error)
	}
	if len(second.Output) != 0 {
		t.Errorf("announced the same start twice: %v", second.Output)
	}
}

func TestOnEventStart_MovedEventIsAnnouncedForItsNewTime(t *testing.T) {
	// The reminder worth having is the one for when the thing actually happens,
	// so the record of what has been announced is keyed by the start too.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, startingAt("ev1", 5*time.Minute))
	}))
	defer srv.Close()
	store := withTriggerEnv(t, srv.URL)
	store[startCursorKey] = `{"keys":["ev1@2026-06-18T13:00:00Z"]}`

	res, err := executeOnEventStart(context.Background(),
		triggerJob(map[string]any{"lead_minutes": 10, "interval_seconds": 300}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := rows(t, res); len(got) != 1 {
		t.Fatalf("announced %+v, want the moved event at its new time", got)
	}
	if !strings.Contains(store[startCursorKey], "2026-06-18T13:00:00Z") {
		t.Errorf("dropped the old reminder key: %q", store[startCursorKey])
	}
}

func TestOnEventStart_LateCheckDoesNotAnnounceThePast(t *testing.T) {
	// A daemon that was down overnight must not wake up and page about
	// yesterday, however long its interval says it has been away.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, startingAt("started-20m-ago", -20*time.Minute))
	}))
	defer srv.Close()
	withTriggerEnv(t, srv.URL)

	res, err := executeOnEventStart(context.Background(),
		triggerJob(map[string]any{"lead_minutes": 10, "interval_seconds": 86400}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("announced a start %v past the grace window: %v", startMaxGrace, res.Output)
	}
}

func TestOnEventStart_ShortIntervalStillToleratesALateCheck(t *testing.T) {
	// The floor under the grace window: a check landing a few seconds behind
	// schedule must not drop a start that fell in the gap.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, startingAt("just-started", -30*time.Second))
	}))
	defer srv.Close()
	withTriggerEnv(t, srv.URL)

	res, err := executeOnEventStart(context.Background(),
		triggerJob(map[string]any{"lead_minutes": 0, "interval_seconds": 1}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := rows(t, res); len(got) != 1 {
		t.Fatalf("announced %+v, want the start from thirty seconds ago", got)
	}
}

const allDayZone = "Europe/Stockholm"

func TestOnEventStart_AllDayEventsOnlyWhenAskedFor(t *testing.T) {
	// Tomorrow in the zone the step is GIVEN, not in UTC. An all-day event
	// starts at midnight where it is, so a date built from UTC put the event's
	// Stockholm midnight in the past for the two hours a day between 22:00 UTC
	// and midnight — the step correctly refused to announce a start that had
	// already happened, and this test called that a failure.
	loc, lerr := time.LoadLocation(allDayZone)
	if lerr != nil {
		t.Fatalf("load %s: %v", allDayZone, lerr)
	}
	tomorrow := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, map[string]any{
			"id": "holiday", "status": "confirmed", "summary": "Holiday",
			"start": map[string]any{"date": tomorrow},
			"end":   map[string]any{"date": tomorrow},
		})
	}))
	defer srv.Close()
	withTriggerEnv(t, srv.URL)

	// A day's notice reaches tomorrow's midnight whatever the hour now is.
	base := map[string]any{"lead_minutes": startMaxLeadMinutes, "interval_seconds": 300}
	res, err := executeOnEventStart(context.Background(), triggerJob(base), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("announced an all-day event nobody asked for: %v", res.Output)
	}

	withTriggerEnv(t, srv.URL) // a fresh record of what has been announced
	res, err = executeOnEventStart(context.Background(), triggerJob(map[string]any{
		"lead_minutes": startMaxLeadMinutes, "interval_seconds": 300,
		"include_all_day": true, "tz": allDayZone,
	}), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := rows(t, res); len(got) != 1 || got[0]["all_day"] != true {
		t.Fatalf("announced %+v, want tomorrow's all-day event", got)
	}
}

func TestOnEventStart_RejectsUnknownTimezone(t *testing.T) {
	withTriggerEnv(t, "http://127.0.0.1:1")
	res, err := executeOnEventStart(context.Background(),
		triggerJob(map[string]any{"tz": "Mars/Olympus"}), nil)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("result = %q %+v, want bad_param", res.Status, res.Error)
	}
}

func TestOnEventStart_CursorReadFailureStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		eventsJSON(t, w, startingAt("soon", 5*time.Minute))
	}))
	defer srv.Close()
	withCalEnv(t, srv.URL)
	cursor.SetStore(
		func(_ context.Context, _, _ string) (string, error) { return "", errors.New("store down") },
		func(_ context.Context, _, _, _ string) error { return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })

	res, err := executeOnEventStart(context.Background(), triggerJob(map[string]any{}), nil)
	if err != nil {
		t.Fatalf("unexpected transport err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "cursor_unavailable" {
		t.Fatalf("result = %q %+v, want a cursor_unavailable error", res.Status, res.Error)
	}
}

func TestWriteAnnounced_CapsOldestFirst(t *testing.T) {
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })

	prior := make([]string, startSeenCap)
	for i := range prior {
		prior[i] = fmt.Sprintf("old%d@t", i)
	}
	if err := writeAnnounced(context.Background(), "t", "n", prior, []string{"fresh@t"}); err != nil {
		t.Fatalf("writeAnnounced: %v", err)
	}
	var got announcedState
	if err := json.Unmarshal([]byte(store["t/n"]), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Keys) != startSeenCap {
		t.Fatalf("kept %d keys, want the cap of %d", len(got.Keys), startSeenCap)
	}
	if got.Keys[0] != "old1@t" {
		t.Errorf("evicted %q, want the oldest key dropped first", got.Keys[0])
	}
	if got.Keys[len(got.Keys)-1] != "fresh@t" {
		t.Errorf("newest key = %q, want the reminder just sent", got.Keys[len(got.Keys)-1])
	}
}
