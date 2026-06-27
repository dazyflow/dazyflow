// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// TestWebhookBody_E2E_JSONPropagation drives the full pipeline: an
// inbound POST with a JSON body fires a graph whose webhook_input node
// is pre-completed by the trigger handler, and a downstream branch
// routes on a field inside the body.
func TestWebhookBody_E2E_JSONPropagation(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarnessLocal(t)

	g := core.Graph{
		ID: "wh-body-flow", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input", Params: map[string]any{"secrets": []any{"wh-secret"}}},
			// Compare reads the priority field out of A (the JSON body) and
			// tests it against "high" (B), emitting 1/0; Branch routes on it.
			{ID: "check", Module: "compare", Params: map[string]any{
				"field": "priority", "op": "equals", "B": "high",
			}},
			{ID: "decide", Module: "branch"},
			{ID: "page", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "queue", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "inbound", FromPort: "body", To: "check", ToPort: "A"},
			{From: "check", FromPort: "result", To: "decide", ToPort: "condition"},
			{From: "inbound", FromPort: "body", To: "decide", ToPort: "in"},
			{From: "decide", FromPort: "then", To: "page", ToPort: "in"},
			{From: "decide", FromPort: "else", To: "queue", ToPort: "in"},
		},
	}
	if _, err := wsStore.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeWebhookForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := []byte(`{"priority":"high","alert":"db down"}`)
	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/wh-body-flow",
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wh-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom-Header", "from-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.JobID == "" {
		t.Fatal("no job_id")
	}

	terminal := waitForFire(t, jobs, out.JobID)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status=%q", terminal)
	}

	// Assertions
	inbound, _ := jobs.Get(t.Context(), daemon.NodeJobID(out.JobID, "inbound"))
	if inbound.Status != core.JobStatusSucceeded {
		t.Errorf("inbound = %q", inbound.Status)
	}
	if inbound.Result == nil {
		t.Fatal("inbound has no result")
	}
	// Inbound's body output should be the parsed JSON object
	bodyOut := inbound.Result.Output["body"]
	parsed, ok := bodyOut.Inline.(map[string]any)
	if !ok {
		t.Fatalf("body Inline is %T, want map", bodyOut.Inline)
	}
	if parsed["priority"] != "high" {
		t.Errorf("priority = %v, want high", parsed["priority"])
	}
	if parsed["alert"] != "db down" {
		t.Errorf("alert = %v", parsed["alert"])
	}

	// Headers should round-trip too
	headersOut := inbound.Result.Output["headers"]
	hmap, _ := headersOut.Inline.(map[string]any)
	if hmap["X-Custom-Header"] != "from-test" {
		t.Errorf("headers = %+v", hmap)
	}

	// Branch routed correctly on body.priority == "high"
	pageRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(out.JobID, "page"))
	queueRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(out.JobID, "queue"))
	if pageRec.Status != core.JobStatusSucceeded {
		t.Errorf("page status = %q, want succeeded (priority=high → then path)", pageRec.Status)
	}
	if queueRec.Status != core.JobStatusSkipped {
		t.Errorf("queue status = %q, want skipped", queueRec.Status)
	}
}

func TestWebhookBody_E2E_TextBody(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarnessLocal(t)

	// Single-node graph — entirely satisfied by the seed, so it
	// completes synchronously inside SubmitGraphWithSeed.
	g := core.Graph{
		ID: "wh-text", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}},
		},
	}
	_, _ = wsStore.Save(g, "test")

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeWebhookForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/wh-text",
		bytes.NewReader([]byte("hello plain text")))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)

	// Single-node seeded graph is terminal by the time POST returns;
	// no need to wait on the bus.
	graphRec, err := jobs.Get(t.Context(), out.JobID)
	if err != nil {
		t.Fatalf("Get graph: %v", err)
	}
	if graphRec.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded", graphRec.Status)
	}

	rec, _ := jobs.Get(t.Context(), daemon.NodeJobID(out.JobID, "inbound"))
	if rec.Result == nil || rec.Result.Output["body"].Inline != "hello plain text" {
		t.Errorf("body = %+v", rec.Result)
	}
}

func TestWebhookBody_E2E_ManualRunFails(t *testing.T) {
	// A graph with webhook_input that's submitted manually (not via
	// webhook) must fail the webhook_input node with no_trigger_data.
	svc, _, jobs, _, wsStore := startWebhookHarnessLocal(t)

	g := core.Graph{
		ID: "wh-manual", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input", Params: map[string]any{"secrets": []any{"x"}}},
		},
	}
	_, _ = wsStore.Save(g, "test")

	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	p := core.Principal{Tenant: "acme", Workspace: "ws1", Roles: []core.Role{role}}

	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}
	terminal := waitForFire(t, jobs, runID)
	if terminal != core.JobStatusFailed {
		t.Fatalf("status=%q, want failed", terminal)
	}
	rec, _ := jobs.Get(t.Context(), daemon.NodeJobID(runID, "inbound"))
	if rec.Result == nil || rec.Result.Error == nil || rec.Result.Error.Code != "no_trigger_data" {
		t.Errorf("error = %+v", rec.Result)
	}
}

// startWebhookHarnessLocal mirrors the startWebhookHarness pattern from
// daemon/webhook_test.go but lives in this e2e package so the test
// imports stay clean.
func startWebhookHarnessLocal(t *testing.T) (*daemon.Service, *daemon.WebhookListener, core.JobStore, *daemon.MemoryBus, *workspace.Store) {
	t.Helper()
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()
	return svc, daemon.NewWebhookListener(svc), jobs, bus, wsStore
}
