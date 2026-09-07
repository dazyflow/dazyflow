// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

// callPost posts to a flow's /call address and returns the response.
func callPost(t *testing.T, wh *daemon.WebhookListener, id, secret, query string) (int, string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/call/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeCallForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/call/acme/ws1/"+id+query,
		bytes.NewReader([]byte(`{"name":"Ada"}`)))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header.Get("Content-Type")
}

func requestNode(id string) core.Node {
	return core.Node{ID: id, Module: "request_input", Params: map[string]any{"secrets": []any{"s3cr3t"}}}
}

// The headline contract: a Reply step's value is what the caller gets back,
// under the status the step declares.
func TestCall_ReplyAnswersCaller(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-ok", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "out", Module: "reply", Params: map[string]any{
				"body": "hello ${trigger.body.name}", "status_code": 201,
			}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: "pass"}},
	})
	code, body, ctype := callPost(t, wh, "call-ok", "s3cr3t", "")
	if code != 201 {
		t.Fatalf("status = %d, want 201; body=%s", code, body)
	}
	if body != "hello Ada" {
		t.Errorf("body = %q, want %q", body, "hello Ada")
	}
	if !strings.HasPrefix(ctype, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ctype)
	}
}

// A record wired into Body goes back as JSON — the API-shaped case.
func TestCall_WiredValueRepliesAsJSON(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-json", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "out", Module: "reply"},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: "body"}},
	})
	code, body, ctype := callPost(t, wh, "call-json", "s3cr3t", "")
	if code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	if !strings.Contains(ctype, "json") {
		t.Errorf("content-type = %q, want JSON", ctype)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	if got["name"] != "Ada" {
		t.Errorf("body = %v, want the posted record echoed", got)
	}
}

// Reply answers and the flow carries on — the acknowledge-then-work shape a
// caller with a short timeout needs.
func TestCall_FlowContinuesPastReply(t *testing.T) {
	t.Parallel()
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-cont", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "out", Module: "reply", Params: map[string]any{"body": "ack"}},
			{ID: "after", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "in", FromPort: "body", To: "out", ToPort: "pass"},
			{From: "out", FromPort: "pass", To: "after", ToPort: "pass"},
		},
	})
	code, body, _ := callPost(t, wh, "call-cont", "s3cr3t", "")
	if code != 200 || body != "ack" {
		t.Fatalf("status = %d body = %q, want 200 \"ack\"", code, body)
	}
	waitFor(t, 3*time.Second, func() bool {
		recs, err := jobs.ListNodeRecords(t.Context(), core.ListNodeRecordsOpts{Limit: 100})
		if err != nil {
			return false
		}
		for _, rec := range recs {
			if rec.NodeID == "after" && rec.Status == core.JobStatusSucceeded {
				return true
			}
		}
		return false
	}, "the step after Reply never ran")
}

// A flow that never reaches a Reply still tells the caller what happened.
func TestCall_NoReplyReturnsRunStatus(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-none", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "a", ToPort: "pass"}},
	})
	code, body, _ := callPost(t, wh, "call-none", "s3cr3t", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q is not JSON: %v", body, err)
	}
	if got["status"] != string(core.JobStatusSucceeded) || got["run_id"] == "" {
		t.Errorf("body = %v, want the run's id and status", got)
	}
}

// ?wait=0 is the opt-out for a caller that must not block.
func TestCall_WaitZeroReturnsImmediately(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-async", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "out", Module: "reply", Params: map[string]any{"body": "ack"}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: "pass"}},
	})
	code, body, _ := callPost(t, wh, "call-async", "s3cr3t", "?wait=0")
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", code, body)
	}
	if !strings.Contains(body, "run_id") {
		t.Errorf("body = %s, want a run_id to follow", body)
	}
}

