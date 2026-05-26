package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeGmail stubs the three endpoints we hit. Per-endpoint
// recorded fields + configurable responses, same pattern as
// fakeSlack / fakeProvider.
type fakeGmail struct {
	server *httptest.Server

	mu sync.Mutex

	// send
	lastSendBody []byte
	lastSendAuth string
	sendResp     string

	// list
	lastListQ    string
	lastListAuth string
	listResp     string

	// get
	lastGetPath string
	lastGetAuth string
	getResp     string
}

func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	f := &fakeGmail{
		sendResp: `{"id":"msg-abc","threadId":"thr-xyz"}`,
		listResp: `{"messages":[{"id":"m1","threadId":"t1"},{"id":"m2","threadId":"t2"}],"resultSizeEstimate":2}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/messages/send", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastSendBody = body
		f.lastSendAuth = r.Header.Get("Authorization")
		resp := f.sendResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	})
	// Exact-match handler for the list endpoint. Go's ServeMux picks
	// the longest matching pattern, so /users/me/messages (exact)
	// wins over /users/me/messages/ (prefix) for that exact path.
	mux.HandleFunc("/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastListQ = r.URL.RawQuery
		f.lastListAuth = r.Header.Get("Authorization")
		resp := f.listResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	})
	// Prefix-match handler for /users/me/messages/{id}. /send is
	// registered ABOVE with a more-specific pattern, so it doesn't
	// fall through here.
	mux.HandleFunc("/users/me/messages/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastGetPath = r.URL.Path
		f.lastGetAuth = r.Header.Get("Authorization")
		resp := f.getResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	prev := currentHTTPBase()
	SetHTTPBase(f.server.URL)
	t.Cleanup(func() { SetHTTPBase(prev) })
	return f
}

func installTokenLookup(t *testing.T, fn TokenLookup) {
	t.Helper()
	tokenLookupMu.RLock()
	prev := tokenLookup
	tokenLookupMu.RUnlock()
	SetTokenLookup(fn)
	t.Cleanup(func() { SetTokenLookup(prev) })
}

// ===== gmail_send_email =============================================

func TestGmailSendEmail_BasicTextFromParams(t *testing.T) {
	fg := newFakeGmail(t)
	res, err := executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "ya29.test-token",
			"to":      "alice@example.com",
			"subject": "Hello",
			"body":    "Pipeline done.",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}

	// Decode the wire request: it's JSON {raw: base64-URL-no-pad}.
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	if err := json.Unmarshal(fg.lastSendBody, &sent); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, fg.lastSendBody)
	}
	raw, _ := sent["raw"].(string)
	decoded, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(raw)
	if err != nil {
		t.Fatalf("raw not base64-URL-no-pad: %v", err)
	}
	rfc822 := string(decoded)
	if !strings.Contains(rfc822, "To: alice@example.com\r\n") {
		t.Errorf("missing To header: %q", rfc822)
	}
	if !strings.Contains(rfc822, "Subject: Hello\r\n") {
		t.Errorf("missing Subject: %q", rfc822)
	}
	if !strings.Contains(rfc822, "\r\n\r\nPipeline done.") {
		t.Errorf("body not separated by blank line: %q", rfc822)
	}
	if !strings.Contains(rfc822, "Content-Type: text/plain") {
		t.Errorf("expected text/plain by default: %q", rfc822)
	}
	if fg.lastSendAuth != "Bearer ya29.test-token" {
		t.Errorf("auth = %q", fg.lastSendAuth)
	}

	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["id"] != "msg-abc" || meta["threadId"] != "thr-xyz" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestGmailSendEmail_HTMLFormat(t *testing.T) {
	fg := newFakeGmail(t)
	_, _ = executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{
			"token":  "x", "to": "a@b", "format": "html",
			"body": "<b>hi</b>",
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastSendBody, &sent)
	raw, _ := sent["raw"].(string)
	decoded, _ := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(raw)
	if !strings.Contains(string(decoded), "Content-Type: text/html") {
		t.Errorf("expected text/html: %s", decoded)
	}
}

func TestGmailSendEmail_BodyPortWinsOverParams(t *testing.T) {
	fg := newFakeGmail(t)
	_, _ = executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "to": "a@b", "subject": "s",
			"body": "from-params",
		},
		Input: map[string]core.Ref{
			"body": {Inline: "from-port"},
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastSendBody, &sent)
	raw, _ := sent["raw"].(string)
	decoded, _ := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(raw)
	if !strings.Contains(string(decoded), "from-port") {
		t.Errorf("body port should win, got: %s", decoded)
	}
}

func TestGmailSendEmail_HeaderInjectionDefense(t *testing.T) {
	// A user-supplied "To" value containing CRLF must NOT inject
	// extra headers (Bcc: leak@evil.com would be the classic
	// attack). The header builder strips CR/LF from values.
	fg := newFakeGmail(t)
	_, _ = executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x",
			"to":    "alice@example.com\r\nBcc: leak@evil.com",
			"body":  "x",
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastSendBody, &sent)
	raw, _ := sent["raw"].(string)
	decoded, _ := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(raw)
	// The injection succeeds only if "Bcc: leak@evil.com" appears as
	// its own header LINE (preceded by \r\n). Appearing as substring
	// inside a mangled To value is fine — that's just literal text
	// the MTA will reject, not an injected header.
	if strings.Contains(string(decoded), "\r\nBcc: leak@evil.com") {
		t.Errorf("header injection succeeded! rfc822 = %q", decoded)
	}
}

func TestGmailSendEmail_ThreadID(t *testing.T) {
	fg := newFakeGmail(t)
	_, _ = executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x", "to": "a@b", "body": "reply",
			"thread_id": "thr-original",
		},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	var sent map[string]any
	_ = json.Unmarshal(fg.lastSendBody, &sent)
	if sent["threadId"] != "thr-original" {
		t.Errorf("threadId = %v", sent["threadId"])
	}
}

func TestGmailSendEmail_MissingTo(t *testing.T) {
	_ = newFakeGmail(t)
	res, _ := executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "body": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestGmailSendEmail_MissingBody(t *testing.T) {
	_ = newFakeGmail(t)
	res, _ := executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "to": "a@b"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q, want bad_input", res.Status, res.Error.Code)
	}
}

func TestGmailSendEmail_OAuthLookupUsed(t *testing.T) {
	fg := newFakeGmail(t)
	var sawAccount string
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		sawAccount = account
		return "ya29.from-oauth", nil
	})
	res, _ := executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{
			"account": "personal",
			"to":      "a@b", "body": "x",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if sawAccount != "personal" {
		t.Errorf("lookup got account=%q", sawAccount)
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if fg.lastSendAuth != "Bearer ya29.from-oauth" {
		t.Errorf("auth header = %q", fg.lastSendAuth)
	}
}

func TestGmailSendEmail_NoTokenNoLookupIsAuthError(t *testing.T) {
	_ = newFakeGmail(t)
	installTokenLookup(t, nil)
	res, _ := executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{"to": "a@b", "body": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%q, want auth", res.Status, res.Error.Code)
	}
}

func TestGmailSendEmail_NonSuccessSurfacesGmailError(t *testing.T) {
	// Google returns {error:{code, message}} on failure. The drop
	// must extract message into the user-facing error.
	fg := newFakeGmail(t)
	// Reconfigure send to fail.
	fg.server.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/messages/send", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"Insufficient Permission","status":"PERMISSION_DENIED"}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	SetHTTPBase(srv.URL)

	res, _ := executeGmailSendEmail(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "to": "a@b", "body": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "gmail_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "Insufficient Permission") {
		t.Errorf("error missing Google message: %q", res.Error.Message)
	}
}

// ===== gmail_search_messages ========================================

func TestGmailSearchMessages_Basic(t *testing.T) {
	fg := newFakeGmail(t)
	res, err := executeGmailSearchMessages(t.Context(), core.Job{
		Params: map[string]any{
			"token": "x",
			"query": "is:unread newer_than:5m",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	msgs := res.Output["messages"].Inline.([]any)
	if len(msgs) != 2 {
		t.Errorf("got %d messages, want 2", len(msgs))
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if !strings.Contains(fg.lastListQ, "q=is%3Aunread+newer_than%3A5m") {
		t.Errorf("query not URL-encoded as expected: %q", fg.lastListQ)
	}
	if !strings.Contains(fg.lastListQ, "maxResults=50") {
		t.Errorf("default maxResults missing: %q", fg.lastListQ)
	}
}

func TestGmailSearchMessages_Pagination(t *testing.T) {
	fg := newFakeGmail(t)
	fg.listResp = `{"messages":[{"id":"m1"}],"nextPageToken":"npt-XYZ","resultSizeEstimate":100}`
	res, _ := executeGmailSearchMessages(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "page_token": "abc"},
	}, nil)
	if pt, _ := res.Output["next_page_token"].Inline.(string); pt != "npt-XYZ" {
		t.Errorf("next_page_token = %v", res.Output["next_page_token"].Inline)
	}
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if !strings.Contains(fg.lastListQ, "pageToken=abc") {
		t.Errorf("page_token not forwarded: %q", fg.lastListQ)
	}
}

func TestGmailSearchMessages_EmptyResultStillEmitsArray(t *testing.T) {
	// Gmail omits the `messages` field when there are zero hits.
	// Downstream consumers (for_each, map_rows) need an empty
	// array, not nil.
	fg := newFakeGmail(t)
	fg.listResp = `{"resultSizeEstimate":0}`
	res, _ := executeGmailSearchMessages(t.Context(), core.Job{
		Params: map[string]any{"token": "x"},
	}, nil)
	msgs, ok := res.Output["messages"].Inline.([]any)
	if !ok {
		t.Fatalf("messages = %T, want []any", res.Output["messages"].Inline)
	}
	if len(msgs) != 0 {
		t.Errorf("len(messages) = %d, want 0", len(msgs))
	}
}

// ===== gmail_get_message ============================================

const sampleGmailGetResponse = `{
  "id": "msg-1",
  "threadId": "thr-1",
  "snippet": "Hello there",
  "labelIds": ["INBOX","UNREAD"],
  "internalDate": "1717000000000",
  "payload": {
    "mimeType": "multipart/alternative",
    "headers": [
      {"name":"From","value":"alice@example.com"},
      {"name":"To","value":"bob@example.com"},
      {"name":"Subject","value":"Hi"},
      {"name":"Date","value":"Mon, 26 May 2026 12:00:00 +0000"}
    ],
    "parts": [
      {"mimeType":"text/plain","body":{"data":"SGVsbG8gd29ybGQ"}},
      {"mimeType":"text/html","body":{"data":"PGI-SGVsbG88L2I-"}}
    ]
  }
}`

func TestGmailGetMessage_FlattensTree(t *testing.T) {
	fg := newFakeGmail(t)
	fg.getResp = sampleGmailGetResponse
	res, err := executeGmailGetMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "id": "msg-1"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	msg := res.Output["message"].Inline.(map[string]any)
	if msg["id"] != "msg-1" || msg["snippet"] != "Hello there" {
		t.Errorf("missing top-level fields: %+v", msg)
	}
	headers := msg["headers"].(map[string]string)
	if headers["From"] != "alice@example.com" || headers["Subject"] != "Hi" {
		t.Errorf("headers wrong: %+v", headers)
	}
	if msg["body_text"] != "Hello world" {
		t.Errorf("body_text = %q, want 'Hello world' (base64-URL decoded)", msg["body_text"])
	}
	if msg["body_html"] != "<b>Hello</b>" {
		t.Errorf("body_html = %q", msg["body_html"])
	}
	labels := msg["labels"].([]any)
	if len(labels) != 2 || labels[0] != "INBOX" {
		t.Errorf("labels = %v", labels)
	}
	// raw passthrough so a custom downstream drop can read everything.
	if _, ok := msg["raw"].(map[string]any); !ok {
		t.Errorf("raw missing")
	}
}

func TestGmailGetMessage_PathIncludesID(t *testing.T) {
	fg := newFakeGmail(t)
	fg.getResp = `{"id":"abc-123","threadId":"t"}`
	_, _ = executeGmailGetMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "id": "abc-123"},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if !strings.HasSuffix(fg.lastGetPath, "/abc-123") {
		t.Errorf("path = %q, want suffix /abc-123", fg.lastGetPath)
	}
}

func TestGmailGetMessage_MissingID(t *testing.T) {
	_ = newFakeGmail(t)
	res, _ := executeGmailGetMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestGmailGetMessage_MinimalPayload(t *testing.T) {
	// metadata-only response (no body parts) → no body_text /
	// body_html keys, but headers + id still flat.
	fg := newFakeGmail(t)
	fg.getResp = `{"id":"m","threadId":"t","payload":{"headers":[{"name":"Subject","value":"X"}]}}`
	res, _ := executeGmailGetMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "x", "id": "m"},
	}, nil)
	msg := res.Output["message"].Inline.(map[string]any)
	if _, ok := msg["body_text"]; ok {
		t.Errorf("body_text should be absent when no parts: %+v", msg)
	}
	if msg["headers"].(map[string]string)["Subject"] != "X" {
		t.Errorf("headers missing")
	}
}

func TestGmailGetMessage_OAuthLookupUsed(t *testing.T) {
	fg := newFakeGmail(t)
	fg.getResp = `{"id":"x"}`
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		if account != "default" {
			t.Errorf("account = %q", account)
		}
		return "ya29.from-oauth", nil
	})
	_, _ = executeGmailGetMessage(t.Context(), core.Job{
		Params: map[string]any{"id": "x"},
	}, nil)
	fg.mu.Lock()
	defer fg.mu.Unlock()
	if fg.lastGetAuth != "Bearer ya29.from-oauth" {
		t.Errorf("auth = %q", fg.lastGetAuth)
	}
}
