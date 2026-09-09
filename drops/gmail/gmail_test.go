// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/mailmsg"
)

func withGmailEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://gmail.googleapis.com/gmail/v1")
		SetTokenLookup(nil)
	})
}

func TestGmailSearch_ReturnsMessages(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "threadId": "t-" + id, "snippet": "snip " + id,
				"payload": map[string]any{
					"headers": []any{
						map[string]any{"name": "From", "value": id + "@x"},
						map[string]any{"name": "Subject", "value": "Hi " + id},
						map[string]any{"name": "Date", "value": "Tue, 10 Jun 2026"},
					},
					"mimeType": "text/plain",
					"body":     map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("body " + id))},
				},
			})
			return
		}
		gotQuery = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages":      []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
			"nextPageToken": "tok",
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{"query": "is:unread", "max_results": 10},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotQuery != "is:unread" {
		t.Errorf("q = %q", gotQuery)
	}
	msgs := res.Output["messages"].Inline.([]any)
	if len(msgs) != 2 || res.Output["next_page_token"].Inline != "tok" {
		t.Fatalf("out = %+v", res.Output)
	}
	// Every match is a REAL email record, never an ID stub.
	first := msgs[0].(map[string]any)
	if first["subject"] != "Hi a" || first["from"] != "a@x" || first["body"] != "body a" {
		t.Errorf("first match = %+v, want expanded email fields", first)
	}
}

func memCursor(t *testing.T) map[string]string {
	t.Helper()
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"|"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"|"+name] = value; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })
	return store
}

func searchServer(t *testing.T, ids []string, dateByID map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "threadId": "t-" + id, "internalDate": dateByID[id],
				"payload": map[string]any{"headers": []any{
					map[string]any{"name": "Subject", "value": "Hi " + id},
				}},
			})
			return
		}
		stubs := make([]any, len(ids))
		for i, id := range ids {
			stubs[i] = map[string]any{"id": id}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": stubs})
	}))
}

// First run with only_new baselines the watermark to the newest email and
// emits NOTHING — the flow starts watching from now, never replaying the
// existing mailbox (the burst-on-publish fix).
func TestGmailSearch_OnlyNew_FirstFireEmitsNothing(t *testing.T) {
	store := memCursor(t)
	srv := searchServer(t, []string{"a", "b"}, map[string]string{
		"a": "1700000000000", "b": "1700000005000", // b is newer
	})
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if _, ok := res.Output["messages"]; ok {
		t.Errorf("first fire should emit no messages port, got %+v", res.Output)
	}
	if got := store["acme|cursor.gmail_search.g1.n1"]; got != "1700000005000" {
		t.Errorf("cursor = %q, want the newest internalDate 1700000005000", got)
	}
}

func TestGmailSearch_OnlyNew_SubsequentFireEmitsOnlyNewer(t *testing.T) {
	store := memCursor(t)
	store["acme|cursor.gmail_search.g1.n1"] = "1700000005000" // as if a prior run baselined here
	srv := searchServer(t, []string{"a", "b", "c"}, map[string]string{
		"a": "1700000000000", // older — already seen
		"b": "1700000005000", // == cursor — already seen
		"c": "1700000009000", // newer — the only fresh one
	})
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	msgs := res.Output["messages"].Inline.([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["id"] != "c" {
		t.Fatalf("want only the newer email c, got %+v", msgs)
	}
	if got := store["acme|cursor.gmail_search.g1.n1"]; got != "1700000009000" {
		t.Errorf("cursor should advance to 1700000009000, got %q", got)
	}
}

func TestGmailSearch_OnlyNew_NothingNew(t *testing.T) {
	store := memCursor(t)
	store["acme|cursor.gmail_search.g1.n1"] = "1700000009000"
	srv := searchServer(t, []string{"a"}, map[string]string{"a": "1700000000000"})
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "x", "only_new": true},
	}, nil)
	if len(res.Output) != 0 {
		t.Errorf("nothing-new run should emit no ports, got %+v", res.Output)
	}
	if got := store["acme|cursor.gmail_search.g1.n1"]; got != "1700000009000" {
		t.Errorf("cursor should be unchanged, got %q", got)
	}
}

func TestGmailSearch_OnlyNewOff_ReturnsAll(t *testing.T) {
	store := memCursor(t)
	srv := searchServer(t, []string{"a", "b"}, map[string]string{
		"a": "1700000000000", "b": "1700000005000",
	})
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "x"}, // only_new defaults false
	}, nil)
	msgs := res.Output["messages"].Inline.([]any)
	if len(msgs) != 2 {
		t.Errorf("default mode should return all matches, got %+v", msgs)
	}
	if len(store) != 0 {
		t.Errorf("default mode must not touch the cursor store, got %+v", store)
	}
}

