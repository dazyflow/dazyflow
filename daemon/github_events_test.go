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

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	_ "git.sr.ht/~klahr/hazy-flow/drops/github"
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
}

func newGitHubHarness(t *testing.T) *githubHarness {
	t.Helper()
	gh := newGatewayHarness(t)
	gh.gw.GitHubEvents = NewGitHubEventsHandler(gh.svc, "test-webhook-secret")
	return &githubHarness{gatewayHarness: gh, secret: "test-webhook-secret"}
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
	h := newGitHubHarness(t)
	body := []byte(`{"zen":"Practicality beats purity."}`)
	rw := h.post(t, "/api/v1/events/github/t", "ping", body)
	if rw.Code != http.StatusOK {
		t.Errorf("ping = %d want 200; body=%s", rw.Code, rw.Body.String())
	}
}

func TestGitHubEvents_BadSignatureRejected(t *testing.T) {
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
	h := newGitHubHarness(t)
	g := core.Graph{
		ID: "deploy-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "github_on_push"}},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	event := map[string]any{
		"ref":    "refs/heads/main",
		"before": "abc123",
		"after":  "def456",
		"commits": []any{
			map[string]any{"id": "def456", "message": "Add feature"},
		},
		"repository": map[string]any{"full_name": "klahr/hazy-flow"},
		"pusher":     map[string]any{"name": "alice"},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/github/t", "push", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	// Wait briefly for the background fanout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "deploy-graph",
		})
		if err == nil && len(runs) > 0 {
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
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no graph-record materialized within 2s")
}

func TestGitHubEvents_PullRequestOpenedDispatches(t *testing.T) {
	h := newGitHubHarness(t)
	g := core.Graph{
		ID: "triage-graph", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "github_on_new_pr"}},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	event := map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number":   42,
			"title":    "Add fizzbuzz",
			"body":     "Fixes #1",
			"html_url": "https://github.com/klahr/hazy-flow/pull/42",
			"user":     map[string]any{"login": "alice"},
			"head":     map[string]any{"ref": "feature/fizzbuzz"},
			"base":     map[string]any{"ref": "main"},
		},
		"repository": map[string]any{"full_name": "klahr/hazy-flow"},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/github/t", "pull_request", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "triage-graph",
		})
		if err == nil && len(runs) > 0 {
			node, _ := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
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
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no graph-record materialized within 2s")
}

func TestGitHubEvents_PullRequestNonOpenedAckedNotDispatched(t *testing.T) {
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
	h := newGitHubHarness(t)
	body := []byte(`{"surprise": true}`)
	rw := h.post(t, "/api/v1/events/github/t", "release", body)
	if rw.Code != http.StatusOK {
		t.Errorf("code=%d want 200 (unknown event should ack)", rw.Code)
	}
}

func TestGitHubOnPush_StandaloneRunErrors(t *testing.T) {
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
	trans, ok := engine.Default.Get("github_on_new_pr")
	if !ok {
		t.Fatal("github_on_new_pr not registered")
	}
	res, _ := trans.Execute(t.Context(), core.Job{ID: "j"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Errorf("standalone github_on_new_pr should error with no_trigger_data, got %+v", res)
	}
}
