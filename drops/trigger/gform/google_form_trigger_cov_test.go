package gform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

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

func TestReadCursor_NilAndError(t *testing.T) {
	// nil reader → "".
	SetCursorStore(nil, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	if got := readCursor(context.Background(), "t", "n"); got != "" {
		t.Errorf("nil reader = %q", got)
	}
	// reader returning an error → "" (treat as start-from-beginning).
	SetCursorStore(
		func(_ context.Context, _, _ string) (string, error) {
			return "ignored", context.Canceled
		},
		nil,
	)
	if got := readCursor(context.Background(), "t", "n"); got != "" {
		t.Errorf("error reader = %q, want empty", got)
	}
}

func TestWriteCursor_NilWriter(t *testing.T) {
	SetCursorStore(nil, nil)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	if err := writeCursor(context.Background(), "t", "n", "v"); err != nil {
		t.Errorf("nil writer should be a no-op, got %v", err)
	}
}

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

// --- soft cursor-write failure ----------------------------------------------

func TestExecute_CursorWriteFailure_StillEmits(t *testing.T) {
	fs := formServer{
		titles: map[string]string{"q1": "Name"},
		pages:  [][]map[string]any{{resp("r1", "2026-06-01T10:00:00Z", map[string]string{"q1": "Ada"})}},
	}
	srv := httptest.NewServer(fs.handler(t))
	defer srv.Close()
	clearTitleCache()
	SetHTTPBase(srv.URL)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	// Reader returns nothing (first fire); writer always fails so the soft
	// failure branch runs — data is still emitted.
	SetCursorStore(
		func(_ context.Context, _, _ string) (string, error) { return "", nil },
		func(_ context.Context, _, _, _ string) error { return context.Canceled },
	)
	t.Cleanup(func() {
		SetHTTPBase(formsAPIBase)
		SetTokenLookup(nil)
		SetCursorStore(nil, nil)
	})

	res := runTrigger(t, "acme")
	out := responsesOf(t, res)
	if len(out) != 1 || out[0]["Name"] != "Ada" {
		t.Fatalf("expected data despite cursor write failure, got %+v", out)
	}
}
