// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops/github"
	"github.com/dazyflow/dazyflow/engine"
)

// signGitHub produces the X-Hub-Signature-256 header value GitHub
// sends. Scheme: "sha256=" + hex(hmac-sha256(secret, body)).
func signGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type githubHarness struct {
	*gatewayHarness
	secret string
	// fanouts receives one value per completed background fanout, so a test
	// waits on the work rather than on the clock. Buffered and sent to
	// non-blockingly: a test that never waits must not wedge the handler's
	// goroutine. Mirrors stripeHarness.
	fanouts chan struct{}
}

func newGitHubHarness(t *testing.T) *githubHarness {
	t.Helper()
	gh := newGatewayHarness(t)
	h := &githubHarness{
		gatewayHarness: gh,
		secret:         "test-webhook-secret",
		fanouts:        make(chan struct{}, 8),
	}
	events := NewGitHubEventsHandler(gh.svc, h.secret)
	events.fanoutDone = func() {
		select {
		case h.fanouts <- struct{}{}:
		default:
		}
	}
	gh.gw.GitHubEvents = events
	return h
}

// awaitFanout blocks until one dispatched fanout has finished. The timeout is
// a hang guard, not a race — see stripeHarness.awaitFanout.
func (h *githubHarness) awaitFanout(t *testing.T) {
	t.Helper()
	select {
	case <-h.fanouts:
	case <-time.After(30 * time.Second):
		t.Fatal("the event's background fanout never finished")
	}
}

