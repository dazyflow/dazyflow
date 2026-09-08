// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestApproveAuthed_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	if rw := h.do(t, "POST", "/api/v1/approvals/ghost/n?decision=approve", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("approve(ghost) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	// Seed a graph run owned by tenant "t" with a node that is NOT awaiting.
	g := core.Graph{ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}}}
	payload, _ := json.Marshal(g)
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-appr", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	if rw := h.do(t, "POST", "/api/v1/approvals/run-appr/n?decision=maybe", nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("approve(bad decision) = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	rw := h.do(t, "POST", "/api/v1/approvals/run-appr/n?decision=approve", nil)
	if rw.Code != http.StatusConflict && rw.Code != http.StatusNotFound {
		t.Fatalf("approve(not awaiting) = %d, want 409/404; body=%s", rw.Code, rw.Body.String())
	}
}

func TestCancelRunMe_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	if rw := h.do(t, "POST", "/api/v1/me/runs/ghost/cancel", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("cancel(ghost) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	g := core.Graph{ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}}}
	payload, _ := json.Marshal(g)
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-cancel", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	rw := h.do(t, "POST", "/api/v1/me/runs/run-cancel/cancel", map[string]any{"reason": "user requested"})
	if rw.Code != http.StatusOK {
		t.Fatalf("cancel(running) = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}

	rw = h.do(t, "POST", "/api/v1/me/runs/run-cancel/cancel", nil)
	if rw.Code != http.StatusConflict {
		t.Fatalf("cancel(already cancelled) = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}
}
