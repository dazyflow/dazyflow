// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

type fakeWebhook struct {
	server   *httptest.Server
	mu       sync.Mutex
	received []capturedPost
	respCode int
}

type capturedPost struct {
	body []byte
	ct   string
}

func newFakeWebhook(t *testing.T) *fakeWebhook {
	t.Helper()
	fw := &fakeWebhook{respCode: 200}
	fw.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fw.mu.Lock()
		fw.received = append(fw.received, capturedPost{
			body: body, ct: r.Header.Get("Content-Type"),
		})
		code := fw.respCode
		fw.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(fw.server.Close)
	return fw
}

func (fw *fakeWebhook) wait(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fw.mu.Lock()
		got := len(fw.received)
		fw.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fw.mu.Lock()
	got := len(fw.received)
	fw.mu.Unlock()
	t.Fatalf("waited %s for %d POSTs, got %d", timeout, n, got)
}

func newFailureNotifyHarness(t *testing.T) *Service {
	t.Helper()
	h := newGatewayHarness(t)
	h.svc.PublicBaseURL = "https://app.example.com"
	return h.svc
}

// Pins the SSRF guard: with private egress off (the production default), a
// webhook pointed at a loopback/private address must NOT be dialed — so a
// tenant can't use a failure webhook to probe the host's internal network. The
// fake server is on loopback, so an unblocked notifier would POST to it; the
// guard must stop that.
func TestFailureNotify_BlocksPrivateWebhook(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)      // production default
	defer hfnet.SetAllowPrivateEgress(true) // restore the package default
	fw := newFakeWebhook(t)                 // listens on 127.0.0.1
	svc := newFailureNotifyHarness(t)

	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-ssrf"
	terminateAndSweep(t, svc, graph, runID, core.JobStatusFailed,
		&core.JobError{Code: "timeout", Message: "boom"})
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Fatalf("SSRF-blocked webhook still received %d POST(s)", len(fw.received))
	}
}

func TestFailureNotify_FiresOnFailedTerminal(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)

	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-1"
	terminateAndSweep(t, svc, graph, runID, core.JobStatusFailed,
		&core.JobError{Code: "timeout", Message: "node 'enrich' exceeded 30s"})

	fw.wait(t, 1, 2*time.Second)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 1 {
		t.Fatalf("got %d POSTs, want 1", len(fw.received))
	}
	got := fw.received[0]
	if got.ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", got.ct)
	}
	var payload FailurePayload
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.GraphID != "g" || payload.RunID != runID || payload.Tenant != "t" || payload.Workspace != "ws" {
		t.Errorf("missing scope fields: %+v", payload)
	}
	if payload.ErrorCode != "timeout" || payload.ErrorMessage != "node 'enrich' exceeded 30s" {
		t.Errorf("missing error fields: %+v", payload)
	}
	if payload.RunURL != "https://app.example.com/runs/run-1?org=t" {
		t.Errorf("run_url = %q", payload.RunURL)
	}
}

func TestFailureNotify_DoesNotFireOnSuccess(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)

	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-ok"
	terminateAndSweep(t, svc, graph, runID, core.JobStatusSucceeded, nil)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("got %d POSTs, want 0 (success shouldn't notify)", len(fw.received))
	}
}

func TestFailureNotify_NoConfigSendsNothing(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
	}
	terminateAndSweep(t, svc, graph, "any-run", core.JobStatusFailed,
		&core.JobError{Code: "boom"})
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("got %d POSTs, want 0 (no FailureNotify config)", len(fw.received))
	}
}

func TestFailureNotify_EmptyWebhookSendsNothing(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: ""}, // explicit empty
	}
	terminateAndSweep(t, svc, graph, "any-run", core.JobStatusFailed,
		&core.JobError{Code: "boom"})
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("got %d POSTs, want 0", len(fw.received))
	}
}

func TestFailureNotify_FailedNodePopulatedFromStore(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-with-failed-node"
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID(runID, "enrich"), Kind: core.JobKindNode,
		GraphRunID: runID, GraphID: "g", NodeID: "enrich",
		Tenant: "t", Workspace: "ws",
		Status: core.JobStatusFailed,
	})
	terminateAndSweep(t, svc, graph, runID, core.JobStatusFailed,
		&core.JobError{Code: "timeout", Message: "x"})

	fw.wait(t, 1, 2*time.Second)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	var payload FailurePayload
	_ = json.Unmarshal(fw.received[0].body, &payload)
	if payload.FailedNode != "enrich" {
		t.Errorf("failed_node = %q, want enrich", payload.FailedNode)
	}
}

// A run that was already terminal before anything looked at it — the shape of
// every failure a restart used to swallow, since the watcher that would have
// heard the terminal event died with the process that armed it. The sweep
// reads the store, so it does not care that nobody was listening.
func TestFailureNotify_FiresForARunNobodyWasWatching(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-already-done"
	terminateAndSweep(t, svc, graph, runID, core.JobStatusFailed,
		&core.JobError{Code: "boom", Message: "exploded"})

	fw.wait(t, 1, 2*time.Second)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	var payload FailurePayload
	_ = json.Unmarshal(fw.received[0].body, &payload)
	if payload.ErrorCode != "boom" || payload.ErrorMessage != "exploded" {
		t.Errorf("race-recheck payload missing error: %+v", payload)
	}
}

