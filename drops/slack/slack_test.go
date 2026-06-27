// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// withSlackAuthErr points the token lookup at a stub that always fails, so the
// drops exercise the resolveToken error path. base is unused by the call
// (auth fails first) but kept for symmetry with withSlackEnv.
func withSlackAuthErr(t *testing.T) {
	t.Helper()
	SetTokenLookup(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("no connected Slack workspace")
	})
	t.Cleanup(func() { SetTokenLookup(nil) })
}

// TestCovCurrentHTTPBase: the package default is returned when nothing is
// overridden, and SetHTTPBase swaps it.
func TestCovCurrentHTTPBase(t *testing.T) {
	if got := currentHTTPBase(); got != "https://slack.com/api" {
		t.Fatalf("default base = %q", got)
	}
	SetHTTPBase("http://example.test")
	t.Cleanup(func() { SetHTTPBase("https://slack.com/api") })
	if got := currentHTTPBase(); got != "http://example.test" {
		t.Fatalf("after Set base = %q", got)
	}
}

// TestCovDecodeSlackJSON covers the parse-failure path plus a well-formed
// envelope where the raw map carries extra fields.
func TestCovDecodeSlackJSON(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		_, _, err := decodeSlackJSON([]byte("not json"))
		if err == nil {
			t.Fatal("want parse error")
		}
	})
	t.Run("ok envelope", func(t *testing.T) {
		env, raw, err := decodeSlackJSON([]byte(`{"ok":true,"ts":"1.2","channel":"C1"}`))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !env.OK || raw["ts"] != "1.2" {
			t.Fatalf("env=%+v raw=%+v", env, raw)
		}
	})
	t.Run("error envelope", func(t *testing.T) {
		env, _, err := decodeSlackJSON([]byte(`{"ok":false,"error":"channel_not_found"}`))
		if err != nil || env.OK || env.Error != "channel_not_found" {
			t.Fatalf("env=%+v err=%v", env, err)
		}
	})
}

// TestCovSlackDo_Non2xx: an upstream non-2xx status is a transport error
// carrying the status code and body.
func TestCovSlackDo_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(srv.Close)

	_, _, err := slackDo(context.Background(), "GET", srv.URL+"/conversations.list", "xoxb-tok", nil, 2000)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("want 500 error, got %v", err)
	}
}

// TestCovSlackDo_DefaultTimeout: a non-positive timeout falls back to the
// 15s default and the GET (nil body) omits the Content-Type header.
func TestCovSlackDo_DefaultTimeout(t *testing.T) {
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	env, _, err := slackDo(context.Background(), "GET", srv.URL+"/x", "xoxb-tok", nil, 0)
	if err != nil || !env.OK {
		t.Fatalf("env=%+v err=%v", env, err)
	}
	if gotCT != "" {
		t.Errorf("GET should not set Content-Type, got %q", gotCT)
	}
}

// TestCovSlackDo_TransportError: an unreachable host surfaces a transport
// error (not an envelope).
func TestCovSlackDo_TransportError(t *testing.T) {
	_, _, err := slackDo(context.Background(), "GET", "http://127.0.0.1:1/x", "xoxb-tok", nil, 1000)
	if err == nil {
		t.Fatal("want transport error")
	}
}

// TestCovSendMessage_AuthFailure: a failing token lookup short-circuits to an
// auth error before any HTTP call.
func TestCovSendMessage_AuthFailure(t *testing.T) {
	withSlackAuthErr(t)
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c", "text": "hi", "account": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Fatalf("status=%q code=%v, want auth", res.Status, res.Error)
	}
}

// TestCovSendMessage_HTTPError: a non-2xx upstream surfaces slack_http_error.
func TestCovSendMessage_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	SetHTTPBase(srv.URL)
	SetTokenLookup(func(_ context.Context, a string) (string, error) { return "xoxb-" + a, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://slack.com/api")
		SetTokenLookup(nil)
	})

	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c", "text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "slack_http_error" {
		t.Fatalf("status=%q code=%v, want slack_http_error", res.Status, res.Error)
	}
}

// TestCovSendMessage_BadBlocks: a malformed blocks payload errors before any
// HTTP call.
func TestCovSendMessage_BadBlocks(t *testing.T) {
	withSlackEnv(t, "http://unused")
	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c"},
		Input:  map[string]core.Ref{"blocks": {Inline: 42}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("status=%q code=%v, want bad_input", res.Status, res.Error)
	}
}

