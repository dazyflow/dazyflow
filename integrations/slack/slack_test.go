package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fakeSlack stands in for the real Slack API. Each handler records
// the last received request so tests can assert on shape, then
// returns whatever the test configures via the response fields.
type fakeSlack struct {
	server *httptest.Server

	mu                  sync.Mutex
	lastPostMessageReq  map[string]any
	lastPostMessageAuth string
	postMessageResp     string

	lastListChannelsQ    string
	lastListChannelsAuth string
	listChannelsResp     string
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{
		postMessageResp:  `{"ok":true,"channel":"C111","ts":"1640995200.000100"}`,
		listChannelsResp: `{"ok":true,"channels":[{"id":"C111","name":"data-ops"},{"id":"C222","name":"general"}]}`,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		_ = json.Unmarshal(body, &f.lastPostMessageReq)
		f.lastPostMessageAuth = r.Header.Get("Authorization")
		resp := f.postMessageResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	})
	mux.HandleFunc("/api/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastListChannelsQ = r.URL.RawQuery
		f.lastListChannelsAuth = r.Header.Get("Authorization")
		resp := f.listChannelsResp
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	// Point the slack package at our fake. Restore after test so
	// other tests in the package don't leak the override.
	prev := currentHTTPBase()
	SetHTTPBase(f.server.URL + "/api")
	t.Cleanup(func() { SetHTTPBase(prev) })
	return f
}

// installTokenLookup wires a deterministic stub for the duration of
// a test, then restores whatever was there (typically nil).
func installTokenLookup(t *testing.T, fn TokenLookup) {
	t.Helper()
	tokenLookupMu.RLock()
	prev := tokenLookup
	tokenLookupMu.RUnlock()
	SetTokenLookup(fn)
	t.Cleanup(func() { SetTokenLookup(prev) })
}

// ===== slack_send_message ===========================================

func TestSlackSendMessage_BasicTextFromParams(t *testing.T) {
	fs := newFakeSlack(t)
	res, err := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "xoxb-test",
			"channel": "#data-ops",
			"text":    "Pipeline done!",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastPostMessageReq["channel"] != "#data-ops" {
		t.Errorf("channel = %v, want #data-ops", fs.lastPostMessageReq["channel"])
	}
	if fs.lastPostMessageReq["text"] != "Pipeline done!" {
		t.Errorf("text = %v", fs.lastPostMessageReq["text"])
	}
	if fs.lastPostMessageAuth != "Bearer xoxb-test" {
		t.Errorf("auth header = %q", fs.lastPostMessageAuth)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["ok"] != true || meta["ts"] != "1640995200.000100" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestSlackSendMessage_BodyInputWinsOverText(t *testing.T) {
	fs := newFakeSlack(t)
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "xoxb-test",
			"channel": "C123",
			"text":    "ignored-from-params",
		},
		Input: map[string]core.Ref{
			"body": {Inline: "from-port"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastPostMessageReq["text"] != "from-port" {
		t.Errorf("text = %v, want from-port (input port wins)", fs.lastPostMessageReq["text"])
	}
}

func TestSlackSendMessage_ObjectBodyRejected(t *testing.T) {
	// Slack expects strings, not arbitrary objects. We could JSON-
	// stringify but that's misleading — the user should
	// deliberately format the message upstream. Reject with a clear
	// error code.
	_ = newFakeSlack(t)
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "xoxb-test", "channel": "C123"},
		Input: map[string]core.Ref{
			"body": {Inline: map[string]any{"text": "wrong shape"}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q, want bad_input", res.Status, res.Error.Code)
	}
}

func TestSlackSendMessage_NoTextOrBlocksRejected(t *testing.T) {
	// Empty message would be Slack-rejected as no_text anyway. We
	// reject earlier with a clearer message — the user usually
	// forgot to wire a body input, not to format the message.
	_ = newFakeSlack(t)
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "xoxb-test", "channel": "C123"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q, want bad_input", res.Status, res.Error.Code)
	}
}

func TestSlackSendMessage_BlocksOnly(t *testing.T) {
	// blocks-only (no text) is a valid Slack call — Block Kit
	// messages.
	fs := newFakeSlack(t)
	blocks := []any{
		map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "Hello"}},
	}
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{
			"token":   "xoxb-test",
			"channel": "C123",
			"blocks":  blocks,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.lastPostMessageReq["blocks"]; !ok {
		t.Errorf("blocks missing from request body: %+v", fs.lastPostMessageReq)
	}
}

func TestSlackSendMessage_ThreadTs(t *testing.T) {
	fs := newFakeSlack(t)
	_, _ = executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{
			"token":     "xoxb-test",
			"channel":   "C123",
			"text":      "reply",
			"thread_ts": "1640995200.000100",
		},
	}, nil)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastPostMessageReq["thread_ts"] != "1640995200.000100" {
		t.Errorf("thread_ts = %v", fs.lastPostMessageReq["thread_ts"])
	}
}

func TestSlackSendMessage_SlackErrorEnvelope(t *testing.T) {
	// HTTP 200 + {"ok":false,"error":"channel_not_found"} is Slack's
	// way of reporting application errors. Must surface as a clear
	// error code with the Slack error string in the message.
	fs := newFakeSlack(t)
	fs.postMessageResp = `{"ok":false,"error":"channel_not_found"}`
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{
			"token": "xoxb-test", "channel": "C-ghost", "text": "x",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "slack_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "channel_not_found") {
		t.Errorf("error message missing Slack detail: %q", res.Error.Message)
	}
}

