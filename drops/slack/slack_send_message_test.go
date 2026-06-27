// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// slackTestServer stands in for the Slack Web API: it records the last
// request and returns the configured envelope.
type slackTestServer struct {
	*httptest.Server
	lastPath string
	lastBody map[string]any
	lastAuth string
	resp     map[string]any
}

func newSlackTestServer(t *testing.T, resp map[string]any) *slackTestServer {
	t.Helper()
	s := &slackTestServer{resp: resp}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.lastPath = r.URL.Path
		s.lastAuth = r.Header.Get("Authorization")
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &s.lastBody)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.resp)
	}))
	t.Cleanup(s.Close)
	return s
}

// withSlackEnv points the package at the test server and a fixed token,
// restoring globals afterwards.
func withSlackEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "xoxb-test-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://slack.com/api")
		SetTokenLookup(nil)
	})
}

func TestSlackSendMessage_PostsTextAndChannel(t *testing.T) {
	srv := newSlackTestServer(t, map[string]any{"ok": true, "channel": "C123", "ts": "1700000000.000100"})
	withSlackEnv(t, srv.URL)

	res, err := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#general", "account": "default", "text": "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastPath != "/chat.postMessage" {
		t.Errorf("path = %q", srv.lastPath)
	}
	if srv.lastAuth != "Bearer xoxb-test-default" {
		t.Errorf("auth = %q", srv.lastAuth)
	}
	if srv.lastBody["channel"] != "#general" || srv.lastBody["text"] != "hello" {
		t.Errorf("body = %+v", srv.lastBody)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["ok"] != true || meta["ts"] != "1700000000.000100" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestSlackSendMessage_TextInputOverridesParam(t *testing.T) {
	srv := newSlackTestServer(t, map[string]any{"ok": true, "channel": "C1", "ts": "1"})
	withSlackEnv(t, srv.URL)

	// A wired 'text' input wins over the typed param.
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c", "text": "from-param"},
		Input:  map[string]core.Ref{"text": {Inline: "from-text"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastBody["text"] != "from-text" {
		t.Errorf("text = %v, want text input to win", srv.lastBody["text"])
	}
}

func TestSlackSendMessage_ChannelInputOverridesParam(t *testing.T) {
	srv := newSlackTestServer(t, map[string]any{"ok": true, "channel": "C9", "ts": "1"})
	withSlackEnv(t, srv.URL)

	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#from-param", "text": "hi"},
		Input:  map[string]core.Ref{"channel": {Inline: "#from-wire"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastBody["channel"] != "#from-wire" {
		t.Errorf("channel = %v, want channel input to win", srv.lastBody["channel"])
	}
}

func TestSlackSendMessage_StructuredChannelIsError(t *testing.T) {
	withSlackEnv(t, "http://unused")
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c", "text": "hi"},
		Input:  map[string]core.Ref{"channel": {Inline: map[string]any{"id": "C1"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v, want bad_input", res.Status, res.Error)
	}
}

func TestSlackSendMessage_StructuredTextIsError(t *testing.T) {
	withSlackEnv(t, "http://unused")
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c"},
		Input:  map[string]core.Ref{"text": {Inline: []any{map[string]any{"line": "x"}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v, want bad_input", res.Status, res.Error)
	}
}

func TestSlackSendMessage_MissingChannel(t *testing.T) {
	withSlackEnv(t, "http://unused")
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v, want bad_param", res.Status, res.Error)
	}
}

func TestSlackSendMessage_NoContent(t *testing.T) {
	withSlackEnv(t, "http://unused")
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v, want bad_input", res.Status, res.Error)
	}
}

func TestSlackSendMessage_SlackLogicalError(t *testing.T) {
	srv := newSlackTestServer(t, map[string]any{"ok": false, "error": "channel_not_found"})
	withSlackEnv(t, srv.URL)
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#nope", "text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "slack_error" {
		t.Errorf("status=%q code=%v, want slack_error", res.Status, res.Error)
	}
}

func TestSlackListChannels_ReturnsChannels(t *testing.T) {
	srv := newSlackTestServer(t, map[string]any{"ok": true, "channels": []any{
		map[string]any{"id": "C1", "name": "general"},
		map[string]any{"id": "C2", "name": "random"},
	}})
	withSlackEnv(t, srv.URL)

	res, err := executeSlackListChannels(context.Background(), core.Job{
		Params: map[string]any{"account": "default", "limit": 50},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v / %v", res.Status, res.Error, err)
	}
	if srv.lastPath != "/conversations.list" {
		t.Errorf("path = %q", srv.lastPath)
	}
	chans := res.Output["channels"].Inline.([]any)
	if len(chans) != 2 {
		t.Errorf("got %d channels, want 2", len(chans))
	}
}
