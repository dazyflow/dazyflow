// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// fakeWebhook captures everything a failure-notify POST sends so
// the tests can assert on the payload shape and headers.
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

// newFailureNotifyHarness wires a minimal Service capable of
// running the notifier — just Jobs + Bus, no engine/workers.
// Tests publish synthetic bus events to drive the notifier.
func newFailureNotifyHarness(t *testing.T) *Service {
	t.Helper()
	h := newGatewayHarness(t)
	// Plug in our own HTTP client + base URL so the notifier POSTs
	// to the fakeWebhook server rather than the wild internet.
	h.svc.PublicBaseURL = "https://app.example.com"
	return h.svc
}

// TestFailureNotify_BlocksPrivateWebhook pins the SSRF guard: with private
// egress off (the production default), a webhook pointed at a loopback/private
// address must NOT be dialed — so a tenant can't use a failure webhook to probe
// the host's internal network. The fake server is on loopback, so an unblocked
// notifier would POST to it; the guard must stop that.
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
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	})

	svc.startFailureNotifier(graph, runID)
	svc.bus().Publish(runID, BusEvent{Terminal: &TerminalEvent{
		JobID:  runID,
		Status: core.JobStatusFailed,
		Error:  &core.JobError{Code: "timeout", Message: "boom"},
	}})

	// The dial guard rejects the loopback address synchronously and fast; give
	// the notifier goroutine ample time to (not) deliver.
	time.Sleep(300 * time.Millisecond)
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
	// Seed a run-record so the notifier's race-recheck doesn't hit
	// ErrNotFound and bail out before it sees the terminal event.
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	})

	svc.startFailureNotifier(graph, runID)

	// Publish the terminal failure event.
	svc.bus().Publish(runID, BusEvent{Terminal: &TerminalEvent{
		JobID:  runID,
		Status: core.JobStatusFailed,
		Error:  &core.JobError{Code: "timeout", Message: "node 'enrich' exceeded 30s"},
	}})

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
	// The link carries the org, or a recipient whose browser last used a
	// different org opens it and is told the run doesn't exist.
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
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	})
	svc.startFailureNotifier(graph, runID)
	svc.bus().Publish(runID, BusEvent{Terminal: &TerminalEvent{
		JobID:  runID,
		Status: core.JobStatusSucceeded,
	}})

	// Give the notifier a moment to (not) fire.
	time.Sleep(100 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("got %d POSTs, want 0 (success shouldn't notify)", len(fw.received))
	}
}

func TestFailureNotify_NoConfigSkipsGoroutine(t *testing.T) {
	// FailureNotify nil → no watcher spawned. The check is "we
	// returned without an error and didn't even subscribe" —
	// observable as zero subscribers on the bus.
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		// FailureNotify: intentionally nil
	}
	svc.startFailureNotifier(graph, "any-run")
	// Publish — nothing should consume it.
	svc.bus().Publish("any-run", BusEvent{Terminal: &TerminalEvent{
		JobID: "any-run", Status: core.JobStatusFailed,
	}})
	time.Sleep(80 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("got %d POSTs, want 0 (no FailureNotify config)", len(fw.received))
	}
}

func TestFailureNotify_EmptyWebhookSkipsGoroutine(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: ""}, // explicit empty
	}
	svc.startFailureNotifier(graph, "any-run")
	svc.bus().Publish("any-run", BusEvent{Terminal: &TerminalEvent{
		JobID: "any-run", Status: core.JobStatusFailed,
	}})
	time.Sleep(80 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("got %d POSTs, want 0", len(fw.received))
	}
}

func TestFailureNotify_FailedNodePopulatedFromStore(t *testing.T) {
	// TerminalEvent carries the graph-level error but no failed
	// node ID. The notifier should query ListNodeRecords for the
	// failed node so the payload includes failed_node.
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-with-failed-node"
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	})
	// Pre-seed a failed node record so the notifier's lookup finds it.
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID(runID, "enrich"), Kind: core.JobKindNode,
		GraphRunID: runID, GraphID: "g", NodeID: "enrich",
		Tenant: "t", Workspace: "ws",
		Status: core.JobStatusFailed,
	})

	svc.startFailureNotifier(graph, runID)
	svc.bus().Publish(runID, BusEvent{Terminal: &TerminalEvent{
		JobID: runID, Status: core.JobStatusFailed,
		Error: &core.JobError{Code: "timeout", Message: "x"},
	}})

	fw.wait(t, 1, 2*time.Second)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	var payload FailurePayload
	_ = json.Unmarshal(fw.received[0].body, &payload)
	if payload.FailedNode != "enrich" {
		t.Errorf("failed_node = %q, want enrich", payload.FailedNode)
	}
}

func TestFailureNotify_RaceRecheckFiresIfAlreadyTerminal(t *testing.T) {
	// If the run completed before startFailureNotifier got to
	// subscribe, the notifier must read the record and fire anyway.
	// Pre-set the record to failed BEFORE starting the notifier;
	// no bus event will arrive because the worker is already done.
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-already-done"
	now := time.Now().UTC()
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		Status:     core.JobStatusFailed,
		FinishedAt: &now,
		Result: &core.Result{
			Status: core.StatusError,
			Error:  &core.JobError{Code: "boom", Message: "exploded"},
		},
	})

	svc.startFailureNotifier(graph, runID)

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
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: "run-500", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	})
	svc.startFailureNotifier(graph, "run-500")
	svc.bus().Publish("run-500", BusEvent{Terminal: &TerminalEvent{
		JobID: "run-500", Status: core.JobStatusFailed,
		Error: &core.JobError{Code: "x", Message: "y"},
	}})
	fw.wait(t, 1, 2*time.Second) // verifies the POST happened despite 500
}

// terminalToPayload unit test — exercises the helper directly so a
// regression in field mapping shows up here rather than only
// through the full notifier round trip.
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
		// Single-tenant deployments carry no tenant on the graph; the bare link
		// is still correct there.
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

// Smoke check that the package's HTTP client default works
// against a real net.Dial — guards against an accidental nil
// client breaking production while keeping unit tests fast.
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