// TestCovSendMessage_BlocksAndThreadTS: blocks + thread_ts both land in the
// posted payload, and a blocks-only message (no text) is accepted.
func TestCovSendMessage_BlocksAndThreadTS(t *testing.T) {
	srv := newSlackTestServer(t, map[string]any{"ok": true, "channel": "C1", "ts": "1"})
	withSlackEnv(t, srv.URL)

	res, _ := executeSlackSendMessage(context.Background(), core.Job{
		Params: map[string]any{"channel": "#c", "thread_ts": "1700000000.0001"},
		Input:  map[string]core.Ref{"blocks": {Inline: []any{map[string]any{"type": "divider"}}}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if srv.lastBody["thread_ts"] != "1700000000.0001" {
		t.Errorf("thread_ts = %v", srv.lastBody["thread_ts"])
	}
	if _, ok := srv.lastBody["blocks"]; !ok {
		t.Errorf("blocks missing from body: %+v", srv.lastBody)
	}
	if _, ok := srv.lastBody["text"]; ok {
		t.Errorf("text should be absent for blocks-only: %+v", srv.lastBody)
	}
}

// TestCovListChannels_AuthFailure: a failing token lookup yields an auth error.
func TestCovListChannels_AuthFailure(t *testing.T) {
	withSlackAuthErr(t)
	res, _ := executeSlackListChannels(context.Background(), core.Job{
		Params: map[string]any{"account": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Fatalf("status=%q code=%v, want auth", res.Status, res.Error)
	}
}

// TestCovListChannels_HTTPError: a non-2xx upstream surfaces slack_http_error.
func TestCovListChannels_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	SetHTTPBase(srv.URL)
	SetTokenLookup(func(_ context.Context, a string) (string, error) { return "xoxb-" + a, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://slack.com/api")
		SetTokenLookup(nil)
	})

	res, _ := executeSlackListChannels(context.Background(), core.Job{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "slack_http_error" {
		t.Fatalf("status=%q code=%v, want slack_http_error", res.Status, res.Error)
	}
}

// TestCovListChannels_SlackError: an {ok:false} envelope maps to slack_error,
// including the unknown-error fallback when no error string is present.
func TestCovListChannels_SlackError(t *testing.T) {
	cases := []struct {
		name string
		resp map[string]any
	}{
		{"named error", map[string]any{"ok": false, "error": "invalid_auth"}},
		{"empty error falls back", map[string]any{"ok": false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newSlackTestServer(t, c.resp)
			withSlackEnv(t, srv.URL)
			res, _ := executeSlackListChannels(context.Background(), core.Job{}, nil)
			if res.Status != core.StatusError || res.Error.Code != "slack_error" {
				t.Fatalf("status=%q code=%v, want slack_error", res.Status, res.Error)
			}
		})
	}
}

// TestCovListChannels_QueryParams: exclude_archived false omits the param and
// custom types/limit reach the query string.
func TestCovListChannels_QueryParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channels":[]}`))
	}))
	t.Cleanup(srv.Close)
	withSlackEnv(t, srv.URL)

	res, _ := executeSlackListChannels(context.Background(), core.Job{
		Params: map[string]any{"types": "im,mpim", "limit": 7, "exclude_archived": false},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(gotQuery, "types=im%2Cmpim") || !strings.Contains(gotQuery, "limit=7") {
		t.Errorf("query = %q", gotQuery)
	}
	if strings.Contains(gotQuery, "exclude_archived") {
		t.Errorf("exclude_archived should be omitted: %q", gotQuery)
	}
}

// TestCovOnMention_Standalone: standalone execution of the trigger returns the
// no_trigger_data sentinel and preserves the job id.
func TestCovOnMention_Standalone(t *testing.T) {
	res, err := executeSlackOnMention(context.Background(), core.Job{ID: "job-77"}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Fatalf("status=%q code=%v, want no_trigger_data", res.Status, res.Error)
	}
	if res.JobID != "job-77" {
		t.Errorf("JobID = %q", res.JobID)
	}
}

// TestCovListChannelsPicker covers the ListChannels resource picker: mapping
// channels to AccountResource, the #name label, id-only fallback, skipping
// rows without an id / non-map entries, the empty-workspace cases, and the
// error/auth paths.
func TestCovListChannelsPicker(t *testing.T) {
	t.Run("maps and filters", func(t *testing.T) {
		srv := newSlackTestServer(t, map[string]any{"ok": true, "channels": []any{
			map[string]any{"id": "C1", "name": "general"},
			map[string]any{"id": "C2"},      // id-only → label is the id
			map[string]any{"name": "no-id"}, // no id → skipped
			"not-a-map",                     // non-map → skipped
		}})
		withSlackEnv(t, srv.URL)
		out, err := ListChannels(context.Background(), core.Job{})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("got %d resources, want 2: %+v", len(out), out)
		}
		if out[0].ID != "C1" || out[0].Name != "#general" {
			t.Errorf("res[0] = %+v", out[0])
		}
		if out[1].ID != "C2" || out[1].Name != "C2" {
			t.Errorf("res[1] = %+v", out[1])
		}
	})

	t.Run("no channels key", func(t *testing.T) {
		srv := newSlackTestServer(t, map[string]any{"ok": true})
		withSlackEnv(t, srv.URL)
		out, err := ListChannels(context.Background(), core.Job{})
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	})

	t.Run("slack error", func(t *testing.T) {
		srv := newSlackTestServer(t, map[string]any{"ok": false, "error": "invalid_auth"})
		withSlackEnv(t, srv.URL)
		_, err := ListChannels(context.Background(), core.Job{})
		if err == nil || !strings.Contains(err.Error(), "invalid_auth") {
			t.Fatalf("want invalid_auth error, got %v", err)
		}
	})

	t.Run("slack error empty falls back", func(t *testing.T) {
		srv := newSlackTestServer(t, map[string]any{"ok": false})
		withSlackEnv(t, srv.URL)
		_, err := ListChannels(context.Background(), core.Job{})
		if err == nil || !strings.Contains(err.Error(), "unknown error") {
			t.Fatalf("want unknown error fallback, got %v", err)
		}
	})

	t.Run("auth failure", func(t *testing.T) {
		withSlackAuthErr(t)
		_, err := ListChannels(context.Background(), core.Job{Params: map[string]any{"account": "x"}})
		if err == nil {
			t.Fatal("want auth error")
		}
	})

	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		withSlackEnv(t, srv.URL)
		_, err := ListChannels(context.Background(), core.Job{})
		if err == nil {
			t.Fatal("want http error")
		}
	})
}
