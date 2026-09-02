// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// End-to-end test of the support-view endpoint over real HTTP + a real graph in
// the workspace store: an active grant unlocks a REDACTED bundle, secrets never
// leak, and a missing grant / missing role are rejected. Complements the
// store/handler unit tests (which don't exercise the graph-load path).
func TestSupportView_EndToEnd(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	h.gw.Grants = support.NewMemGrantStore()
	h.gw.SupportAgents = support.NewMemAgentStore()
	h.gw.supportNow = func() time.Time { return now }
	auditLog := NewMemAuditLog()
	h.gw.Audit = auditLog
	ctx := context.Background()

	// Seed a flow (tenant t / workspace ws — the harness's MapWorkspaces key)
	// carrying a pasted secret in a param.
	if _, err := h.ws.Save(core.Graph{
		ID: "flow1", Tenant: "t", Workspace: "ws", Name: "Daily invoice",
		Nodes: []core.Node{{
			ID: "charge", Module: "stripe_create_customer",
			Params: map[string]any{"api_key": "sk_live_leakyleakyleaky00", "email": "${secret.EMAIL}"},
		}},
	}, "author"); err != nil {
		t.Fatalf("seed graph: %v", err)
	}

	// A support agent (API key carrying SupportAgentRole; its own tenant is
	// irrelevant — the grant is the authority).
	_, agentTok, err := auth.IssueAPIKey(h.ks, ctx, "k-agent", "", "", "agent-a",
		[]core.Role{core.SupportAgentRole()}, nil)
	if err != nil {
		t.Fatalf("issue agent key: %v", err)
	}

	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/support/flows/t/ws/flow1", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	// No grant yet → 404 (existence not leaked).
	if rw := get(agentTok); rw.Code != 404 {
		t.Fatalf("no grant should 404, got %d: %s", rw.Code, rw.Body)
	}

	// Approve an active grant for (agent-a, t, flow1).
	if err := h.gw.Grants.Create(ctx, core.AccessGrant{
		ID: "g1", Tenant: "t", FlowID: "flow1", AgentSubject: "agent-a",
		Status: core.GrantRequested, RequestedAt: now, RequestedBy: "agent-a",
	}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	if err := h.gw.Grants.Decide(ctx, "g1", core.GrantApproved, "admin", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Now the view opens with a REDACTED bundle.
	rw := get(agentTok)
	if rw.Code != 200 {
		t.Fatalf("with grant should 200, got %d: %s", rw.Code, rw.Body)
	}
	body := rw.Body.String()
	if strings.Contains(body, "sk_live_leakyleakyleaky00") {
		t.Errorf("support view leaked the pasted secret:\n%s", body)
	}
	// Diagnostic structure survives: the flow id, the module, the redaction
	// sentinel for the literal param, and the kept ${secret.…} reference.
	for _, want := range []string{"flow1", "stripe_create_customer", "__redacted", "${secret.EMAIL}"} {
		if !strings.Contains(body, want) {
			t.Errorf("bundle missing diagnostic %q:\n%s", want, body)
		}
	}

	// Every support view is audited into the ORG's OWN log (not some vendor-side
	// log), naming the agent, the flow, and the grant that authorized it — the
	// org's guarantee that it can see everything support looked at.
	viewEvent, ok := auditActions(t, auditLog, "t")["support.view"]
	if !ok {
		t.Fatalf("no support.view audit event in the org's log: %v", auditActions(t, auditLog, "t"))
	}
	if viewEvent.Actor != "agent-a" || viewEvent.Target != "flow1" || viewEvent.Detail != "grant=g1" {
		t.Errorf("support.view audit = %+v, want actor agent-a / target flow1 / detail grant=g1", viewEvent)
	}

	// A different support agent with no grant → 404.
	_, otherTok, _ := auth.IssueAPIKey(h.ks, ctx, "k-agent-b", "", "", "agent-b",
		[]core.Role{core.SupportAgentRole()}, nil)
	if rw := get(otherTok); rw.Code != 404 {
		t.Errorf("agent without a grant should 404, got %d", rw.Code)
	}

	// A non-support principal (the harness editor) → 403.
	if rw := get(h.token); rw.Code != 403 {
		t.Errorf("non-support principal should 403, got %d", rw.Code)
	}

	// Revoke → the view closes again (404).
	if err := h.gw.Grants.Revoke(ctx, "g1", "admin", now.Add(time.Minute)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if rw := get(agentTok); rw.Code != 404 {
		t.Errorf("after revoke the view should 404, got %d", rw.Code)
	}
}
