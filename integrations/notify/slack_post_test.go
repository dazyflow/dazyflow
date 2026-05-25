package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// swapSlackURL points slack_post at an httptest server for the
// duration of the test. Returns a restore func through t.Cleanup so
// tests can run in parallel without leaking the override.
func swapSlackURL(t *testing.T, url string) {
	t.Helper()
	prev := slackPostURL
	slackPostURL = url
	t.Cleanup(func() { slackPostURL = prev })
}

func TestSlackPost_HappyPath(t *testing.T) {
	var got struct {
		auth        string
		contentType string
		body        map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		got.contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.body)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C123","ts":"1700000000.0001","message":{"permalink":"https://slack.example/C123/p1700"}}`)
	}))
	t.Cleanup(srv.Close)
	swapSlackURL(t, srv.URL)

	res, err := executeSlackPost(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "xoxb-test-token",
			"channel": "#general",
			"text":    "hello, world",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got.auth != "Bearer xoxb-test-token" {
		t.Errorf("auth = %q, want bearer", got.auth)
	}
	if got.contentType != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", got.contentType)
	}
	if got.body["channel"] != "#general" || got.body["text"] != "hello, world" {
		t.Errorf("body = %+v", got.body)
	}
	meta, ok := res.Output["meta"]
	if !ok {
		t.Fatal("missing meta output")
	}
	m, _ := meta.Inline.(map[string]any)
	if m["ts"] != "1700000000.0001" || m["channel"] != "C123" {
		t.Errorf("meta = %+v", m)
	}
}

// Slack's quirkiest failure shape: HTTP 200 with ok:false. The
// integration must surface this as an error, not as a success — a
// silent failure here would route past failure-propagation rules
// and let downstream nodes "act on" a message that was never sent.
func TestSlackPost_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	t.Cleanup(srv.Close)
	swapSlackURL(t, srv.URL)

	res, _ := executeSlackPost(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "xoxb-test",
			"channel": "#nope",
			"text":    "hi",
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "slack_api_error" {
		t.Fatalf("error = %+v, want slack_api_error", res.Error)
	}
	if res.Error.Message != "channel_not_found" {
		t.Errorf("error message = %q", res.Error.Message)
	}
}

// Refuse tokens that don't look like Slack tokens — a misconfigured
// ${secret:...} resolution would otherwise produce a confusing 401
// from Slack itself.
func TestSlackPost_BadTokenShape(t *testing.T) {
	res, _ := executeSlackPost(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "not-a-slack-token",
			"channel": "#x",
			"text":    "y",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v", res)
	}
}

// Text can come from an input port; the integration prefers it over
// params.text so a wired pipeline (e.g. ai_chat → slack_post) doesn't
// have to round-trip through the param panel.
func TestSlackPost_TextFromInputPort(t *testing.T) {
	var gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		gotText, _ = m["text"].(string)
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C","ts":"1.1","message":{"permalink":""}}`)
	}))
	t.Cleanup(srv.Close)
	swapSlackURL(t, srv.URL)

	res, _ := executeSlackPost(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "xoxb-x",
			"channel": "C123",
			"text":    "from-params",
		},
		Input: map[string]core.Ref{
			"text": {MIME: "text/plain", Inline: "from-input"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if gotText != "from-input" {
		t.Errorf("text = %q, want from-input (input port should win)", gotText)
	}
}