// The endpoint is key-guarded, and says nothing about which flows exist.
func TestCall_RejectsBadKey(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-auth", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{requestNode("in"), {ID: "out", Module: "reply"}},
	})
	for _, key := range []string{"", "wrong"} {
		code, body, _ := callPost(t, wh, "call-auth", key, "")
		if code != http.StatusUnauthorized {
			t.Errorf("key %q: status = %d, want 401; body=%s", key, code, body)
		}
	}
	// An unknown flow answers identically — no existence oracle.
	code, _, _ := callPost(t, wh, "no-such-flow", "s3cr3t", "")
	if code != http.StatusUnauthorized {
		t.Errorf("unknown flow: status = %d, want 401", code)
	}
}

// waitFor polls cond until it holds or the budget runs out.
func waitFor(t *testing.T, budget time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// callPostKeyed posts with an Idempotency-Key and returns the status, body and
// the Idempotency-Replay header.
func callPostKeyed(t *testing.T, wh *daemon.WebhookListener, id, query, key, body string) (int, string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/call/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeCallForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/call/acme/ws1/"+id+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer s3cr3t")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(got), resp.Header.Get("Idempotency-Replay")
}

func runCount(t *testing.T, jobs core.JobStore, graphID string) int {
	t.Helper()
	runs, err := jobs.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{GraphID: graphID, Limit: 100})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	return len(runs)
}

func idemGraph(id string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "out", Module: "reply", Params: map[string]any{"body": "answer"}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: "pass"}},
	}
}

// The headline: a retry carrying the same key replays the first answer and
// does NOT run the flow a second time.
func TestCall_IdempotencyKeyReplaysWithoutRerunning(t *testing.T) {
	t.Parallel()
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, idemGraph("idem-replay"))

	code, body, replay := callPostKeyed(t, wh, "idem-replay", "", "k-1", `{"n":1}`)
	if code != 200 || body != "answer" {
		t.Fatalf("first call: %d %q", code, body)
	}
	if replay == "true" {
		t.Errorf("first call reported as a replay")
	}
	code, body, replay = callPostKeyed(t, wh, "idem-replay", "", "k-1", `{"n":1}`)
	if code != 200 || body != "answer" {
		t.Fatalf("retry: %d %q, want the first answer", code, body)
	}
	if replay != "true" {
		t.Errorf("Idempotency-Replay = %q, want true", replay)
	}
	if n := runCount(t, jobs, "idem-replay"); n != 1 {
		t.Errorf("%d runs, want 1 — the retry started another", n)
	}
}

// The case the feature exists for: the first caller gave up before the answer
// (here, ?wait=0), and the retry JOINS that run rather than starting a second.
func TestCall_IdempotencyKeyJoinsTheRunInFlight(t *testing.T) {
	t.Parallel()
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, idemGraph("idem-join"))

	code, _, _ := callPostKeyed(t, wh, "idem-join", "?wait=0", "k-2", `{"n":1}`)
	if code != http.StatusAccepted {
		t.Fatalf("first call: %d, want 202", code)
	}
	code, body, replay := callPostKeyed(t, wh, "idem-join", "", "k-2", `{"n":1}`)
	if code != 200 || body != "answer" {
		t.Fatalf("retry: %d %q, want the joined run's answer", code, body)
	}
	if replay != "true" {
		t.Errorf("Idempotency-Replay = %q, want true", replay)
	}
	if n := runCount(t, jobs, "idem-join"); n != 1 {
		t.Errorf("%d runs, want 1 — the retry started another", n)
	}
}

// Reusing a key with a different payload is misuse: replaying the first answer
// would hide it.
func TestCall_IdempotencyKeyReusedWithDifferentBody(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, idemGraph("idem-reuse"))

	if code, body, _ := callPostKeyed(t, wh, "idem-reuse", "", "k-3", `{"n":1}`); code != 200 {
		t.Fatalf("first call: %d %q", code, body)
	}
	code, _, replay := callPostKeyed(t, wh, "idem-reuse", "", "k-3", `{"n":2}`)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", code)
	}
	if replay == "true" {
		t.Errorf("a refused reuse must not report itself as a replay")
	}
}

// Without a key the endpoint stays at-least-once — the caller opted out.
func TestCall_WithoutKeyEachCallRuns(t *testing.T) {
	t.Parallel()
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, idemGraph("idem-none"))

	for i := 0; i < 2; i++ {
		if code, body, _ := callPostKeyed(t, wh, "idem-none", "", "", `{"n":1}`); code != 200 {
			t.Fatalf("call %d: %d %q", i, code, body)
		}
	}
	if n := runCount(t, jobs, "idem-none"); n != 2 {
		t.Errorf("%d runs, want 2", n)
	}
}

