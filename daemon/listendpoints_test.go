// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestListModules_Filters covers listModules with query filters (q, category,
// provider, tag, include_disabled) exercising the param-collection branches.
func TestListModules_Filters(t *testing.T) {
	h := newGatewayHarness(t)

	rw := h.do(t, "GET",
		"/api/v1/drops?q=delay&category=flow_control&provider=internal&tag=timing&include_disabled=1", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("listModules = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Both legacy + new keys are emitted.
	if _, ok := resp["drops"]; !ok {
		t.Fatalf("response missing drops key: %s", rw.Body.String())
	}
	if _, ok := resp["modules"]; !ok {
		t.Fatalf("response missing modules key: %s", rw.Body.String())
	}
}

// TestListPendingApprovals_Cov covers listPendingApprovals: an empty list, an
// awaiting node with a pending_url (surfaced), and a subgraph-style awaiting
// node (filtered out).
func TestListPendingApprovals_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Empty to start.
	rw := h.do(t, "GET", "/api/v1/approvals/pending", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("pending(empty) = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}

	// A human-approval awaiting node (carries pending_url + prompt).
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run1::approve", Kind: core.JobKindNode, Tenant: "t", Workspace: "ws",
		GraphRunID: "run1", GraphID: "g", NodeID: "approve", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://app/approve/run1/approve"},
			"prompt":      {Inline: "Ship it?"},
		}},
	}); err != nil {
		t.Fatalf("seed approval node: %v", err)
	}
	// A subgraph-style awaiting node (no pending_url) — must be filtered out.
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run1::sub", Kind: core.JobKindNode, Tenant: "t", Workspace: "ws",
		GraphRunID: "run1", GraphID: "g", NodeID: "sub", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_child_graph_id": {Inline: "child"},
		}},
	}); err != nil {
		t.Fatalf("seed subgraph node: %v", err)
	}

	rw = h.do(t, "GET", "/api/v1/approvals/pending", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("pending = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Approvals []PendingApproval `json:"approvals"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Approvals) != 1 {
		t.Fatalf("approvals = %d, want 1 (subgraph filtered out): %s", len(resp.Approvals), rw.Body.String())
	}
	if resp.Approvals[0].NodeID != "approve" || resp.Approvals[0].Prompt != "Ship it?" {
		t.Fatalf("approval = %+v", resp.Approvals[0])
	}
}
