// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/mailmsg"
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
		// Per-message expansion fetch (format=full) — return a real message.
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

// Wiring Search emails' "Matching emails" list straight into Message ID
// reads the FIRST match — the obvious drag just works.
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

// An EMPTY wired match list (search found nothing) falls back to the param /
// the clear "required" error — not a confusing bad-input failure.
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

// b64 encodes s as Gmail's unpadded base64url.
func b64url_Cov(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

// --- resolveMessageID: wired input vs param across every shape ---

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

// --- helpers: flatten / extractHeaders / findTextPart / stripB64Pad / str ---

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

	// Missing/garbage headers field yields an empty map, not a panic.
	if h := extractHeaders(map[string]any{}); len(h) != 0 {
		t.Errorf("no headers => empty, got %+v", h)
	}
}

func TestFindTextPart_Cov(t *testing.T) {
	// Top-level part matches directly.
	flat := map[string]any{
		"mimeType": "text/plain",
		"body":     map[string]any{"data": b64url_Cov("plain top")},
	}
	if got := findTextPart(flat, "text/plain"); got != "plain top" {
		t.Errorf("top-level = %q", got)
	}

	// Nested multipart: text/plain lives under parts.
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

	// No match at all.
	if got := findTextPart(multi, "application/pdf"); got != "" {
		t.Errorf("no match => empty, got %q", got)
	}

	// Matching mime but empty data, and matching mime but body not a map.
	emptyData := map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": ""}}
	if got := findTextPart(emptyData, "text/plain"); got != "" {
		t.Errorf("empty data => empty, got %q", got)
	}
	noBody := map[string]any{"mimeType": "text/plain", "body": "not-a-map"}
	if got := findTextPart(noBody, "text/plain"); got != "" {
		t.Errorf("non-map body => empty, got %q", got)
	}

	// Matching mime but invalid base64 yields empty (decode error path).
	badB64 := map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": "!!!not base64!!!"}}
	if got := findTextPart(badB64, "text/plain"); got != "" {
		t.Errorf("bad base64 => empty, got %q", got)
	}

	// Padded base64url decodes via stripB64Pad.
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

	// Raw without payload and without labels: those keys are simply absent.
	bare := flatten(map[string]any{"id": "x"})
	if _, ok := bare["headers"]; ok {
		t.Errorf("no payload => no headers, got %+v", bare)
	}
	if _, ok := bare["labels"]; ok {
		t.Errorf("no labelIds => no labels, got %+v", bare)
	}
}

// --- friendlyMessage: header lookup, body fallbacks, UTF-8-safe truncation ---

func TestFriendlyMessage_Cov(t *testing.T) {
	// body_text wins.
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

	// Falls back to html when text empty.
	m2 := friendlyMessage(map[string]any{"body_html": "the html", "snippet": "s"})
	if m2["body"] != "the html" {
		t.Errorf("html fallback = %v", m2["body"])
	}

	// Falls back to snippet when both empty.
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

// --- baseURL: param override vs package default ---

func TestBaseURL_Cov(t *testing.T) {
	if got := baseURL(core.Job{Params: map[string]any{"base_url": "http://override"}}); got != "http://override" {
		t.Errorf("override = %q", got)
	}
	withGmailEnv(t, "http://from-env")
	if got := baseURL(core.Job{Params: map[string]any{}}); got != "http://from-env" {
		t.Errorf("default = %q", got)
	}
}

// --- extractGmailError: nested error.message vs raw body ---

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

// --- buildRFC822: MIME assembly (headers, CC/BCC/Reply-To, attachments) ---

func TestBuildRFC822_Cov(t *testing.T) {
	// No attachments: single-part, includes all headers.
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

	// Header injection: CR/LF in values is stripped.
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

	// With attachments: multipart/mixed with the body part and a file part.
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

// --- executeGmailGetMessage: error paths ---

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

// Body falls back to snippet when no text/html parts decode.
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

// --- executeGmailSearch: error and edge paths ---

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

// Search sets page token and query input override; empty results degrade safely.
func TestGmailSearch_PageTokenAndEmpty_Cov(t *testing.T) {
	var gotQuery, gotPageToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotPageToken = r.URL.Query().Get("pageToken")
		// No "messages" key at all => parsed.Messages stays nil => [] out.
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

// A per-message expansion failure degrades that entry to its stub instead of
// failing the whole search.
func TestGmailSearch_ExpansionFailureDegradesToStub_Cov(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/messages/") {
			// Every expansion fetch errors.
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

// --- executeGmailSend: error paths and CC/BCC assembly ---

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

// Text format plus CC/BCC/Reply-To/thread_id flow through into the raw message
// and the send payload.
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