func TestSlackSendMessage_MissingChannel(t *testing.T) {
	_ = newFakeSlack(t)
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{"token": "xoxb-test", "text": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

// ===== token resolution =============================================

func TestSlackSendMessage_OAuthLookupHookUsed(t *testing.T) {
	// When no `token` param is given, the drop must fall back to
	// the registered OAuth lookup, passing the requested `account`.
	fs := newFakeSlack(t)
	var sawAccount string
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		sawAccount = account
		return "xoxb-from-oauth", nil
	})
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{
			"account": "main",
			"channel": "C123",
			"text":    "x",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if sawAccount != "main" {
		t.Errorf("lookup got account=%q, want main", sawAccount)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastPostMessageAuth != "Bearer xoxb-from-oauth" {
		t.Errorf("auth header = %q, want token from OAuth lookup", fs.lastPostMessageAuth)
	}
}

func TestSlackSendMessage_DefaultAccountIsDefault(t *testing.T) {
	// When neither token nor account are set, the lookup is called
	// with "default".
	_ = newFakeSlack(t)
	var sawAccount string
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		sawAccount = account
		return "xoxb-x", nil
	})
	_, _ = executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{"channel": "C123", "text": "x"},
	}, nil)
	if sawAccount != "default" {
		t.Errorf("account = %q, want default", sawAccount)
	}
}

func TestSlackSendMessage_NoTokenAndNoLookupIsAuthError(t *testing.T) {
	_ = newFakeSlack(t)
	installTokenLookup(t, nil)
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{"channel": "C123", "text": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%q, want auth", res.Status, res.Error.Code)
	}
}

func TestSlackSendMessage_LookupErrorSurfaces(t *testing.T) {
	_ = newFakeSlack(t)
	installTokenLookup(t, func(_ context.Context, _ string) (string, error) {
		return "", io.ErrUnexpectedEOF
	})
	res, _ := executeSlackSendMessage(t.Context(), core.Job{
		Params: map[string]any{"channel": "C123", "text": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "auth" {
		t.Errorf("status=%q code=%q, want auth", res.Status, res.Error.Code)
	}
}

// ===== slack_list_channels ==========================================

func TestSlackListChannels_BasicResponse(t *testing.T) {
	_ = newFakeSlack(t)
	res, err := executeSlackListChannels(t.Context(), core.Job{
		Params: map[string]any{"token": "xoxb-test"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	chans := res.Output["channels"].Inline.([]any)
	if len(chans) != 2 {
		t.Fatalf("got %d channels, want 2", len(chans))
	}
	first := chans[0].(map[string]any)
	if first["name"] != "data-ops" {
		t.Errorf("first channel name = %v", first["name"])
	}
}

func TestSlackListChannels_QueryStringShape(t *testing.T) {
	fs := newFakeSlack(t)
	_, _ = executeSlackListChannels(t.Context(), core.Job{
		Params: map[string]any{
			"token":            "xoxb-test",
			"types":            "public_channel",
			"limit":            50,
			"exclude_archived": true,
		},
	}, nil)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !strings.Contains(fs.lastListChannelsQ, "types=public_channel") {
		t.Errorf("query missing types: %q", fs.lastListChannelsQ)
	}
	if !strings.Contains(fs.lastListChannelsQ, "limit=50") {
		t.Errorf("query missing limit: %q", fs.lastListChannelsQ)
	}
	if !strings.Contains(fs.lastListChannelsQ, "exclude_archived=true") {
		t.Errorf("query missing exclude_archived: %q", fs.lastListChannelsQ)
	}
}

func TestSlackListChannels_SlackError(t *testing.T) {
	fs := newFakeSlack(t)
	fs.listChannelsResp = `{"ok":false,"error":"invalid_auth"}`
	res, _ := executeSlackListChannels(t.Context(), core.Job{
		Params: map[string]any{"token": "xoxb-bad"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "slack_error" {
		t.Fatalf("status=%q code=%q", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "invalid_auth") {
		t.Errorf("missing slack error code: %q", res.Error.Message)
	}
}

func TestSlackListChannels_EmptyResultStillEmitsChannelsPort(t *testing.T) {
	// When Slack returns ok with no channels, downstream consumers
	// (a map_rows that picks a name) shouldn't see a nil — the
	// output port must always carry an array, even if empty.
	fs := newFakeSlack(t)
	fs.listChannelsResp = `{"ok":true,"channels":[]}`
	res, _ := executeSlackListChannels(t.Context(), core.Job{
		Params: map[string]any{"token": "xoxb-test"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	chans, ok := res.Output["channels"].Inline.([]any)
	if !ok {
		t.Fatalf("channels = %T, want []any", res.Output["channels"].Inline)
	}
	if len(chans) != 0 {
		t.Errorf("len(channels) = %d, want 0", len(chans))
	}
}

func TestSlackListChannels_UsesOAuthLookup(t *testing.T) {
	fs := newFakeSlack(t)
	installTokenLookup(t, func(_ context.Context, account string) (string, error) {
		if account != "default" {
			t.Errorf("account = %q", account)
		}
		return "xoxb-from-oauth", nil
	})
	_, _ = executeSlackListChannels(t.Context(), core.Job{
		Params: map[string]any{},
	}, nil)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.lastListChannelsAuth != "Bearer xoxb-from-oauth" {
		t.Errorf("auth = %q", fs.lastListChannelsAuth)
	}
}
