// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops/slack"
	"github.com/dazyflow/dazyflow/engine"
)

// signSlackRequest produces the X-Slack-Signature header value Slack
// would send for (timestamp, body) under signingSecret. Mirrors the
// scheme documented at
// https://api.slack.com/authentication/verifying-requests-from-slack
func signSlackRequest(t *testing.T, secret string, ts int64, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "v0:%d:", ts)
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// slackHarness wraps a gatewayHarness with the Slack events handler
// wired up. Tests poke ServeForTest with /api/v1/events/slack/... and
// inspect the harness for downstream side effects.
type slackHarness struct {
	*gatewayHarness
	secret string
	frozen time.Time // time the handler sees in verifySignature
}

func newSlackHarness(t *testing.T) *slackHarness {
	t.Helper()
	gh := newGatewayHarness(t)
	sh := &slackHarness{
		gatewayHarness: gh,
		secret:         "shh-this-is-the-test-secret",
		frozen:         time.Unix(1700000000, 0).UTC(),
	}
	handler := NewSlackEventsHandler(gh.svc, sh.secret)
	handler.now = func() time.Time { return sh.frozen }
	gh.gw.SlackEvents = handler
	return sh
}

// post fires a Slack-style POST. ts=0 → use the frozen clock.
func (h *slackHarness) post(t *testing.T, path string, body []byte, ts int64) *httptest.ResponseRecorder {
	t.Helper()
	if ts == 0 {
		ts = h.frozen.Unix()
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", signSlackRequest(t, h.secret, ts, body))
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestSlackEvents_URLVerificationEchoesChallenge(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"hello-world-token","token":"deprecated"}`)
	rw := h.post(t, "/api/v1/events/slack/t", body, 0)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}
	if got := rw.Body.String(); got != "hello-world-token" {
		t.Errorf("body=%q want %q", got, "hello-world-token")
	}
}

func TestSlackEvents_BadSignatureRejected(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	ts := h.frozen.Unix()
	req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestSlackEvents_MissingHeadersRejected(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
	// no headers
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestSlackEvents_StaleTimestampRejected(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	// 10 minutes old — outside Slack's 5-minute replay window.
	stale := h.frozen.Add(-10 * time.Minute).Unix()
	rw := h.post(t, "/api/v1/events/slack/t", body, stale)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestSlackEvents_FutureTimestampRejected(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	future := h.frozen.Add(10 * time.Minute).Unix()
	rw := h.post(t, "/api/v1/events/slack/t", body, future)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestSlackEvents_NotConfiguredReturns501(t *testing.T) {
	gh := newGatewayHarness(t)
	// gw.SlackEvents left nil.
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
	rw := httptest.NewRecorder()
	ServeForTest(gh.gw, rw, req)
	if rw.Code != http.StatusNotImplemented {
		t.Errorf("code=%d want 501", rw.Code)
	}
}

// TestSlackEvents_AppMentionFiresSubscribedGraphs is the integration
// shape — proves the end-to-end seed flow. A graph in t/ws subscribes
// to slack_on_mention; we POST an app_mention event; we wait briefly
// for the (background) fanout to land; we then assert a graph-record
// landed in the jobstore.
func TestSlackEvents_AppMentionFiresSubscribedGraphs(t *testing.T) {
	h := newSlackHarness(t)
	// Save a graph with a slack_on_mention node.
	g := core.Graph{
		ID: "mention-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "trig", Module: "slack_on_mention"},
		},
	}
	savePublished(t, h.ws, g)

	event := map[string]any{
		"type":    "event_callback",
		"team_id": "T123",
		"event": map[string]any{
			"type":    "app_mention",
			"user":    "U456",
			"text":    "<@U999> hello bot",
			"channel": "C789",
			"ts":      "1700000000.000100",
		},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/slack/t", body, 0)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	// Fanout is a goroutine — give it a moment, then assert the
	// graph-record landed. Polling vs sleeping so the test stays
	// fast on a happy path.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "mention-graph",
		})
		if err == nil && len(runs) > 0 {
			// Found one — verify the seed lit up the trigger node.
			node, err := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
			if err != nil {
				t.Fatalf("get node record: %v", err)
			}
			if node.Status != core.JobStatusSucceeded {
				t.Fatalf("trigger node status=%q want succeeded", node.Status)
			}
			if node.Result == nil || node.Result.Output == nil {
				t.Fatalf("trigger node has no output: %+v", node.Result)
			}
			if got, _ := node.Result.Output["text"].Inline.(string); got != "<@U999> hello bot" {
				t.Errorf("text port = %q", got)
			}
			if got, _ := node.Result.Output["channel"].Inline.(string); got != "C789" {
				t.Errorf("channel port = %q", got)
			}
			if got, _ := node.Result.Output["team"].Inline.(string); got != "T123" {
				t.Errorf("team port = %q", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no graph-record materialized within 2s")
}

func TestSlackEvents_ChannelFilterSkipsMismatchedGraphs(t *testing.T) {
	h := newSlackHarness(t)
	// Two graphs, both subscribed to slack_on_mention. Graph A
	// filters on channel C111, graph B filters on C222. An event
	// in C111 should fire ONLY graph A.
	graphA := core.Graph{
		ID: "graph-a", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{
			ID: "trig", Module: "slack_on_mention",
			Params: map[string]any{"channel_filter": "C111"},
		}},
	}
	graphB := core.Graph{
		ID: "graph-b", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{
			ID: "trig", Module: "slack_on_mention",
			Params: map[string]any{"channel_filter": "C222"},
		}},
	}
	savePublished(t, h.ws, graphA)
	savePublished(t, h.ws, graphB)

	event := map[string]any{
		"type":    "event_callback",
		"team_id": "T",
		"event": map[string]any{
			"type":    "app_mention",
			"channel": "C111",
			"text":    "<@U> ping",
		},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/slack/t", body, 0)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	var fired map[string]bool
	for time.Now().Before(deadline) {
		fired = map[string]bool{}
		runs, _ := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws",
		})
		for _, r := range runs {
			fired[r.GraphID] = true
		}
		if fired["graph-a"] {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !fired["graph-a"] {
		t.Errorf("graph-a (channel_filter=C111) should fire for C111 event")
	}
	if fired["graph-b"] {
		t.Errorf("graph-b (channel_filter=C222) should NOT fire for C111 event")
	}
}

func TestSlackEvents_EmptyChannelFilterMatchesAll(t *testing.T) {
	// Backward compat: graphs without channel_filter param must
	// still fire for any channel. Pre-filter authoring depended
	// on this behavior.
	h := newSlackHarness(t)
	g := core.Graph{
		ID: "old-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "slack_on_mention"}},
	}
	savePublished(t, h.ws, g)
	event := map[string]any{
		"type":    "event_callback",
		"team_id": "T",
		"event":   map[string]any{"type": "app_mention", "channel": "C-any", "text": "hi"},
	}
	body, _ := json.Marshal(event)
	h.post(t, "/api/v1/events/slack/t", body, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "old-graph",
		})
		if len(runs) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("unfiltered graph should fire for any channel")
}

func TestSlackEvents_NonAppMentionEventIsAcked(t *testing.T) {
	h := newSlackHarness(t)
	// reaction_added isn't subscribed — handler should 200 and not
	// fire anything.
	body, _ := json.Marshal(map[string]any{
		"type":    "event_callback",
		"team_id": "T123",
		"event":   map[string]any{"type": "reaction_added"},
	})
	rw := h.post(t, "/api/v1/events/slack/t", body, 0)
	if rw.Code != http.StatusOK {
		t.Errorf("code=%d want 200", rw.Code)
	}
}

func TestSlackEvents_UnknownEnvelopeTypeIsAcked(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"surprise_party","field":"value"}`)
	rw := h.post(t, "/api/v1/events/slack/t", body, 0)
	if rw.Code != http.StatusOK {
		t.Errorf("code=%d want 200", rw.Code)
	}
}