func TestGmailGetMessage_FlattensHeadersAndBody(t *testing.T) {
	bodyData := base64.RawURLEncoding.EncodeToString([]byte("Hello body"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages/m1") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m1", "threadId": "t1", "snippet": "snip",
			"payload": map[string]any{
				"headers": []any{
					map[string]any{"name": "From", "value": "a@x"},
					map[string]any{"name": "Subject", "value": "Hi"},
				},
				"mimeType": "text/plain",
				"body":     map[string]any{"data": bodyData},
			},
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{"id": "m1", "format": "full"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	msg := res.Output["message"].Inline.(map[string]any)
	headers := msg["headers"].(map[string]any)
	if headers["From"] != "a@x" || headers["Subject"] != "Hi" {
		t.Errorf("headers = %+v", headers)
	}
	if msg["body_text"] != "Hello body" {
		t.Errorf("body_text = %v", msg["body_text"])
	}
}

func TestGmailGetMessage_MissingID(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailGetMessage(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailGetMessage_TakesFirstOfWiredMatchList(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "m-first", "snippet": "s"})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{},
		Input: map[string]core.Ref{"id": {Inline: []any{
			map[string]any{"id": "m-first", "threadId": "t1"},
			map[string]any{"id": "m-second", "threadId": "t2"},
		}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(gotPath, "/messages/m-first") {
		t.Errorf("fetched %q, want the first match m-first", gotPath)
	}
}

func TestGmailGetMessage_EmptyWiredMatchList(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{},
		Input:  map[string]core.Ref{"id": {Inline: []any{}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_PlainText(t *testing.T) {
	var rawMsg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Raw string `json:"raw"`
		}
		_ = json.Unmarshal(b, &p)
		dec, _ := base64.RawURLEncoding.DecodeString(p.Raw)
		rawMsg = string(dec)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent1", "threadId": "th1"})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com", "subject": "Hello", "body": "the body"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(rawMsg, "To: x@y.com") || !strings.Contains(rawMsg, "Subject: Hello") || !strings.Contains(rawMsg, "the body") {
		t.Errorf("rfc822 = %q", rawMsg)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["id"] != "sent1" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestGmailSend_WithAttachment(t *testing.T) {
	var rawMsg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Raw string `json:"raw"`
		}
		_ = json.Unmarshal(b, &p)
		dec, _ := base64.RawURLEncoding.DecodeString(p.Raw)
		rawMsg = string(dec)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "s2"})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com", "body": "see attached"},
		Input: map[string]core.Ref{
			"attachments[0]": {MIME: "application/pdf", Ref: "scratch://report.pdf", Inline: []byte("%PDF-1.4 fake")},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(rawMsg, "multipart/mixed") || !strings.Contains(rawMsg, "filename=\"report.pdf\"") {
		t.Errorf("rfc822 missing attachment parts: %q", rawMsg)
	}
}

func TestGmailSend_MissingTo(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSend(context.Background(), core.Job{Params: map[string]any{"body": "x"}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_StructuredBodyRejected(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com"},
		Input:  map[string]core.Ref{"body": {Inline: []any{1, 2}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func b64url_Cov(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestResolveMessageID_Cov(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		input  map[string]core.Ref
		wantID string
		wantOK bool
	}{
		{
			name:   "no input falls back to param",
			params: map[string]any{"id": "p1"},
			wantID: "p1", wantOK: true,
		},
		{
			name:   "nil inline falls back to param",
			params: map[string]any{"id": "p2"},
			input:  map[string]core.Ref{"id": {Inline: nil}},
			wantID: "p2", wantOK: true,
		},
		{
			name:   "string input overrides param",
			params: map[string]any{"id": "p3"},
			input:  map[string]core.Ref{"id": {Inline: "wired"}},
			wantID: "wired", wantOK: true,
		},
		{
			name:   "empty string input falls back to param",
			params: map[string]any{"id": "p4"},
			input:  map[string]core.Ref{"id": {Inline: ""}},
			wantID: "p4", wantOK: true,
		},
		{
			name:   "non-empty []byte input",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: []byte("bytesid")}},
			wantID: "bytesid", wantOK: true,
		},
		{
			name:   "empty []byte falls back to param",
			params: map[string]any{"id": "p5"},
			input:  map[string]core.Ref{"id": {Inline: []byte{}}},
			wantID: "p5", wantOK: true,
		},
		{
			name:   "single stub map",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: map[string]any{"id": "stub1", "threadId": "t"}}},
			wantID: "stub1", wantOK: true,
		},
		{
			name:   "map without id is unusable",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: map[string]any{"threadId": "t"}}},
			wantID: "", wantOK: false,
		},
		{
			name:   "list takes first stub",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: []any{map[string]any{"id": "first"}, map[string]any{"id": "second"}}}},
			wantID: "first", wantOK: true,
		},
		{
			name:   "list of plain strings takes first",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: []any{"sid1", "sid2"}}},
			wantID: "sid1", wantOK: true,
		},
		{
			name:   "empty list falls back to param",
			params: map[string]any{"id": "pfallback"},
			input:  map[string]core.Ref{"id": {Inline: []any{}}},
			wantID: "pfallback", wantOK: true,
		},
		{
			name:   "list whose first element is unusable",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: []any{map[string]any{"threadId": "t"}}}},
			wantID: "", wantOK: false,
		},
		{
			name:   "list with empty-string first element is unusable",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: []any{""}}},
			wantID: "", wantOK: false,
		},
		{
			name:   "unsupported type is unusable",
			params: map[string]any{},
			input:  map[string]core.Ref{"id": {Inline: 42}},
			wantID: "", wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := resolveMessageID(core.Job{Params: tt.params, Input: tt.input})
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("resolveMessageID = (%q,%v), want (%q,%v)", id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestStripB64Pad_Cov(t *testing.T) {
	tests := []struct{ in, want string }{
		{"abc", "abc"},
		{"abc=", "abc"},
		{"abc==", "abc"},
		{"", ""},
		{"==", ""},
	}
	for _, tt := range tests {
		if got := stripB64Pad(tt.in); got != tt.want {
			t.Errorf("stripB64Pad(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStr_Cov(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{"hello", "hello"},
		{nil, ""},
		{42, "42"},
		{true, "true"},
		{3.5, "3.5"},
	}
	for _, tt := range tests {
		if got := str(tt.in); got != tt.want {
			t.Errorf("str(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExtractHeaders_Cov(t *testing.T) {
	payload := map[string]any{
		"headers": []any{
			map[string]any{"name": "From", "value": "a@x"},
			map[string]any{"name": "Subject", "value": "Hi"},
			map[string]any{"name": "", "value": "skip-empty-name"},
			"not-a-map",
			map[string]any{"value": "no-name"},
		},
	}
	got := extractHeaders(payload)
	if got["From"] != "a@x" || got["Subject"] != "Hi" {
		t.Errorf("headers = %+v", got)
	}
	if _, ok := got[""]; ok {
		t.Errorf("empty-name header should be skipped: %+v", got)
	}
	if len(got) != 2 {
		t.Errorf("want 2 headers, got %+v", got)
	}

	if h := extractHeaders(map[string]any{}); len(h) != 0 {
		t.Errorf("no headers => empty, got %+v", h)
	}
}

func TestFindTextPart_Cov(t *testing.T) {
	flat := map[string]any{
		"mimeType": "text/plain",
		"body":     map[string]any{"data": b64url_Cov("plain top")},
	}
	if got := findTextPart(flat, "text/plain"); got != "plain top" {
		t.Errorf("top-level = %q", got)
	}

	multi := map[string]any{
		"mimeType": "multipart/alternative",
		"parts": []any{
			map[string]any{
				"mimeType": "text/html",
				"body":     map[string]any{"data": b64url_Cov("<p>html</p>")},
			},
			map[string]any{
				"mimeType": "text/plain",
				"body":     map[string]any{"data": b64url_Cov("nested plain")},
			},
		},
	}
	if got := findTextPart(multi, "text/plain"); got != "nested plain" {
		t.Errorf("nested plain = %q", got)
	}
	if got := findTextPart(multi, "text/html"); got != "<p>html</p>" {
		t.Errorf("nested html = %q", got)
	}

	if got := findTextPart(multi, "application/pdf"); got != "" {
		t.Errorf("no match => empty, got %q", got)
	}

	emptyData := map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": ""}}
	if got := findTextPart(emptyData, "text/plain"); got != "" {
		t.Errorf("empty data => empty, got %q", got)
	}
	noBody := map[string]any{"mimeType": "text/plain", "body": "not-a-map"}
	if got := findTextPart(noBody, "text/plain"); got != "" {
		t.Errorf("non-map body => empty, got %q", got)
	}

	badB64 := map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": "!!!not base64!!!"}}
	if got := findTextPart(badB64, "text/plain"); got != "" {
		t.Errorf("bad base64 => empty, got %q", got)
	}

	padded := base64.URLEncoding.EncodeToString([]byte("padded text"))
	padPart := map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": padded}}
	if got := findTextPart(padPart, "text/plain"); got != "padded text" {
		t.Errorf("padded decode = %q", got)
	}
}

func TestFlatten_Cov(t *testing.T) {
	raw := map[string]any{
		"id":           "m1",
		"threadId":     "t1",
		"snippet":      "snip",
		"internalDate": "1700000000000",
		"labelIds":     []any{"INBOX", "UNREAD"},
		"payload": map[string]any{
			"headers": []any{
				map[string]any{"name": "Subject", "value": "Hi"},
			},
			"mimeType": "multipart/alternative",
			"parts": []any{
				map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": b64url_Cov("the text")}},
				map[string]any{"mimeType": "text/html", "body": map[string]any{"data": b64url_Cov("<b>the html</b>")}},
			},
		},
	}
	got := flatten(raw)
	if got["id"] != "m1" || got["threadId"] != "t1" || got["snippet"] != "snip" {
		t.Errorf("scalars = %+v", got)
	}
	if got["internal_date_ms"] != "1700000000000" {
		t.Errorf("internal_date_ms = %v", got["internal_date_ms"])
	}
	if labels, ok := got["labels"].([]any); !ok || len(labels) != 2 {
		t.Errorf("labels = %v", got["labels"])
	}
	if got["body_text"] != "the text" || got["body_html"] != "<b>the html</b>" {
		t.Errorf("bodies = %v / %v", got["body_text"], got["body_html"])
	}
	hdrs := got["headers"].(map[string]any)
	if hdrs["Subject"] != "Hi" {
		t.Errorf("headers = %+v", hdrs)
	}

	bare := flatten(map[string]any{"id": "x"})
	if _, ok := bare["headers"]; ok {
		t.Errorf("no payload => no headers, got %+v", bare)
	}
	if _, ok := bare["labels"]; ok {
		t.Errorf("no labelIds => no labels, got %+v", bare)
	}
}

func TestFriendlyMessage_Cov(t *testing.T) {
	m := friendlyMessage(map[string]any{
		"id":       "m1",
		"threadId": "t1",
		"headers": map[string]any{
			"from":    "a@x", // case-insensitive lookup
			"Subject": "Re: hi",
			"Date":    "Tue",
		},
		"body_text": "plain",
		"body_html": "html",
		"snippet":   "snip",
	})
	if m["from"] != "a@x" || m["subject"] != "Re: hi" || m["date"] != "Tue" {
		t.Errorf("headers = %+v", m)
	}
	if m["body"] != "plain" {
		t.Errorf("body should prefer text, got %v", m["body"])
	}

	m2 := friendlyMessage(map[string]any{"body_html": "the html", "snippet": "s"})
	if m2["body"] != "the html" {
		t.Errorf("html fallback = %v", m2["body"])
	}

	m3 := friendlyMessage(map[string]any{"snippet": "just snippet"})
	if m3["body"] != "just snippet" {
		t.Errorf("snippet fallback = %v", m3["body"])
	}

	// Oversized body is truncated with an ellipsis, on a rune boundary.
	long := strings.Repeat("a", 20001)
	m4 := friendlyMessage(map[string]any{"body_text": long})
	body := m4["body"].(string)
	if !strings.HasSuffix(body, "…") {
		t.Errorf("oversized body should end with ellipsis")
	}
	if len([]rune(body)) > 20001 {
		t.Errorf("body not truncated: %d runes", len([]rune(body)))
	}

	// Multi-byte boundary: a long body that would split a rune at the cut.
	mb := strings.Repeat("a", 19999) + "世界" + strings.Repeat("b", 10)
	m5 := friendlyMessage(map[string]any{"body_text": mb})
	if !strings.HasSuffix(m5["body"].(string), "…") {
		t.Errorf("multibyte body should truncate cleanly")
	}
}

func TestBaseURL_Cov(t *testing.T) {
	if got := baseURL(core.Job{Params: map[string]any{"base_url": "http://override"}}); got != "http://override" {
		t.Errorf("override = %q", got)
	}
	withGmailEnv(t, "http://from-env")
	if got := baseURL(core.Job{Params: map[string]any{}}); got != "http://from-env" {
		t.Errorf("default = %q", got)
	}
}

func TestExtractGmailError_Cov(t *testing.T) {
	msg := extractGmailError([]byte(`{"error":{"message":"bad token"}}`))
	if msg != "bad token" {
		t.Errorf("nested = %q", msg)
	}
	raw := extractGmailError([]byte(`plain text error`))
	if raw != "plain text error" {
		t.Errorf("raw = %q", raw)
	}
}

func TestBuildRFC822_Cov(t *testing.T) {
	msg := buildRFC822(rfcHeaders{
		to:              "to@x.com",
		cc:              "cc@x.com",
		bcc:             "bcc@x.com",
		replyTo:         "reply@x.com",
		subject:         "Hello",
		bodyContentType: `text/plain; charset="utf-8"`,
	}, "the body", nil)
	for _, want := range []string{
		"To: to@x.com", "Cc: cc@x.com", "Bcc: bcc@x.com",
		"Reply-To: reply@x.com", "Subject: Hello", "MIME-Version: 1.0",
		"Content-Type: text/plain", "the body",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("single-part missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "multipart/mixed") {
		t.Errorf("no attachments => not multipart")
	}

	inj := buildRFC822(rfcHeaders{
		to:              "to@x.com\r\nBcc: evil@x.com",
		subject:         "Hi",
		bodyContentType: `text/plain; charset="utf-8"`,
	}, "b", nil)
	// CR/LF are stripped so the injected "Bcc:" never starts its own header
	// line — it collapses onto the To line instead.
	if strings.Contains(inj, "\nBcc: evil@x.com") {
		t.Errorf("CRLF injection not stripped:\n%s", inj)
	}

	atts := []mailmsg.Attachment{
		{Filename: "report.pdf", MIME: "application/pdf", Data: []byte("%PDF fake")},
	}
	multi := buildRFC822(rfcHeaders{
		to:              "to@x.com",
		subject:         "With file",
		bodyContentType: `text/html; charset="utf-8"`,
	}, "<p>see attached</p>", atts)
	if !strings.Contains(multi, "multipart/mixed") {
		t.Errorf("attachments => multipart/mixed:\n%s", multi)
	}
	if !strings.Contains(multi, "report.pdf") {
		t.Errorf("attachment filename missing:\n%s", multi)
	}
	if !strings.Contains(multi, "<p>see attached</p>") {
		t.Errorf("body part missing:\n%s", multi)
	}
}

func TestGmailGetMessage_BadInput_Cov(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{},
		Input:  map[string]core.Ref{"id": {Inline: map[string]any{"threadId": "no-id"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailGetMessage_Non2xx_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	res, _ := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{"id": "missing"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "gmail_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
	if !strings.Contains(res.Error.Message, "not found") {
		t.Errorf("message = %q", res.Error.Message)
	}
}

func TestGmailGetMessage_BadJSON_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	res, _ := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{"id": "m1"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "gmail_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailGetMessage_AuthError_Cov(t *testing.T) {
	SetHTTPBase("http://unused")
	SetTokenLookup(nil) // no resolver => auth failure
	t.Cleanup(func() {
		SetHTTPBase("https://gmail.googleapis.com/gmail/v1")
		SetTokenLookup(nil)
	})
	res, _ := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{"id": "m1", "account": "default"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailGetMessage_BodyFallsBackToSnippet_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m1", "snippet": "the snippet",
			"payload": map[string]any{
				"headers":  []any{map[string]any{"name": "From", "value": "a@x"}},
				"mimeType": "multipart/mixed",
			},
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	res, _ := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{"id": "m1"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["body"].Inline != "the snippet" {
		t.Errorf("body = %v, want snippet fallback", res.Output["body"].Inline)
	}
}

func TestGmailSearch_AuthError_Cov(t *testing.T) {
	SetHTTPBase("http://unused")
	SetTokenLookup(nil)
	t.Cleanup(func() {
		SetHTTPBase("https://gmail.googleapis.com/gmail/v1")
		SetTokenLookup(nil)
	})
	res, _ := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{"account": "default"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSearch_BadQueryInput_Cov(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{},
		Input:  map[string]core.Ref{"query": {Inline: []any{1, 2}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSearch_Non2xx_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	res, _ := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{"query": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "gmail_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSearch_PageTokenAndEmpty_Cov(t *testing.T) {
	var gotQuery, gotPageToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotPageToken = r.URL.Query().Get("pageToken")
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{"page_token": "ptok"},
		Input:  map[string]core.Ref{"query": {Inline: "wired-query"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotQuery != "wired-query" {
		t.Errorf("query input should override param, got q=%q", gotQuery)
	}
	if gotPageToken != "ptok" {
		t.Errorf("pageToken = %q", gotPageToken)
	}
	msgs := res.Output["messages"].Inline.([]any)
	if len(msgs) != 0 {
		t.Errorf("want empty messages, got %+v", msgs)
	}
}

func TestGmailSearch_ExpansionFailureDegradesToStub_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []any{
				map[string]any{"id": "a", "threadId": "ta"},
				map[string]any{}, // no id => stays a stub, not fetched
			},
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{"query": "x"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	msgs := res.Output["messages"].Inline.([]any)
	if len(msgs) != 2 {
		t.Fatalf("want 2 entries, got %+v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["id"] != "a" || first["threadId"] != "ta" {
		t.Errorf("failed expansion should degrade to stub, got %+v", first)
	}
}

func TestGmailSend_AuthError_Cov(t *testing.T) {
	SetHTTPBase("http://unused")
	SetTokenLookup(nil)
	t.Cleanup(func() {
		SetHTTPBase("https://gmail.googleapis.com/gmail/v1")
		SetTokenLookup(nil)
	})
	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com", "account": "default"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_BadToInput_Cov(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{},
		Input:  map[string]core.Ref{"to": {Inline: []any{1}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_BadSubjectInput_Cov(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com"},
		Input:  map[string]core.Ref{"subject": {Inline: map[string]any{"k": "v"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_Non2xx_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid recipient"}}`))
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com", "body": "b", "format": "text"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "gmail_error" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_TextWithCCBCCThread_Cov(t *testing.T) {
	var rawMsg, threadID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Raw      string `json:"raw"`
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(b, &p)
		dec, _ := base64.RawURLEncoding.DecodeString(p.Raw)
		rawMsg = string(dec)
		threadID = p.ThreadID
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent", "threadId": "th"})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{
			"to": "to@x.com", "cc": "cc@x.com", "bcc": "bcc@x.com",
			"reply_to": "r@x.com", "subject": "S", "body": "plain body",
			"format": "text", "thread_id": "thread-99",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	for _, want := range []string{"To: to@x.com", "Cc: cc@x.com", "Bcc: bcc@x.com", "Reply-To: r@x.com", "text/plain", "plain body"} {
		if !strings.Contains(rawMsg, want) {
			t.Errorf("raw missing %q:\n%s", want, rawMsg)
		}
	}
	if threadID != "thread-99" {
		t.Errorf("threadId = %q", threadID)
	}
}

// The read-failure case, at the highest stakes in the product: a mailbox
// watcher whose watermark cannot be read must NOT conclude "first run".
// Doing so re-baselines to the newest message present and marks every email
// that arrived since the last poll as handled — an invoice-filing or
// auto-reply flow silently skips them all, and the run reports success.
func TestGmailSearch_OnlyNew_ReadFailureStopsAndKeepsTheWatermark(t *testing.T) {
	const stored = "1700000000000"
	name := "cursor.gmail_search.g1.n1"
	backing := map[string]string{"acme|" + name: stored}
	failRead := true
	cursor.SetStore(
		func(_ context.Context, tenant, key string) (string, error) {
			if failRead {
				return "", errors.New("secret store unavailable")
			}
			return backing[tenant+"|"+key], nil
		},
		func(_ context.Context, tenant, key, value string) error {
			backing[tenant+"|"+key] = value
			return nil
		},
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })

	// Two emails newer than the stored watermark: exactly the mail that would
	// have been lost.
	srv := searchServer(t, []string{"a", "b"}, map[string]string{
		"a": "1700000009000", "b": "1700000005000",
	})
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	job := core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}
	res, err := executeGmailSearch(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error when the watermark can't be read", res.Status)
	}
	if res.Error == nil || res.Error.Code != "cursor_unavailable" {
		t.Errorf("error = %+v, want cursor_unavailable", res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("failed poll emitted %d port(s); downstream must not run", len(res.Output))
	}
	if got := backing["acme|"+name]; got != stored {
		t.Fatalf("watermark was overwritten: %q, want it left at %q", got, stored)
	}

	failRead = false
	res, err = executeGmailSearch(context.Background(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("recovery status=%q err=%+v", res.Status, res.Error)
	}
	msgs, ok := res.Output["messages"]
	if !ok {
		t.Fatal("after recovery the emails were not emitted")
	}
	list, _ := msgs.Inline.([]any)
	if len(list) != 2 {
		t.Errorf("emitted %d email(s) after recovery, want 2", len(list))
	}
}

func hydrationServer(t *testing.T, ids []string, dateByID map[string]string, failIDs map[string]bool, gets *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			atomic.AddInt32(gets, 1)
			if failIDs[id] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "threadId": "t-" + id, "internalDate": dateByID[id],
				"payload": map[string]any{"headers": []any{
					map[string]any{"name": "Subject", "value": "Hi " + id},
				}},
			})
			return
		}
		stubs := make([]any, len(ids))
		for i, id := range ids {
			stubs[i] = map[string]any{"id": id}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": stubs})
	}))
}

// The silent-loss case: one email's expansion fails while a NEWER one in the
// same page succeeds. The failed email has no date, so it can't be emitted —
// and if the watermark advances to the newer email's date, it is newer than
// the failed one and that email is never offered again. One unlucky API call,
// one invoice never filed, on a run that reported success.
//
// The watermark must be held instead, so the next run picks it up.
func TestGmailSearch_OnlyNew_UnfetchableEmailHoldsTheWatermark(t *testing.T) {
	const startedAt = "1700000000000"
	name := "cursor.gmail_search.g1.n1"
	backing := map[string]string{"acme|" + name: startedAt}
	cursor.SetStore(
		func(_ context.Context, tenant, key string) (string, error) { return backing[tenant+"|"+key], nil },
		func(_ context.Context, tenant, key, value string) error {
			backing[tenant+"|"+key] = value
			return nil
		},
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })

	dates := map[string]string{"lost": "1700000005000", "newer": "1700000009000"}
	var gets int32
	failing := map[string]bool{"lost": true}
	srv := hydrationServer(t, []string{"newer", "lost"}, dates, failing, &gets)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	job := core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}
	res, err := executeGmailSearch(context.Background(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	list, _ := res.Output["messages"].Inline.([]any)
	if len(list) != 1 {
		t.Fatalf("emitted %d email(s), want the 1 that fetched", len(list))
	}
	if n := atomic.LoadInt32(&gets); n != 2 {
		t.Errorf("%d expansion call(s), want 2 (one per message, no in-step retry)", n)
	}
	// The watermark must NOT have moved past the email we couldn't read.
	if got := backing["acme|"+name]; got != startedAt {
		t.Fatalf("watermark advanced to %q past an unreadable email; it must stay at %q", got, startedAt)
	}

	srv2 := hydrationServer(t, []string{"newer", "lost"}, dates, map[string]bool{}, &gets)
	defer srv2.Close()
	withGmailEnv(t, srv2.URL)
	res, err = executeGmailSearch(context.Background(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("recovery status=%q err=%+v", res.Status, res.Error)
	}
	list, _ = res.Output["messages"].Inline.([]any)
	if len(list) != 2 {
		t.Fatalf("recovery emitted %d email(s), want both", len(list))
	}
	if got := backing["acme|"+name]; got != dates["newer"] {
		t.Errorf("watermark = %q, want it advanced to %q once everything read", got, dates["newer"])
	}
}

func TestGmailSearch_OnlyNew_AllUnfetchableFails(t *testing.T) {
	store := memCursor(t)
	store["acme|cursor.gmail_search.g1.n1"] = "1700000000000"
	var gets int32
	srv := hydrationServer(t, []string{"a", "b"},
		map[string]string{"a": "1700000005000", "b": "1700000009000"},
		map[string]bool{"a": true, "b": true}, &gets)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error when nothing could be fetched", res.Status)
	}
	if res.Error == nil || res.Error.Code != "gmail_unresolved" {
		t.Errorf("error = %+v, want gmail_unresolved", res.Error)
	}
	if got := store["acme|cursor.gmail_search.g1.n1"]; got != "1700000000000" {
		t.Errorf("watermark moved to %q during an outage", got)
	}
}

// A baseline is the exception: it emits nothing by design, so an email that
// couldn't be read loses nothing by being baselined over — and holding would
// leave the watermark unwritten, so the flow would baseline for ever and never
// emit anything (the trap cursor.FailBaseline exists for).
func TestGmailSearch_OnlyNew_UnfetchableOnFirstRunStillBaselines(t *testing.T) {
	store := memCursor(t)
	var gets int32
	srv := hydrationServer(t, []string{"newer", "lost"},
		map[string]string{"newer": "1700000009000", "lost": "1700000005000"},
		map[string]bool{"lost": true}, &gets)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if _, ok := res.Output["messages"]; ok {
		t.Error("a baseline run emitted messages")
	}
	if got := store["acme|cursor.gmail_search.g1.n1"]; got != "1700000009000" {
		t.Fatalf("baseline = %q, want it recorded at the newest readable email", got)
	}
}

// backlogServer emulates the two things about messages.list that made #5 a
// silent loss: it returns matches NEWEST FIRST, and it honours `after:<epoch>`
// so a poll can ask for just the backlog. maxResults caps each page and a
// nextPageToken is issued when more remain.
//
// ids must be newest-first; dateByID gives each one an internalDate in ms.
func backlogServer(t *testing.T, ids []string, dateByID map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": id, "threadId": "t-" + id, "internalDate": dateByID[id],
				"payload": map[string]any{"headers": []any{
					map[string]any{"name": "Subject", "value": id},
				}},
			})
			return
		}
		// Apply after:<epoch seconds> from the query, as Gmail would.
		match := ids
		if q := r.URL.Query().Get("q"); strings.Contains(q, "after:") {
			after := q[strings.Index(q, "after:")+len("after:"):]
			if sp := strings.IndexByte(after, ' '); sp >= 0 {
				after = after[:sp]
			}
			secs, err := strconv.ParseInt(after, 10, 64)
			if err != nil {
				t.Fatalf("unparseable after: %q", after)
			}
			match = nil
			for _, id := range ids {
				ms, _ := strconv.ParseInt(dateByID[id], 10, 64)
				if ms/1000 >= secs {
					match = append(match, id)
				}
			}
		}
		start := 0
		if pt := r.URL.Query().Get("pageToken"); pt != "" {
			n, err := strconv.Atoi(pt)
			if err != nil {
				t.Fatalf("unparseable pageToken: %q", pt)
			}
			start = n
		}
		size := 500
		if mr := r.URL.Query().Get("maxResults"); mr != "" {
			if n, err := strconv.Atoi(mr); err == nil && n > 0 {
				size = n
			}
		}
		end := min(start+size, len(match))
		out := map[string]any{}
		stubs := make([]any, 0, end-start)
		for _, id := range match[start:end] {
			stubs = append(stubs, map[string]any{"id": id})
		}
		out["messages"] = stubs
		if end < len(match) {
			out["nextPageToken"] = strconv.Itoa(end)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

// #5, the silent one: more email arrived than max_results. messages.list hands
// back the NEWEST max_results, so emitting those and advancing the watermark to
// the newest of them puts everything older permanently behind it — gone, on a
// green run. The poll must drain from the OLDEST end instead, so a burst is
// delayed across polls rather than truncated.
//
// The assertion is the invariant rather than per-poll batch sizes: polling until
// it goes quiet must yield every email exactly once, in arrival order, and must
// terminate. Batch sizes are not exactly max_results every time, because the
// second-granular `after:` bound brings the boundary email back.
func TestGmailSearch_OnlyNew_BacklogDrainsOldestFirst(t *testing.T) {
	store := memCursor(t)
	name := "acme|cursor.gmail_search.g1.n1"
	store[name] = "1700000000000" // watermark: everything below is handled

	ids := []string{"e5", "e4", "e3", "e2", "e1"}
	dates := map[string]string{
		"e1": "1700000001000", "e2": "1700000002000", "e3": "1700000003000",
		"e4": "1700000004000", "e5": "1700000005000",
	}
	srv := backlogServer(t, ids, dates)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	job := core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true, "max_results": 2},
	}
	subjects := func(res core.Result) []string {
		list, _ := res.Output["messages"].Inline.([]any)
		out := make([]string, 0, len(list))
		for _, m := range list {
			rec, _ := m.(map[string]any)
			out = append(out, str(rec["subject"]))
		}
		return out
	}

	var drained []string
	const maxPolls = 8 // a stall or a duplicate loop trips this
	for poll := 1; poll <= maxPolls; poll++ {
		res, err := executeGmailSearch(context.Background(), job, nil)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("poll %d: status=%q err=%+v", poll, res.Status, res.Error)
		}
		got := subjects(res)
		if len(got) == 0 {
			break
		}
		if len(got) > 2 {
			t.Fatalf("poll %d emitted %d emails, over the cap of 2: %v", poll, len(got), got)
		}
		drained = append(drained, got...)
		if poll == maxPolls {
			t.Fatalf("still emitting after %d polls: %v", maxPolls, drained)
		}
	}

	want := []string{"e1", "e2", "e3", "e4", "e5"}
	if len(drained) != len(want) {
		t.Fatalf("drained %v, want %v", drained, want)
	}
	for i := range want {
		if drained[i] != want[i] {
			t.Fatalf("drained %v, want %v (arrival order)", drained, want)
		}
	}
	if got := store[name]; got != dates["e5"] {
		t.Errorf("final watermark = %q, want e5's %q", got, dates["e5"])
	}
}

func TestGmailSearch_OnlyNew_QueryCarriesTheWatermark(t *testing.T) {
	store := memCursor(t)
	store["acme|cursor.gmail_search.g1.n1"] = "1700000000000"

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "internalDate": "1700000009000"})
			return
		}
		gotQuery = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []any{}})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	_, err := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotQuery != "is:unread after:1700000000" {
		t.Errorf("query = %q, want the search ANDed with the watermark bound", gotQuery)
	}
}

// A backlog deeper than the step will scan must not be part-drained: the scan
// runs newest-first, so its oldest end — where a drain has to start — is
// exactly what is missing. Refusing keeps the watermark untouched, so nothing
// is skipped and the whole backlog is still waiting.
func TestGmailSearch_OnlyNew_BacklogTooDeepRefuses(t *testing.T) {
	store := memCursor(t)
	name := "acme|cursor.gmail_search.g1.n1"
	store[name] = "1700000000000"

	total := backlogPageSize*maxBacklogPages + 10
	ids := make([]string, 0, total)
	dates := map[string]string{}
	for i := total; i >= 1; i-- { // newest first
		id := "e" + strconv.Itoa(i)
		ids = append(ids, id)
		dates[id] = strconv.FormatInt(1700000000000+int64(i)*1000, 10)
	}
	srv := backlogServer(t, ids, dates)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Tenant: "acme", GraphID: "g1", NodeID: "n1",
		Params: map[string]any{"query": "is:unread", "only_new": true, "max_results": 10},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error on a backlog too deep to drain safely", res.Status)
	}
	if res.Error == nil || res.Error.Code != "gmail_backlog_too_deep" {
		t.Errorf("error = %+v, want gmail_backlog_too_deep", res.Error)
	}
	if got := store[name]; got != "1700000000000" {
		t.Errorf("watermark moved to %q; a refusal must leave the backlog intact", got)
	}
}
