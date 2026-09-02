// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestResumeRunMe_ErrorLegs covers resumeRun's not-found and conflict legs via
// the /me/runs/{id}/resume route.
func TestResumeRunMe_ErrorLegs(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	// Unknown run -> 404.
	if rw := h.do(t, "POST", "/api/v1/me/runs/ghost/resume", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("resume(ghost) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	// A running (not paused) run -> 409 conflict (not paused).
	g := core.Graph{ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}}}
	payload, _ := json.Marshal(g)
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-running", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if rw := h.do(t, "POST", "/api/v1/me/runs/run-running/resume", nil); rw.Code != http.StatusConflict {
		t.Fatalf("resume(not paused) = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}
}

// TestRetryRunMe_Cov covers retryRunMe: the not-found leg, the conflict leg
// (a still-running run isn't retryable), and the happy path on a failed run.
func TestRetryRunMe_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	// Unknown run -> 404.
	if rw := h.do(t, "POST", "/api/v1/me/runs/ghost/retry", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("retry(ghost) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	g := core.Graph{ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}}}
	payload, _ := json.Marshal(g)

	// A running run isn't retryable -> 409.
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-live", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if rw := h.do(t, "POST", "/api/v1/me/runs/run-live/retry", nil); rw.Code != http.StatusConflict {
		t.Fatalf("retry(running) = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}

	// A failed run is retryable -> 202 with a new job id. Seed the failed
	// graph record and one succeeded node (inline output, so it's reused).
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-failed", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusFailed, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	rw := h.do(t, "POST", "/api/v1/me/runs/run-failed/retry", nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("retry(failed) = %d, want 202; body=%s", rw.Code, rw.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp["job_id"] == "" {
		t.Fatalf("retry response missing job_id: %s", rw.Body.String())
	}
}