func TestSlackOnMention_StandaloneRunErrors(t *testing.T) {
	// The drop's Execute is called when the user runs the graph
	// manually (no Slack event seeded the node). Should be a clear
	// "no event" error, not a silent success — same shape as
	// webhook_input's no_trigger_data.
	trans, ok := engine.Default.Get("slack_on_mention")
	if !ok {
		t.Fatal("slack_on_mention not registered")
	}
	res, err := trans.Execute(t.Context(), core.Job{ID: "j", NodeID: "n"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status=%q want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "no_trigger_data" {
		t.Fatalf("error=%+v want code=no_trigger_data", res.Error)
	}
}

// TestSlackEvents_NonIntegerTimestampRejected — a non-numeric
// X-Slack-Request-Timestamp must be rejected (401), not parsed into a
// zero/garbage time that could slip past the replay window.
func TestSlackEvents_NonIntegerTimestampRejected(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"x"}`)
	req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", "not-a-number")
	req.Header.Set("X-Slack-Signature", "v0=deadbeef")
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("non-integer timestamp code=%d, want 401", rw.Code)
	}
}

// TestSlackEvents_RetryHeaderTolerated — Slack re-delivers events with
// X-Slack-Retry-Num; the handler must still ack a valid retry (200),
// not choke on the extra header.
func TestSlackEvents_RetryHeaderTolerated(t *testing.T) {
	h := newSlackHarness(t)
	body := []byte(`{"type":"url_verification","challenge":"abc"}`)
	ts := h.frozen.Unix()
	req := httptest.NewRequest("POST", "/api/v1/events/slack/t", bytes.NewReader(body))
	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", signSlackRequest(t, h.secret, ts, body))
	req.Header.Set("X-Slack-Retry-Num", "2")
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Errorf("retry code=%d, want 200", rw.Code)
	}
}
