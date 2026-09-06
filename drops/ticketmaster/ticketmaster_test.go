// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ticketmaster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
)

// fakeTM is a stand-in Discovery API. Tests read back the last query to assert
// what Ticketmaster is actually asked for.
type fakeTM struct {
	server    *httptest.Server
	lastQuery url.Values
	lastPath  string
	calls     int
	body      func(call int) string
}

func newFakeTM(t *testing.T, body func(call int) string) *fakeTM {
	t.Helper()
	f := &fakeTM{body: body}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path
		f.lastQuery = r.URL.Query()
		f.calls++
		_, _ = w.Write([]byte(f.body(f.calls)))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTM) job(p map[string]any) core.Job {
	params := map[string]any{"api_key": "test-key", "base_url": f.server.URL}
	for k, v := range p {
		params[k] = v
	}
	return core.Job{ID: "j1", Tenant: "acme", GraphID: "flow1", NodeID: "node1", Params: params}
}

// eventsPage renders a Discovery page from a list of (id, name) pairs.
func eventsPage(ids ...string) string {
	items := make([]string, 0, len(ids))
	for _, id := range ids {
		items = append(items, `{
			"id":"`+id+`","name":"Robyn","url":"https://www.ticketmaster.se/event/`+id+`",
			"images":[{"url":"https://small.jpg","width":100},{"url":"https://big.jpg","width":2048}],
			"dates":{"start":{"localDate":"2026-11-14","localTime":"20:00:00","dateTime":"2026-11-14T19:00:00Z"},"status":{"code":"onsale"}},
			"_embedded":{
				"venues":[{"name":"Avicii Arena","city":{"name":"Stockholm"},"country":{"countryCode":"SE"}}],
				"attractions":[{"name":"Robyn"}]
			}}`)
	}
	return `{"_embedded":{"events":[` + strings.Join(items, ",") + `]},
		"page":{"size":20,"totalElements":41,"totalPages":3,"number":0}}`
}

func TestSearchEvents_FlattensRows(t *testing.T) {
	f := newFakeTM(t, func(int) string { return eventsPage("Z1") })

	res, err := executeSearchEvents(context.Background(),
		f.job(map[string]any{"country_code": "se", "city": "Stockholm", "classification": "music", "limit": 50}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if f.lastPath != "/events.json" {
		t.Errorf("path = %q", f.lastPath)
	}
	q := f.lastQuery
	if q.Get("countryCode") != "SE" {
		t.Errorf("countryCode = %q, want it upper-cased to SE", q.Get("countryCode"))
	}
	if q.Get("city") != "Stockholm" || q.Get("classificationName") != "music" {
		t.Errorf("filters = %v", q)
	}
	if q.Get("size") != "50" {
		t.Errorf("size = %q, want 50", q.Get("size"))
	}
	// Date order, not the API's relevance default — a "what's coming up" list
	// that leads with next year's stadium show is the wrong list.
	if q.Get("sort") != "date,asc" {
		t.Errorf("sort = %q, want date,asc", q.Get("sort"))
	}
	if q.Get("apikey") != "test-key" {
		t.Errorf("apikey = %q", q.Get("apikey"))
	}

	rows, ok := res.Output["events"].Inline.([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("events = %#v", res.Output["events"].Inline)
	}
	row := rows[0]
	for field, want := range map[string]string{
		"id": "Z1", "name": "Robyn", "artist": "Robyn", "venue": "Avicii Arena",
		"city": "Stockholm", "country": "SE", "date": "2026-11-14", "time": "20:00:00",
		"status": "onsale",
	} {
		if row[field] != want {
			t.Errorf("row[%q] = %v, want %v", field, row[field], want)
		}
	}
	// Discovery returns every crop in no useful order, so the widest wins
	// rather than the first.
	if row["image"] != "https://big.jpg" {
		t.Errorf("image = %v, want the widest", row["image"])
	}
	if got := res.Output["total"].Inline; got != "41" {
		t.Errorf("total = %v, want 41", got)
	}
}

// The Search for pin is what a For each over an artist list wires, so it has
// to beat the keyword typed on the node.
func TestSearchEvents_InputKeywordOverridesParam(t *testing.T) {
	f := newFakeTM(t, func(int) string { return eventsPage("Z1") })
	job := f.job(map[string]any{"keyword": "from-param"})
	job.Input = map[string]core.Ref{"keyword": {MIME: "text/plain", Inline: "from-wire"}}

	if _, err := executeSearchEvents(context.Background(), job, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if f.lastQuery.Get("keyword") != "from-wire" {
		t.Errorf("keyword = %q, want the wired value", f.lastQuery.Get("keyword"))
	}
	// The wired value must not leak into the node's stored params.
	if job.Params["keyword"] != "from-param" {
		t.Errorf("the run mutated the node's params: %v", job.Params["keyword"])
	}
}

func TestEventQuery_ResolvesRelativeWindowAsBareUTC(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	q, err := eventQuery(core.Job{Params: map[string]any{"start_date": "now", "end_date": "+90d"}}, 20, now)
	if err != nil {
		t.Fatalf("eventQuery: %v", err)
	}
	// Discovery rejects a zone offset, so the stamp must end in Z with no
	// fractional seconds.
	if got := q.Get("startDateTime"); got != "2026-09-07T12:00:00Z" {
		t.Errorf("startDateTime = %q", got)
	}
	if got := q.Get("endDateTime"); got != "2026-12-06T12:00:00Z" {
		t.Errorf("endDateTime = %q", got)
	}
}

func TestSearchEvents_NotConnected(t *testing.T) {
	f := newFakeTM(t, func(int) string { t.Error("should not call the API without a key"); return "" })
	job := f.job(nil)
	delete(job.Params, "api_key")

	res, _ := executeSearchEvents(context.Background(), job, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result when the integration isn't connected")
	}
	if res.Error.Code != "auth" || !strings.Contains(res.Error.Message, "Apps page") {
		t.Errorf("error = %+v — it should say where to fix it", res.Error)
	}
}

func TestSearchEvents_RejectedKeyIsNamedAsSuch(t *testing.T) {
	f := &fakeTM{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"fault":{"faultstring":"Invalid ApiKey","detail":{"errorcode":"oauth.v2.InvalidApiKey"}}}`))
	}))
	t.Cleanup(f.server.Close)

	res, _ := executeSearchEvents(context.Background(), f.job(nil), nil)
	if res.Error == nil || res.Error.Code != "auth" {
		t.Fatalf("error = %+v, want code auth", res.Error)
	}
	if !strings.Contains(res.Error.Message, "Invalid ApiKey") {
		t.Errorf("message = %q, want Ticketmaster's own words", res.Error.Message)
	}
	// The URL carries the key, so it must never reach the message.
	if strings.Contains(res.Error.Message, "apikey") {
		t.Errorf("the error quotes the request URL, which carries the key: %q", res.Error.Message)
	}
}

func TestSearchEvents_QuotaExhaustedSaysWhatToDo(t *testing.T) {
	f := &fakeTM{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"fault":{"faultstring":"Rate limit quota violation"}}`))
	}))
	t.Cleanup(f.server.Close)

	res, _ := executeSearchEvents(context.Background(), f.job(nil), nil)
	if res.Error == nil || res.Error.Code != "rate_limited" {
		t.Fatalf("error = %+v, want code rate_limited", res.Error)
	}
}

