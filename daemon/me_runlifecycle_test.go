// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestResumeRunMe_ErrorLegs(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	if rw := h.do(t, "POST", "/api/v1/me/runs/ghost/resume", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("resume(ghost) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

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

func TestRetryRunMe_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	if rw := h.do(t, "POST", "/api/v1/me/runs/ghost/retry", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("retry(ghost) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	g := core.Graph{ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}}}
	payload, _ := json.Marshal(g)

	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-live", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if rw := h.do(t, "POST", "/api/v1/me/runs/run-live/retry", nil); rw.Code != http.StatusConflict {
		t.Fatalf("retry(running) = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}

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
