// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

type fakeDiscord struct {
	srv      *httptest.Server
	lastBody map[string]any
	lastWait string
	lastThr  string
	reject   bool
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	t.Helper()
	f := &fakeDiscord{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastWait = r.URL.Query().Get("wait")
		f.lastThr = r.URL.Query().Get("thread_id")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &f.lastBody)
		if f.reject {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 50027, "message": "Invalid Webhook Token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg1", "channel_id": "chan1"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDiscord) run(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	p := map[string]any{"webhook_url": f.srv.URL}
	for k, v := range params {
		p[k] = v
	}
	res, err := executeSendMessage(context.Background(), core.Job{ID: "j1", Params: p, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	return res
}

func TestSend_Success(t *testing.T) {
	f := newFakeDiscord(t)
	res := f.run(t, map[string]any{"content": "hello", "username": "CI Bot"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if f.lastWait != "true" {
		t.Errorf("wait = %q, want true (so Discord returns the message id)", f.lastWait)
	}
	if f.lastBody["content"] != "hello" || f.lastBody["username"] != "CI Bot" {
		t.Errorf("body = %v", f.lastBody)
	}
	if res.Output["message_id"].Inline != "msg1" {
		t.Errorf("message_id = %v", res.Output["message_id"].Inline)
	}
}

func TestSend_OmitsBlankOptionalFields(t *testing.T) {
	f := newFakeDiscord(t)
	f.run(t, map[string]any{"content": "hi"}, nil)
	if _, has := f.lastBody["username"]; has {
		t.Errorf("blank username must be omitted: %v", f.lastBody)
	}
	if _, has := f.lastBody["avatar_url"]; has {
		t.Errorf("blank avatar_url must be omitted: %v", f.lastBody)
	}
}

func TestSend_ContentInputOverridesParam(t *testing.T) {
	f := newFakeDiscord(t)
	f.run(t, map[string]any{"content": "typed"}, map[string]core.Ref{"content": {Inline: "wired"}})
	if f.lastBody["content"] != "wired" {
		t.Errorf("content = %v, want wired", f.lastBody["content"])
	}
}

func TestSend_ThreadID(t *testing.T) {
	f := newFakeDiscord(t)
	f.run(t, map[string]any{"content": "hi", "thread_id": "t99"}, nil)
	if f.lastThr != "t99" {
		t.Errorf("thread_id = %q", f.lastThr)
	}
}

func TestSend_Validation(t *testing.T) {
	f := newFakeDiscord(t)
	// Missing content.
	res := f.run(t, map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("missing content res = %+v", res)
	}
	// Over the 2000-char limit.
	res = f.run(t, map[string]any{"content": strings.Repeat("x", maxContentLen+1)}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("too-long res = %+v", res)
	}
}

func TestSend_MissingWebhookIsFriendly(t *testing.T) {
	res, err := executeSendMessage(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"content": "hi"}, // no webhook_url
	}, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("res = %+v", res)
	}
}

func TestSend_APIErrorSurfacesMessage(t *testing.T) {
	f := newFakeDiscord(t)
	f.reject = true
	res := f.run(t, map[string]any{"content": "hi"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "discord_error" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Error.Message, "Invalid Webhook Token") || !strings.Contains(res.Error.Message, "50027") {
		t.Errorf("error message = %q", res.Error.Message)
	}
}

// TestBuildEndpoint covers the query-merge and validation branches: a plain
// webhook URL gains wait=true; a thread_id is added; an existing query is
// preserved; and a non-http(s) URL is rejected.
func TestBuildEndpoint(t *testing.T) {
	t.Run("adds wait", func(t *testing.T) {
		got, err := buildEndpoint("https://discord.test/api/webhooks/1/tok", "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "wait=true") {
			t.Errorf("endpoint = %q, want wait=true", got)
		}
		if strings.Contains(got, "thread_id") {
			t.Errorf("no thread_id expected: %q", got)
		}
	})
	t.Run("adds thread_id", func(t *testing.T) {
		got, err := buildEndpoint("https://discord.test/api/webhooks/1/tok", "t99")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "thread_id=t99") {
			t.Errorf("endpoint = %q, want thread_id=t99", got)
		}
	})
	t.Run("preserves existing query", func(t *testing.T) {
		got, err := buildEndpoint("https://discord.test/api/webhooks/1/tok?foo=bar", "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "foo=bar") || !strings.Contains(got, "wait=true") {
			t.Errorf("endpoint = %q, want foo=bar and wait=true", got)
		}
	})
	t.Run("rejects non-http scheme", func(t *testing.T) {
		for _, bad := range []string{"ftp://x/y", "not a url", "://nope"} {
			if _, err := buildEndpoint(bad, ""); err == nil {
				t.Errorf("buildEndpoint(%q) = nil error, want rejection", bad)
			}
		}
	})
}

// TestSend_BadWebhookURL covers executeSendMessage's bad_param branch when
// buildEndpoint rejects the webhook URL (non-http scheme).
func TestSend_BadWebhookURL(t *testing.T) {
	res, err := executeSendMessage(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"webhook_url": "ftp://discord.test/webhook",
			"content":     "hi",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want bad_param", res)
	}
}

// TestSend_ContentInputNonText covers the bad_input branch for a non-text
// Content wire.
func TestSend_ContentInputNonText(t *testing.T) {
	res, err := executeSendMessage(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"webhook_url": "https://discord.test/webhook", "content": "typed"},
		Input:  map[string]core.Ref{"content": {Inline: map[string]any{"oops": true}}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("res = %+v, want bad_input", res)
	}
}

// TestSend_HTTPError covers the discord_http_error branch: the webhook host is
// unroutable so net.Do returns a transport error (private egress is on per
// TestMain, so this is a real dial failure, not an SSRF block).
func TestSend_HTTPError(t *testing.T) {
	res, err := executeSendMessage(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			// .invalid never resolves → transport error from net.Do.
			"webhook_url": "http://discord-nonexistent.invalid/api/webhooks/1/tok",
			"content":     "hi",
			"timeout_ms":  2000,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "discord_http_error" {
		t.Fatalf("res = %+v, want discord_http_error", res)
	}
}

// TestSend_AvatarURLIncluded covers the avatar_url payload branch.
func TestSend_AvatarURLIncluded(t *testing.T) {
	f := newFakeDiscord(t)
	f.run(t, map[string]any{"content": "hi", "avatar_url": "https://img.test/a.png"}, nil)
	if f.lastBody["avatar_url"] != "https://img.test/a.png" {
		t.Errorf("avatar_url = %v", f.lastBody["avatar_url"])
	}
}

// TestSend_ZeroTimeoutDefaults covers discordDo's timeout_ms<=0 → default
// branch while still completing a successful send.
func TestSend_ZeroTimeoutDefaults(t *testing.T) {
	f := newFakeDiscord(t)
	res := f.run(t, map[string]any{"content": "hi", "timeout_ms": 0}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
}

// TestExtractDiscordError covers the error-body extraction wrapper, including a
// body without a recognizable message.
func TestExtractDiscordError(t *testing.T) {
	if got := extractDiscordError([]byte(`{"code":50027,"message":"Invalid Webhook Token"}`)); !strings.Contains(got, "Invalid Webhook Token") {
		t.Errorf("got %q", got)
	}
	if got := extractDiscordError([]byte("not json")); got == "" {
		t.Errorf("expected a fallback message for non-json body")
	}
}