func (h *githubHarness) post(t *testing.T, path, event string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signGitHub(h.secret, body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "test-delivery-id")
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestGitHubEvents_PingAcked(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	body := []byte(`{"zen":"Practicality beats purity."}`)
	rw := h.post(t, "/api/v1/events/github/t", "ping", body)
	if rw.Code != http.StatusOK {
		t.Errorf("ping = %d want 200; body=%s", rw.Code, rw.Body.String())
	}
}

func TestGitHubEvents_BadSignatureRejected(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	body := []byte(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	req.Header.Set("X-GitHub-Event", "push")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestGitHubEvents_MissingSignatureRejected(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-GitHub-Event", "push")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestGitHubEvents_NotConfiguredReturns501(t *testing.T) {
	t.Parallel()
	gh := newGatewayHarness(t)
	// gw.GitHubEvents intentionally nil.
	body := []byte(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "ping")
	rw := httptest.NewRecorder()
	ServeForTest(gh.gw, rw, req)
	if rw.Code != http.StatusNotImplemented {
		t.Errorf("code=%d want 501", rw.Code)
	}
}

func TestGitHubEvents_PushDispatchesToSubscribedGraphs(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	g := core.Graph{
		ID: "deploy-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "github_on_push"}},
	}
	savePublished(t, h.ws, g)

	event := map[string]any{
		"ref":    "refs/heads/main",
		"before": "abc123",
		"after":  "def456",
		"commits": []any{
			map[string]any{"id": "def456", "message": "Add feature"},
		},
		"repository": map[string]any{"full_name": "klahr/dazyflow"},
		"pusher":     map[string]any{"name": "alice"},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/github/t", "push", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	// The fanout runs in the background, so wait for it rather than for the
	// clock, then assert once.
	h.awaitFanout(t)
	runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: "t", Workspace: "ws", GraphID: "deploy-graph",
	})
	if err != nil {
		t.Fatalf("list graph runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("the event dispatched no run")
	}
	// Verify the trigger node's outputs got the right values.
	node, err := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
	if err != nil {
		t.Fatalf("get node record: %v", err)
	}
	if node.Status != core.JobStatusSucceeded {
		t.Fatalf("trigger node status=%q want succeeded", node.Status)
	}
	if got, _ := node.Result.Output["ref"].Inline.(string); got != "refs/heads/main" {
		t.Errorf("ref port = %q", got)
	}
	if got, _ := node.Result.Output["after"].Inline.(string); got != "def456" {
		t.Errorf("after port = %q", got)
	}
}

func TestGitHubEvents_PullRequestOpenedDispatches(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	g := core.Graph{
		ID: "triage-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "github_on_new_pr"}},
	}
	savePublished(t, h.ws, g)

	event := map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number":   42,
			"title":    "Add fizzbuzz",
			"body":     "Fixes #1",
			"html_url": "https://github.com/klahr/dazyflow/pull/42",
			"user":     map[string]any{"login": "alice"},
			"head":     map[string]any{"ref": "feature/fizzbuzz"},
			"base":     map[string]any{"ref": "main"},
		},
		"repository": map[string]any{"full_name": "klahr/dazyflow"},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/github/t", "pull_request", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	// The fanout runs in the background, so wait for it rather than for the
	// clock, then assert once.
	h.awaitFanout(t)
	runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: "t", Workspace: "ws", GraphID: "triage-graph",
	})
	if err != nil {
		t.Fatalf("list graph runs: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("the event dispatched no run")
	}
	node, err := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
	if err != nil {
		t.Fatalf("get node record: %v", err)
	}
	if node.Status != core.JobStatusSucceeded {
		t.Fatalf("status=%q", node.Status)
	}
	if got, _ := node.Result.Output["number"].Inline.(string); got != "42" {
		t.Errorf("number = %q", got)
	}
	if got, _ := node.Result.Output["title"].Inline.(string); got != "Add fizzbuzz" {
		t.Errorf("title = %q", got)
	}
	if got, _ := node.Result.Output["author"].Inline.(string); got != "alice" {
		t.Errorf("author = %q", got)
	}
	if got, _ := node.Result.Output["head_ref"].Inline.(string); got != "feature/fizzbuzz" {
		t.Errorf("head_ref = %q", got)
	}
}

func TestGitHubEvents_PullRequestNonOpenedAckedNotDispatched(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	event := map[string]any{
		"action":       "closed",
		"pull_request": map[string]any{"number": 1},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/github/t", "pull_request", body)
	if rw.Code != http.StatusOK {
		t.Errorf("code=%d want 200 (closed PR should ack)", rw.Code)
	}
}

func TestGitHubEvents_UnknownEventAcked(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	body := []byte(`{"surprise": true}`)
	rw := h.post(t, "/api/v1/events/github/t", "release", body)
	if rw.Code != http.StatusOK {
		t.Errorf("code=%d want 200 (unknown event should ack)", rw.Code)
	}
}

func TestGitHubOnPush_StandaloneRunErrors(t *testing.T) {
	t.Parallel()
	trans, ok := engine.Default.Get("github_on_push")
	if !ok {
		t.Fatal("github_on_push not registered")
	}
	res, _ := trans.Execute(t.Context(), core.Job{ID: "j"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Errorf("standalone github_on_push should error with no_trigger_data, got %+v", res)
	}
}

func TestGitHubOnNewPR_StandaloneRunErrors(t *testing.T) {
	t.Parallel()
	trans, ok := engine.Default.Get("github_on_new_pr")
	if !ok {
		t.Fatal("github_on_new_pr not registered")
	}
	res, _ := trans.Execute(t.Context(), core.Job{ID: "j"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Errorf("standalone github_on_new_pr should error with no_trigger_data, got %+v", res)
	}
}

// TestGitHubEvents_WrongSigPrefixRejected — a valid HMAC hex under the
// wrong algorithm prefix (sha1=) must be rejected; the handler requires
// the sha256= scheme GitHub actually uses.
func TestGitHubEvents_WrongSigPrefixRejected(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha1="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", "push")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("wrong sig prefix code=%d, want 401", rw.Code)
	}
}

// TestGitHubEvents_UppercaseHexSigRejected — GitHub signs with lowercase
// hex; an uppercased hex of an otherwise-correct HMAC must not validate
// (the compare is byte-for-byte, not case-folded).
func TestGitHubEvents_UppercaseHexSigRejected(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(body)
	upper := string(bytes.ToUpper([]byte(hex.EncodeToString(mac.Sum(nil)))))
	req := httptest.NewRequest("POST", "/api/v1/events/github/t", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+upper)
	req.Header.Set("X-GitHub-Event", "push")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("uppercase hex sig code=%d, want 401", rw.Code)
	}
}

// TestGitHubEvents_SparsePushAcked — a push with most fields absent
// (only ref) is structurally valid; the handler must parse it without
// erroring (200), not 4xx/5xx on missing commits/repository/pusher.
func TestGitHubEvents_SparsePushAcked(t *testing.T) {
	t.Parallel()
	h := newGitHubHarness(t)
	rw := h.post(t, "/api/v1/events/github/t", "push", []byte(`{"ref":"refs/heads/main"}`))
	if rw.Code != http.StatusOK {
		t.Errorf("sparse push code=%d, want 200", rw.Code)
	}
}
