// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// TestForEach_E2E_WebhookToIteration drives the full unlock end-to-end:
// a webhook delivers a JSON array, the body is seeded straight into a
// for_each node, and a step runs once per item.
func TestForEach_E2E_WebhookToIteration(t *testing.T) {
	_, wh, jobs, _, wsStore := startWebhookHarnessLocal(t)

	g := core.Graph{
		ID: "fe-iter", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}},
			{ID: "iter", Module: "for_each", Params: map[string]any{"concurrency": 3}},
			// The loop body: one delay step per item, wired to the body pin.
			{ID: "step", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "inbound", FromPort: "body", To: "iter", ToPort: "items"},
			{From: "iter", FromPort: "body", To: "step", ToPort: "pass"},
		},
	}
	savePublished(t, wsStore, g)

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeWebhookForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := []byte(`["alpha","beta","gamma","delta","epsilon"]`)
	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/fe-iter", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Content-Type", "application/json")
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

	terminal := waitForFire(t, jobs, out.JobID)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status=%q, want succeeded", terminal)
	}

	iter, _ := jobs.Get(t.Context(), daemon.NodeJobID(out.JobID, "iter"))
	if iter.Result == nil {
		t.Fatal("iter has no result")
	}
	results, ok := iter.Result.Output["results"].Inline.([]core.Ref)
	if !ok {
		t.Fatalf("results inline is %T", iter.Result.Output["results"].Inline)
	}
	if len(results) != 5 {
		t.Fatalf("got %d results, want 5", len(results))
	}
	for i, r := range results {
		payload, ok := r.Inline.(map[string]any)
		if !ok {
			t.Fatalf("results[%d].Inline = %T", i, r.Inline)
		}
		if payload["status"] != core.StatusOK {
			t.Errorf("results[%d].status = %v, want ok", i, payload["status"])
		}
	}
	errs, _ := iter.Result.Output["errors"].Inline.(map[string]any)
	if len(errs) != 0 {
		t.Errorf("errors = %+v, want empty", errs)
	}
}

// TestForEach_E2E_PerItemHTTPWithTemplatedURL exercises the realistic
// shape: webhook delivers a list of records, for_each runs http_request
// once per record with ${item.id} in the URL and ${builtin.KEY} in the
// Authorization header on the wired body node. Proves that:
//   - Secret substitution reaches the body node's params.
//   - Item substitution runs per-iteration against the current row.
//   - Both kinds of placeholder compose cleanly.
func TestForEach_E2E_PerItemHTTPWithTemplatedURL(t *testing.T) {
	tokens := daemon.NewBuiltinProvider()
	tokens.Set("UPSTREAM_TOKEN", "real-secret-9000")

	// Mock backend captures (path, auth) per request.
	type hit struct {
		path string
		auth string
	}
	var mu sync.Mutex
	var hits []hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, r.Header.Get("Authorization")})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// Build a full dzd-equivalent stack: workspace, job store, engine
	// configured with the builtin secret provider, worker.
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets:  map[string]core.SecretProvider{"builtin": tokens},
	}
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

	wh := daemon.NewWebhookListener(svc)

	g := core.Graph{
		ID: "fe-http", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "inbound", Module: "webhook_input", Params: map[string]any{"secrets": []any{"s"}}},
			{ID: "fan", Module: "for_each", Params: map[string]any{"concurrency": 2}},
			// The loop body: one http_request per item. ${item.id} is resolved
			// per iteration by the engine; ${builtin.…} by the secret provider.
			{ID: "call", Module: "http_request", Params: map[string]any{
				"url":    srv.URL + "/items/${item.id}",
				"method": "GET",
				"headers": map[string]any{
					"Authorization": "Bearer ${builtin.UPSTREAM_TOKEN}",
				},
				"allow_private_networks": true,
			}},
		},
		Edges: []core.Edge{
			{From: "inbound", FromPort: "body", To: "fan", ToPort: "items"},
			{From: "fan", FromPort: "body", To: "call", ToPort: "pass"},
		},
	}
	savePublished(t, wsStore, g)

	mux := http.NewServeMux()
	mux.HandleFunc("/trigger/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeWebhookForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := []byte(`[
		{"id":"u-1"},
		{"id":"u-2"},
		{"id":"u-3"}
	]`)
	req, _ := http.NewRequest("POST", ts.URL+"/trigger/acme/ws1/fe-http", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)

	terminal := waitForFire(t, jobs, out.JobID)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", terminal)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 3 {
		t.Fatalf("got %d backend hits, want 3", len(hits))
	}
	paths := []string{hits[0].path, hits[1].path, hits[2].path}
	sort.Strings(paths)
	wantPaths := []string{"/items/u-1", "/items/u-2", "/items/u-3"}
	for i, want := range wantPaths {
		if paths[i] != want {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want)
		}
	}
	for _, h := range hits {
		if h.auth != "Bearer real-secret-9000" {
			t.Errorf("Authorization = %q, want Bearer real-secret-9000", h.auth)
		}
	}
}