// --- the trigger ----------------------------------------------------------

// memCursor wires the cursor store to a map so the trigger's seen-set survives
// between polls within one test.
func memCursor(t *testing.T) map[string]string {
	t.Helper()
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })
	return store
}

func TestOnNewEvent_FirstCheckLearnsWithoutFiring(t *testing.T) {
	memCursor(t)
	f := newFakeTM(t, func(int) string { return eventsPage("Z1", "Z2") })

	res, err := executeOnNewEvent(context.Background(), f.job(nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	// Publishing a watch on a search with a hundred existing events must not
	// page anyone about all hundred.
	if len(res.Output) != 0 {
		t.Errorf("first check emitted %v, want nothing", res.Output)
	}
}

func TestOnNewEvent_FiresOnlyOnTheNewOnes(t *testing.T) {
	memCursor(t)
	f := newFakeTM(t, func(call int) string {
		if call == 1 {
			return eventsPage("Z1", "Z2")
		}
		return eventsPage("Z1", "Z2", "Z3")
	})
	job := f.job(nil)

	if _, err := executeOnNewEvent(context.Background(), job, nil); err != nil {
		t.Fatalf("first check: %v", err)
	}
	res, err := executeOnNewEvent(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	rows, ok := res.Output["events"].Inline.([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("events = %#v, want only the new one", res.Output["events"].Inline)
	}
	if rows[0]["id"] != "Z3" {
		t.Errorf("fired on %v, want Z3", rows[0]["id"])
	}
	if got := res.Output["count"].Inline; got != "1" {
		t.Errorf("count = %v", got)
	}
}

func TestOnNewEvent_UnchangedSearchStaysQuiet(t *testing.T) {
	memCursor(t)
	f := newFakeTM(t, func(int) string { return eventsPage("Z1", "Z2") })
	job := f.job(nil)

	for i := 0; i < 3; i++ {
		res, err := executeOnNewEvent(context.Background(), job, nil)
		if err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
		if i > 0 && len(res.Output) != 0 {
			t.Fatalf("check %d fired %v on an unchanged search", i, res.Output)
		}
	}
}

// An event that drops out of the window and comes back must not re-fire while
// it is still remembered — the whole point of keeping the set rather than a
// high-water mark.
func TestOnNewEvent_DoesNotRefireAReturningEvent(t *testing.T) {
	memCursor(t)
	f := newFakeTM(t, func(call int) string {
		switch call {
		case 1:
			return eventsPage("Z1", "Z2")
		case 2:
			return eventsPage("Z1")
		default:
			return eventsPage("Z1", "Z2")
		}
	})
	job := f.job(nil)
	for i := 0; i < 2; i++ {
		if _, err := executeOnNewEvent(context.Background(), job, nil); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	res, err := executeOnNewEvent(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("third check: %v", err)
	}
	if len(res.Output) != 0 {
		t.Errorf("re-fired an event it had already seen: %v", res.Output)
	}
}

func TestWriteSeen_CapsOldestFirst(t *testing.T) {
	store := memCursor(t)
	prior := make([]string, seenCap)
	for i := range prior {
		prior[i] = "old" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if err := writeSeen(context.Background(), "acme", "cursor.k", []string{"new1", "new2"}, prior); err != nil {
		t.Fatalf("writeSeen: %v", err)
	}
	var s seenState
	if err := json.Unmarshal([]byte(store["acme/cursor.k"]), &s); err != nil {
		t.Fatalf("stored value: %v", err)
	}
	if len(s.IDs) != seenCap {
		t.Fatalf("kept %d ids, want the cap %d", len(s.IDs), seenCap)
	}
	// The newest survive; the two oldest were evicted to make room.
	if s.IDs[len(s.IDs)-1] != "new2" || s.IDs[len(s.IDs)-2] != "new1" {
		t.Errorf("tail = %v, want the newest ids last", s.IDs[len(s.IDs)-2:])
	}
	if s.IDs[0] != prior[2] {
		t.Errorf("head = %q, want the third-oldest after two evictions", s.IDs[0])
	}
}
