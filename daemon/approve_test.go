// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// approveAuthed: the authenticated approval endpoint. Approving a run that
// doesn't exist (or isn't in the caller's tenant) is rejected before the
// service Approve call.

func TestApproveAuthed_UnknownRun(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/approvals/ghostrun/nodeA", nil)
	if rw.Code != http.StatusNotFound && rw.Code != http.StatusForbidden {
		t.Fatalf("approve unknown run = %d (%s), want 404/403", rw.Code, rw.Body.String())
	}
}

func TestApproveAuthed_UnknownRunWithDecision(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/approvals/ghostrun/nodeA?decision=reject", nil)
	if rw.Code != http.StatusNotFound && rw.Code != http.StatusForbidden {
		t.Fatalf("approve(reject) unknown run = %d (%s), want 404/403", rw.Code, rw.Body.String())
	}
}

// Noninteractive approval is opt-in. The endpoint accepts any key holding
// workspace membership, so without this a key minted to RUN a flow could also
// wave through the gate that exists to stop it — the step has to say a machine
// may decide it.
//
// seedParkedApproval stages a run parked on one await_approval node, with the
// step's params as given, and returns the run id.
func seedParkedApproval(t *testing.T, h *gatewayHarness, runID string, params map[string]any) string {
	t.Helper()
	g := core.Graph{ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "gate", Module: core.ApprovalModuleID, Params: params}}}
	payload, _ := json.Marshal(g)
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	// Parked exactly as the module leaves it: the marker URL plus the prompt.
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID(runID, "gate"), Kind: core.JobKindNode,
		GraphRunID: runID, GraphID: "g", NodeID: "gate",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {MIME: "text/plain", Inline: "https://example.test/approve/x/y"},
		}},
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return runID
}

func TestApproveAuthed_APIKeyRefusedUnlessStepOptsIn(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	// No allow_api: a person's gate.
	seedParkedApproval(t, h, "run-human", map[string]any{"prompt": "Refund $230?"})

	rw := h.do(t, "POST", "/api/v1/approvals/run-human/gate?decision=approve", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("api key approve = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}
	// The message has to name the switch, or the caller cannot act on it.
	if !strings.Contains(rw.Body.String(), "API key") {
		t.Errorf("body=%s — it should say why and what to turn on", rw.Body.String())
	}
	// And it must not have resumed the run behind the 403.
	rec, err := h.store.Get(t.Context(), NodeJobID("run-human", "gate"))
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Status != core.JobStatusAwaiting {
		t.Errorf("node is %s, want it still awaiting", rec.Status)
	}
}

func TestApproveAuthed_APIKeyAllowedWhenStepOptsIn(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedParkedApproval(t, h, "run-bot", map[string]any{"prompt": "Refund $230?", "allow_api": true})

	rw := h.do(t, "POST", "/api/v1/approvals/run-bot/gate?decision=approve&comment=policy+bot", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("api key approve = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	rec, err := h.store.Get(t.Context(), NodeJobID("run-bot", "gate"))
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("node is %s, want succeeded", rec.Status)
	}
	// Attribution is the key's subject, never a client-supplied name.
	if got, _ := rec.Result.Output["approver"].Inline.(string); got != "alice" {
		t.Errorf("approver = %q, want the key's subject", got)
	}
}

// The gate is on the credential KIND, not on permissions: a person working the
// Approvals inbox decides either way, which is the whole point of the step.
func TestApproveAuthed_SessionApprovesWithoutOptIn(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedParkedApproval(t, h, "run-inbox", map[string]any{"prompt": "Refund $230?"})

	sessions := auth.NewMemSessionStore()
	h.gw.Sessions = sessions
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: sessions},
	}
	_, tok, err := auth.IssueSession(t.Context(), sessions, auth.User{
		Subject: "anna@nordkraft.se", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}},
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/approvals/run-inbox/gate?decision=approve", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("session approve = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	rec, _ := h.store.Get(t.Context(), NodeJobID("run-inbox", "gate"))
	if got, _ := rec.Result.Output["approver"].Inline.(string); got != "anna@nordkraft.se" {
		t.Errorf("approver = %q, want the signed-in person", got)
	}
}
