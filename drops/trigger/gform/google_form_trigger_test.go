// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// TestMain enables the operator private-egress opt-in so httptest servers
// on loopback aren't blocked by the SSRF guard (mirrors drops/sheets).
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// formServer stands in for the Forms API: GET /forms/{id} returns the
// structure (question titles); GET /forms/{id}/responses returns a fixed
// (optionally paged) response list. pages lets a test exercise paging.
type formServer struct {
	titles map[string]string  // questionId -> title
	pages  [][]map[string]any // each element is one page's "responses" array
}

func (fs formServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			page := 0
			if tok := r.URL.Query().Get("pageToken"); tok != "" {
				// pageToken is "p<N>" pointing at the page to serve.
				_, _ = jsonScan(tok[1:], &page)
			}
			body := map[string]any{}
			if page < len(fs.pages) {
				body["responses"] = fs.pages[page]
			}
			if page+1 < len(fs.pages) {
				body["nextPageToken"] = "p" + itoa(page+1)
			}
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		// form structure
		items := make([]map[string]any, 0, len(fs.titles))
		for qid, title := range fs.titles {
			items = append(items, map[string]any{
				"itemId": qid,
				"title":  title,
				"questionItem": map[string]any{
					"question": map[string]any{"questionId": qid},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	}
}

// resp builds one response object for the fixtures.
func resp(id, submitted string, answers map[string]string) map[string]any {
	a := map[string]any{}
	for qid, val := range answers {
		a[qid] = map[string]any{
			"questionId":  qid,
			"textAnswers": map[string]any{"answers": []map[string]any{{"value": val}}},
		}
	}
	return map[string]any{
		"responseId":        id,
		"lastSubmittedTime": submitted,
		"answers":           a,
	}
}

// withEnv points the package at the test server, a fake token, and an
// in-memory cursor store; returns the cursor map for assertions.
func withEnv(t *testing.T, base string) map[string]string {
	t.Helper()
	store := map[string]string{}
	clearTitleCache() // fixtures all reuse form_id "F1"; don't let titles bleed across tests
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() {
		SetHTTPBase(formsAPIBase)
		SetTokenLookup(nil)
		cursor.SetStore(nil, nil)
	})
	return store
}

// withEnvWatching is withEnv plus a watermark old enough that every fixture
// response counts as new — i.e. a flow that is already past its baseline.
//
// The FIRST fire deliberately emits nothing now (see the baseline branch in
// executeGoogleFormTrigger), so a test about output SHAPE has to start from a
// flow that is already watching, or it would be asserting on an empty batch.
func withEnvWatching(t *testing.T, base string) map[string]string {
	t.Helper()
	store := withEnv(t, base)
	store["acme/cursor.gform.flowA.trigger1"] = "2000-01-01T00:00:00Z"
	return store
}

func runTrigger(t *testing.T, tenant string) core.Result {
	t.Helper()
	res, err := executeGoogleFormTrigger(context.Background(), core.Job{
		Tenant:  tenant,
		GraphID: "flowA",
		NodeID:  "trigger1",
		Params:  map[string]any{"form_id": "F1", "account": "default"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	return res
}

func responsesOf(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	out, ok := res.Output["responses"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("responses type = %T", res.Output["responses"].Inline)
	}
	return out
}

// The first fire records where the form is up to and emits NOTHING.
//
// It used to emit everything, which made this the only watcher in the product
// that processed a form's whole back catalogue the moment you published: turn
// the flow on against a form with 500 existing responses and all 500 went
// through the step that acts on each one. Every sibling watcher baselines
// silently, and two of them even carry comments claiming they mirror this one.
func TestFirstFire_BaselinesSilently(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Full Name", "q2": "Email"},
		pages: [][]map[string]any{{
			resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Ada", "q2": "ada@x"}),
			resp("r2", "2026-06-02T10:00:00Z", map[string]string{"q1": "Bo", "q2": "bo@y"}),
		}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	store := withEnv(t, srv.URL)

	res := runTrigger(t, "acme")
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q (%+v)", res.Status, res.Error)
	}
	if _, ok := res.Output["responses"]; ok {
		t.Error("the first fire emitted responses; it must baseline silently")
	}
	// It still records the position, so the NEXT response is picked up.
	if got := store["acme/cursor.gform.flowA.trigger1"]; got != "2026-06-02T10:00:00Z" {
		t.Errorf("baseline cursor = %q, want the newest existing response", got)
	}
}

// And once it is watching, a response that arrives AFTER the baseline comes
// through — the baseline must not swallow anything but what was already there.
func TestFirstFire_ThenEmitsWhatArrivesNext(t *testing.T) {
	existing := resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Old"})
	before := formServer{
		titles: map[string]string{"q1": "Name"},
		pages:  [][]map[string]any{{existing}},
	}
	srv := httptest.NewServer(before.handler(t))
	defer srv.Close()
	withEnv(t, srv.URL)

	if res := runTrigger(t, "acme"); res.Output["responses"].Inline != nil {
		t.Fatal("the baseline fire emitted responses")
	}

	// A new response arrives. The fixture handler takes its pages by value, so
	// the second state is a second server pointed at the same cursor store.
	after := formServer{
		titles: map[string]string{"q1": "Name"},
		pages: [][]map[string]any{{
			existing,
			resp("r2", "2026-06-03T10:00:00Z", map[string]string{"q1": "New"}),
		}},
	}
	srv2 := httptest.NewServer(after.handler(t))
	defer srv2.Close()
	clearTitleCache()
	SetHTTPBase(srv2.URL)

	out := responsesOf(t, runTrigger(t, "acme"))
	if len(out) != 1 || out[0]["Name"] != "New" {
		t.Fatalf("after the baseline, got %+v; want just the new response", out)
	}
}

func TestSecondFire_OnlyNewer(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Name"},
		pages: [][]map[string]any{{
			resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Ada"}),
			resp("r2", "2026-06-03T10:00:00Z", map[string]string{"q1": "Cy"}),
		}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	store := withEnv(t, srv.URL)
	// Pretend r1 was already seen.
	store["acme/cursor.gform.flowA.trigger1"] = "2026-06-01T10:00:00Z"

	out := responsesOf(t, runTrigger(t, "acme"))
	if len(out) != 1 || out[0]["Name"] != "Cy" {
		t.Fatalf("want only the newer response, got %+v", out)
	}
	if got := store["acme/cursor.gform.flowA.trigger1"]; got != "2026-06-03T10:00:00Z" {
		t.Errorf("cursor advanced to %q", got)
	}
}

func TestNoNewResponses_EmptyNoCursorChange(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Name"},
		pages:  [][]map[string]any{{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Ada"})}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	store := withEnv(t, srv.URL)
	store["acme/cursor.gform.flowA.trigger1"] = "2026-06-01T10:00:00Z" // same as the only response

	res := runTrigger(t, "acme")
	// An empty poll emits NO outputs at all — ports without values make
	// their edges dormant, so the dispatcher skips the rest of the flow
	// (an empty check is a non-event, not a run).
	if len(res.Output) != 0 {
		t.Fatalf("empty poll must emit no outputs, got %v", res.Output)
	}
	if res.Status != core.StatusOK {
		t.Errorf("status = %q, want ok", res.Status)
	}
	if got := store["acme/cursor.gform.flowA.trigger1"]; got != "2026-06-01T10:00:00Z" {
		t.Errorf("cursor should be unchanged, got %q", got)
	}
}

func TestPagination_GathersAllPages(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Name"},
		pages: [][]map[string]any{
			{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "A"})},
			{resp("r2", "2026-06-02T10:00:00Z", map[string]string{"q1": "B"})},
		},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	store := withEnvWatching(t, srv.URL)

	out := responsesOf(t, runTrigger(t, "acme"))
	if len(out) != 2 {
		t.Fatalf("want 2 across pages, got %d", len(out))
	}
	if got := store["acme/cursor.gform.flowA.trigger1"]; got != "2026-06-02T10:00:00Z" {
		t.Errorf("cursor = %q", got)
	}
}

func TestMultiValueAnswer_Joined(t *testing.T) {
	fs := formServer{titles: map[string]string{"q1": "Toppings"}}
	fs.pages = [][]map[string]any{{
		map[string]any{
			"responseId":        "r1",
			"lastSubmittedTime": "2026-06-01T10:00:00Z",
			"answers": map[string]any{
				"q1": map[string]any{
					"questionId": "q1",
					"textAnswers": map[string]any{"answers": []map[string]any{
						{"value": "cheese"}, {"value": "ham"},
					}},
				},
			},
		},
	}}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	withEnvWatching(t, srv.URL)

	out := responsesOf(t, runTrigger(t, "acme"))
	if out[0]["Toppings"] != "cheese, ham" {
		t.Errorf("joined = %v", out[0]["Toppings"])
	}
}

func TestRespondentEmail_SurfacedWhenPresent(t *testing.T) {
	fs := formServer{titles: map[string]string{"q1": "Message"}}
	fs.pages = [][]map[string]any{{
		map[string]any{
			"responseId":        "r1",
			"lastSubmittedTime": "2026-06-01T10:00:00Z",
			"respondentEmail":   "alice@example.com",
			"answers": map[string]any{
				"q1": map[string]any{
					"questionId":  "q1",
					"textAnswers": map[string]any{"answers": []map[string]any{{"value": "hi"}}},
				},
			},
		},
	}}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	withEnvWatching(t, srv.URL)

	out := responsesOf(t, runTrigger(t, "acme"))
	if out[0]["email"] != "alice@example.com" {
		t.Errorf("email = %v", out[0]["email"])
	}
}

func TestRespondentEmail_OmittedWhenAbsent(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Message"},
		pages:  [][]map[string]any{{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "hi"})}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	withEnvWatching(t, srv.URL)

	out := responsesOf(t, runTrigger(t, "acme"))
	if _, present := out[0]["email"]; present {
		t.Errorf("email key should be absent when the form doesn't collect it, got %+v", out[0])
	}
}

func TestUnknownQuestionFallsBackToID(t *testing.T) {
	fs := formServer{
		titles: map[string]string{}, // no titles known
		pages:  [][]map[string]any{{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"qZ": "x"})}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	withEnvWatching(t, srv.URL)

	out := responsesOf(t, runTrigger(t, "acme"))
	if out[0]["qZ"] != "x" {
		t.Errorf("expected fallback to questionId key, got %+v", out[0])
	}
}

func TestAuthError_WhenNoToken(t *testing.T) {
	SetTokenLookup(nil)
	res, err := executeGoogleFormTrigger(context.Background(), core.Job{
		Tenant: "acme", GraphID: "f", NodeID: "n",
		Params: map[string]any{"form_id": "F1"},
	}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "auth" {
		t.Fatalf("want auth error, got status=%q err=%+v", res.Status, res.Error)
	}
}

func TestFormsAPIError_Surfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "insufficient scope"}})
	}))
	defer srv.Close()
	withEnv(t, srv.URL)

	res := runTrigger(t, "acme")
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "forms_error" {
		t.Fatalf("want forms_error, got status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "insufficient scope") {
		t.Errorf("message = %q", res.Error.Message)
	}
}