func TestFailureNotify_NonSuccessWebhookDoesNotPanic(t *testing.T) {
	// Webhook returns 500 — notifier must log and move on, not
	// crash the daemon. Test passes if the goroutine completes
	// without leaking or panicking.
	fw := newFakeWebhook(t)
	fw.respCode = 500
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	terminateAndSweep(t, svc, graph, "run-500", core.JobStatusFailed,
		&core.JobError{Code: "x", Message: "y"})
	fw.wait(t, 1, 2*time.Second) // verifies the POST happened despite 500
}

func TestFailureNotify_TerminalToPayloadShape(t *testing.T) {
	graph := core.Graph{ID: "g", Tenant: "acme", Workspace: "main"}
	got := terminalToPayload(graph, "r1", &TerminalEvent{
		Status: core.JobStatusFailed,
		Error:  &core.JobError{Code: "c", Message: "m"},
	}, "https://app.example.com")
	if got.GraphID != "g" || got.RunID != "r1" || got.Tenant != "acme" {
		t.Errorf("scope = %+v", got)
	}
	if got.ErrorCode != "c" || got.ErrorMessage != "m" {
		t.Errorf("error = %+v", got)
	}
	if got.RunURL != "https://app.example.com/runs/r1?org=acme" {
		t.Errorf("run_url = %q", got.RunURL)
	}
	if got.FinishedAt == "" {
		t.Error("finished_at not stamped")
	}
}

func TestBuildRunURL(t *testing.T) {
	cases := []struct {
		name          string
		base          string
		tenant, runID string
		want          string
	}{
		{"with org", "https://app.example.com", "acme", "r1",
			"https://app.example.com/runs/r1?org=acme"},
		{"trailing slash trimmed", "https://app.example.com/", "acme", "r1",
			"https://app.example.com/runs/r1?org=acme"},
		// A tenant id with URL-significant characters must not be able to graft
		// extra params onto the link.
		{"tenant escaped", "https://app.example.com", "a&b=c d", "r1",
			"https://app.example.com/runs/r1?org=a%26b%3Dc+d"},
		{"no tenant", "https://app.example.com", "", "r1",
			"https://app.example.com/runs/r1"},
		{"no base", "", "acme", "r1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := buildRunURL(c.base, c.tenant, c.runID); got != c.want {
				t.Errorf("buildRunURL(%q,%q,%q) = %q, want %q",
					c.base, c.tenant, c.runID, got, c.want)
			}
		})
	}
}

func TestFailureNotify_NoPublicBaseURLOmitsRunURL(t *testing.T) {
	got := terminalToPayload(
		core.Graph{ID: "g", Tenant: "t", Workspace: "w"},
		"r1",
		&TerminalEvent{Status: core.JobStatusFailed},
		"", // no base URL configured
	)
	if got.RunURL != "" {
		t.Errorf("run_url = %q, want empty when PublicBaseURL is unset", got.RunURL)
	}
}

func TestFailureNotify_DefaultClientPostsRealJSON(t *testing.T) {
	var got bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&got, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	svc := newFailureNotifyHarness(t)
	svc.fireFailureNotification(context.Background(),
		core.Graph{
			ID: "g", Tenant: "t", Workspace: "w",
			FailureNotify: &core.FailureNotify{Webhook: srv.URL},
		},
		FailurePayload{GraphID: "g", RunID: "r"},
	)
	if !bytes.Contains(got.Bytes(), []byte(`"graph_id":"g"`)) {
		t.Errorf("missing graph_id: %s", got.String())
	}
}

// terminateAndSweep is the sweep-era replacement for "arm a watcher, then
// publish a terminal event": it puts the run in the store in the state the
// dispatcher would have left it, then runs one sweep pass. Notification now
// reads the store rather than listening, so this is what drives it.
func terminateAndSweep(t *testing.T, svc *Service, g core.Graph, runID string, status core.JobStatus, jerr *core.JobError) {
	t.Helper()
	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	if _, err := svc.Jobs.Get(t.Context(), runID); err != nil {
		if err := svc.Jobs.Enqueue(t.Context(), core.JobRecord{
			ID: runID, Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
			Tenant: g.Tenant, Workspace: g.Workspace,
			Status: core.JobStatusRunning, GraphPayload: payload,
		}); err != nil {
			t.Fatalf("enqueue run: %v", err)
		}
	}
	res := &core.Result{JobID: runID, Status: core.StatusOK}
	if jerr != nil {
		res.Status, res.Error = core.StatusError, jerr
	}
	if err := svc.Jobs.Complete(t.Context(), runID, status, res); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	svc.SweepFailureNotifications(t.Context())
}
