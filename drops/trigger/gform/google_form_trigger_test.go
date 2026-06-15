package gform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
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
	SetCursorStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() {
		SetHTTPBase(formsAPIBase)
		SetTokenLookup(nil)
		SetCursorStore(nil, nil)
	})
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

func TestFirstFire_EmitsAllAndWritesCursor(t *testing.T) {
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

	out := responsesOf(t, runTrigger(t, "acme"))
	if len(out) != 2 {
		t.Fatalf("want 2 responses, got %d", len(out))
	}
	if out[0]["Full Name"] != "Ada" || out[0]["Email"] != "ada@x" {
		t.Errorf("row keyed by title wrong: %+v", out[0])
	}
	if out[0]["responseId"] != "r1" || out[0]["submittedTime"] != "2026-06-01T10:00:00Z" {
		t.Errorf("missing responseId/submittedTime: %+v", out[0])
	}
	if got := store["acme/cursor.gform.flowA.trigger1"]; got != "2026-06-02T10:00:00Z" {
		t.Errorf("cursor = %q, want newest", got)
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
	store := withEnv(t, srv.URL)

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
	withEnv(t, srv.URL)

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
	withEnv(t, srv.URL)

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
	withEnv(t, srv.URL)

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
	withEnv(t, srv.URL)

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