func TestTitleCache_AvoidsRepeatFetch(t *testing.T) {
	// Two FieldNames calls for the same form within the TTL should hit
	// forms.get (the structure endpoint) only once.
	var structCalls int
	fs := formServer{titles: map[string]string{"q1": "Name", "q2": "Email"}}
	inner := fs.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			structCalls++
		}
		inner(w, r)
	}))
	defer srv.Close()
	withEnv(t, srv.URL)

	job := core.Job{Params: map[string]any{"form_id": "F1", "account": "default"}}
	for i := 0; i < 2; i++ {
		if _, err := FieldNames(context.Background(), job); err != nil {
			t.Fatalf("FieldNames: %v", err)
		}
	}
	if structCalls != 1 {
		t.Errorf("forms.get called %d times, want 1 (second served from cache)", structCalls)
	}
}

func TestExtractFormID(t *testing.T) {
	cases := map[string]string{
		"PLAIN_ID": "PLAIN_ID",
		"https://docs.google.com/forms/d/ABC-123_x/edit":    "ABC-123_x",
		"https://docs.google.com/forms/d/e/LONGID/viewform": "LONGID",
	}
	for in, want := range cases {
		if got := extractFormID(in); got != want {
			t.Errorf("extractFormID(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- tiny test helpers (avoid extra imports) -------------------------------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func jsonScan(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return n, nil
}

// --- pure helper coverage ---------------------------------------------------

func TestSanitizeTitle_Cov(t *testing.T) {
	cases := map[string]string{
		"  hello   world ": "hello world",
		"":                 "untitled",
		"   ":              "untitled",
		"a\t\nb":           "a b",
	}
	for in, want := range cases {
		if got := sanitizeTitle(in); got != want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTime_Cov(t *testing.T) {
	// RFC3339Nano path.
	if _, err := parseTime("2026-06-01T10:00:00.5Z"); err != nil {
		t.Errorf("nano parse: %v", err)
	}
	// RFC3339 (non-nano) fallback path.
	if _, err := parseTime("2026-06-01T10:00:00Z"); err != nil {
		t.Errorf("rfc3339 parse: %v", err)
	}
	// Unparseable → error from the fallback.
	if _, err := parseTime("not-a-time"); err == nil {
		t.Errorf("expected error for bad time")
	}
}

func TestNewerThan_Cov(t *testing.T) {
	// Empty cursor → always newer.
	if !newerThan("2026-06-01T10:00:00Z", "") {
		t.Error("empty cursor should make everything newer")
	}
	// Both parseable, ts after cursor.
	if !newerThan("2026-06-02T10:00:00Z", "2026-06-01T10:00:00Z") {
		t.Error("later ts should be newer")
	}
	if newerThan("2026-06-01T10:00:00Z", "2026-06-02T10:00:00Z") {
		t.Error("earlier ts should not be newer")
	}
	// Unparseable ts falls back to lexical compare.
	if !newerThan("zzz", "aaa") {
		t.Error("lexical fallback: zzz > aaa")
	}
	if newerThan("aaa", "zzz") {
		t.Error("lexical fallback: aaa < zzz")
	}
}

func TestMaxTime_Cov(t *testing.T) {
	if got := maxTime("", "b"); got != "b" {
		t.Errorf("maxTime(empty,b) = %q", got)
	}
	if got := maxTime("a", ""); got != "a" {
		t.Errorf("maxTime(a,empty) = %q", got)
	}
	if got := maxTime("2026-06-01T10:00:00Z", "2026-06-02T10:00:00Z"); got != "2026-06-02T10:00:00Z" {
		t.Errorf("maxTime later-b = %q", got)
	}
	if got := maxTime("2026-06-03T10:00:00Z", "2026-06-02T10:00:00Z"); got != "2026-06-03T10:00:00Z" {
		t.Errorf("maxTime later-a = %q", got)
	}
}

func TestFormsBaseURL_ParamWins(t *testing.T) {
	job := core.Job{Params: map[string]any{"base_url": "https://override.example"}}
	if got := formsBaseURL(job); got != "https://override.example" {
		t.Errorf("formsBaseURL = %q, want override", got)
	}
}

// --- cursor store guards ----------------------------------------------------

// --- mapAnswers collision ---------------------------------------------------

func TestMapAnswers_TitleCollisionDisambiguated(t *testing.T) {
	// Two questions resolve to the same title → the second is suffixed with
	// its questionId. Build the formResponse via JSON to avoid restating its
	// anonymous-struct shape.
	raw := resp("r1", "2026-06-01T10:00:00Z", map[string]string{"qa": "first", "qb": "second"})
	blob, _ := json.Marshal(raw)
	var r formResponse
	if err := json.Unmarshal(blob, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	titles := map[string]string{"qa": "Name", "qb": "Name"}

	out := mapAnswers(r, titles)
	// One key is "Name", the other "Name (qa)" or "Name (qb)" depending on
	// map iteration order. Both values must be present under distinct keys.
	base, hasBase := out["Name"]
	suffixedA, hasA := out["Name (qa)"]
	suffixedB, hasB := out["Name (qb)"]
	if !hasBase || (!hasA && !hasB) {
		t.Fatalf("expected collision disambiguation, got %+v", out)
	}
	_ = base
	_ = suffixedA
	_ = suffixedB
}

// --- FieldNames error/dedup paths -------------------------------------------

func TestFieldNames_MissingFormID(t *testing.T) {
	_, err := FieldNames(context.Background(), core.Job{Params: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "form_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestFieldNames_TokenError(t *testing.T) {
	clearTitleCache()
	SetTokenLookup(nil) // no token resolver → resolveToken errors
	t.Cleanup(func() { SetTokenLookup(nil) })
	_, err := FieldNames(context.Background(), core.Job{Params: map[string]any{"form_id": "F1"}})
	if err == nil {
		t.Fatal("expected token error")
	}
}

func TestFieldNames_FetchTitlesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
	}))
	defer srv.Close()
	withEnv(t, srv.URL)
	_, err := FieldNames(context.Background(), core.Job{Params: map[string]any{"form_id": "F1", "account": "default"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
}

func TestFieldNames_DedupesAndSorts(t *testing.T) {
	// Two questions with the same sanitized title collapse to one field; the
	// result is sorted and carries the structural keys.
	fs := formServer{titles: map[string]string{"q1": "Name", "q2": "Name", "q3": "Age"}}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	withEnv(t, srv.URL)

	got, err := FieldNames(context.Background(), core.Job{Params: map[string]any{"form_id": "F1", "account": "default"}})
	if err != nil {
		t.Fatalf("FieldNames: %v", err)
	}
	// Expect: Age, Name (deduped, sorted), then email, responseId, submittedTime.
	want := []string{"Age", "Name", "email", "responseId", "submittedTime"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// --- fetchTitles / fetchNewResponses decode + status errors -----------------

func TestFetchTitles_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()
	withEnv(t, srv.URL)
	res := runTrigger(t, "acme")
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "forms_error" {
		t.Fatalf("want forms_error, got status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "decode") {
		t.Errorf("message = %q", res.Error.Message)
	}
}

func TestFetchNewResponses_StatusError(t *testing.T) {
	// forms.get OK, but responses.list returns a non-2xx.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"list boom"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	withEnv(t, srv.URL)
	res := runTrigger(t, "acme")
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "forms_error" {
		t.Fatalf("want forms_error, got status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "list boom") {
		t.Errorf("message = %q", res.Error.Message)
	}
}

func TestFetchNewResponses_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = w.Write([]byte("{bad"))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	withEnv(t, srv.URL)
	res := runTrigger(t, "acme")
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "forms_error" {
		t.Fatalf("want forms_error, got status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "decode") {
		t.Errorf("message = %q", res.Error.Message)
	}
}

// --- cursor-write failure ---------------------------------------------------

// Once responses have been emitted, a failed cursor write is safe to carry on
// past: the trigger is at-least-once, so the next fire re-emits this batch.
func TestExecute_CursorWriteFailure_SteadyStateStillEmits(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Name"},
		pages:  [][]map[string]any{{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Ada"})}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	clearTitleCache()
	SetHTTPBase(srv.URL)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	// A watermark is already stored (so this is NOT a first fire), and the
	// writer always fails.
	cursor.SetStore(
		func(_ context.Context, _, _ string) (string, error) { return "2026-05-01T00:00:00Z", nil },
		func(_ context.Context, _, _, _ string) error { return context.Canceled },
	)
	t.Cleanup(func() {
		SetHTTPBase(formsAPIBase)
		SetTokenLookup(nil)
		cursor.SetStore(nil, nil)
	})

	res := runTrigger(t, "acme")
	out := responsesOf(t, res)
	if len(out) != 1 || out[0]["Name"] != "Ada" {
		t.Fatalf("expected data despite cursor write failure, got %+v", out)
	}
}

// The FIRST fire is the exception, and this trigger's first fire is unlike its
// siblings': with no stored watermark every response counts as new, so it
// emits the form's entire existing backlog. Emitting that without recording
// where it got to means the NEXT fire emits the same backlog again — and a
// write that keeps failing does it for ever, against a step whose contract is
// "each response exactly once". So it fails and emits nothing instead.
func TestExecute_CursorWriteFailure_FirstFireRefusesToEmit(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Name"},
		pages:  [][]map[string]any{{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Ada"})}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	clearTitleCache()
	SetHTTPBase(srv.URL)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	// Nothing stored (first fire) and the writer always fails.
	cursor.SetStore(
		func(_ context.Context, _, _ string) (string, error) { return "", nil },
		func(_ context.Context, _, _, _ string) error { return context.Canceled },
	)
	t.Cleanup(func() {
		SetHTTPBase(formsAPIBase)
		SetTokenLookup(nil)
		cursor.SetStore(nil, nil)
	})

	res := runTrigger(t, "acme")
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error on a first fire that could not record its position", res.Status)
	}
	if res.Error == nil || res.Error.Code != "cursor_baseline_failed" {
		t.Errorf("error = %+v, want cursor_baseline_failed", res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("emitted %d port(s); a batch that cannot be recorded must not go downstream", len(res.Output))
	}
}

// clearTitleCache drops every cached form structure. Used by tests to keep
// fixtures (which all reuse one form_id) isolated.
func clearTitleCache() {
	titleCacheMu.Lock()
	defer titleCacheMu.Unlock()
	titleCache = map[string]titleCacheEntry{}
}