// A Reply on a Form flow becomes the page the person who submitted it sees —
// the same step that answers an API caller, on the product's least technical
// surface.
func TestForm_ReplyIsNotRequiredForTheFormToWork(t *testing.T) {
	t.Parallel()
	_, wh, jobs, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "form-reply", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "form_input", Params: map[string]any{
				"form_fields": []any{"name"},
			}},
			{ID: "out", Module: "reply", Params: map[string]any{"body": "Thanks ${trigger.body.name}"}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: "pass"}},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/form/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeFormForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	resp, err := http.PostForm(ts.URL+"/form/acme/ws1/form-reply", url.Values{"name": {"Ada"}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	// The submission started a run, and the Reply step recorded what it would
	// have sent — a form needs no key and no Reply to work.
	waitFor(t, 3*time.Second, func() bool {
		recs, err := jobs.ListNodeRecords(t.Context(), core.ListNodeRecordsOpts{Limit: 100})
		if err != nil {
			return false
		}
		for _, rec := range recs {
			if rec.NodeID != "out" || rec.Status != core.JobStatusSucceeded || rec.Result == nil {
				continue
			}
			if ref, ok := rec.Result.Output["body"]; ok && ref.Inline == "Thanks Ada" {
				return true
			}
		}
		return false
	}, "the form submission never reached the Reply step")
}

// Same story as the Webhook step: a caller that can only be given a URL has
// nowhere to put a header, so /call reads the key from the query string too.
func TestCall_AcceptsKeyInTheURL(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-url-key", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			requestNode("in"),
			{ID: "out", Module: "reply", Params: map[string]any{"body": "hi"}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: core.PassPort}},
	})

	code, body, _ := callPost(t, wh, "call-url-key", "", "?key=s3cr3t")
	if code != http.StatusOK {
		t.Fatalf("key in the URL: status=%d, want 200; body=%s", code, body)
	}
	// A wrong key in the URL is still a stranger.
	if code, _, _ := callPost(t, wh, "call-url-key", "", "?key=wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong key in the URL: status=%d, want 401", code)
	}
}

// The header is what gets checked when both are present, so a caller that can
// set one is never admitted by a stale key left in a URL.
func TestCall_HeaderWinsOverURLKey(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-both", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{requestNode("in"), {ID: "out", Module: "reply"}},
	})
	if code, _, _ := callPost(t, wh, "call-both", "stale", "?key=s3cr3t"); code != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401 — the header is the one that counts", code)
	}
}

// A caller that can carry neither. The author opens the step and the address
// becomes the credential — a heavier decision here than on /trigger, because
// this endpoint hands back the flow's Reply.
func TestCall_PublicStepAnswersWithNoKey(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-public", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "request_input", Params: map[string]any{"public": true}},
			{ID: "out", Module: "reply", Params: map[string]any{"body": "open"}},
		},
		Edges: []core.Edge{{From: "in", FromPort: "body", To: "out", ToPort: core.PassPort}},
	})
	code, body, _ := callPost(t, wh, "call-public", "", "")
	if code != http.StatusOK {
		t.Fatalf("status=%d, want 200; body=%s", code, body)
	}
	if !strings.Contains(body, "open") {
		t.Errorf("body=%q, want the Reply value", body)
	}
}

// And without the switch it stays shut, so a half-built Request step never
// publishes its Reply to the internet by omission.
func TestCall_KeylessStepIsInertUnlessPublic(t *testing.T) {
	t.Parallel()
	_, wh, _, _, wsStore := startWebhookHarness(t)
	savePublished(t, wsStore, core.Graph{
		ID: "call-keyless", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "in", Module: "request_input", Params: map[string]any{}},
			{ID: "out", Module: "reply"},
		},
	})
	if code, _, _ := callPost(t, wh, "call-keyless", "", ""); code != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", code)
	}
}
