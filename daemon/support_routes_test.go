// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// supportGateway builds a gateway with just the support stores wired + a pinned
// clock, enough to drive the grant-lifecycle handlers directly (bypassing
// requireAuth, which needs sessions).
func supportGateway() (*HTTPGateway, time.Time) {
	now := time.Unix(1_700_000_000, 0).UTC()
	h := &HTTPGateway{
		Grants:     support.NewMemGrantStore(),
		Bundles:    support.NewMemBundleStore(),
		supportNow: func() time.Time { return now },
	}
	return h, now
}

func agentPrincipal(subject string) core.Principal {
	return core.Principal{Subject: subject, Roles: []core.Role{core.SupportAgentRole()}}
}

func adminPrincipal(tenant string) core.Principal {
	return core.Principal{Subject: "admin-1", Tenant: tenant, Roles: []core.Role{core.TeamRoleAdmin()}}
}

// Full lifecycle: agent requests → admin lists + approves → ActiveGrant opens →
// revoke closes it.
func TestSupport_GrantLifecycle(t *testing.T) {
	h, now := supportGateway()
	ctx := context.Background()

	// 1. Agent requests access.
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/support/grants",
		strings.NewReader(`{"tenant":"acme","flow_id":"daily-invoice","ticket_id":"t1"}`))
	h.requestGrant(rw, req, agentPrincipal("agent-a"))
	if rw.Code != 201 {
		t.Fatalf("request grant: code %d body %s", rw.Code, rw.Body)
	}
	var created core.AccessGrant
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode grant: %v", err)
	}
	if created.Status != core.GrantRequested || created.AgentSubject != "agent-a" {
		t.Fatalf("unexpected created grant: %+v", created)
	}
	// Not active yet (only requested).
	if _, ok, _ := h.Grants.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now); ok {
		t.Fatal("requested grant must not be active")
	}

	// 2. Admin lists their tenant's grants and sees it.
	lrw := httptest.NewRecorder()
	h.listGrants(lrw, httptest.NewRequest("GET", "/api/v1/support/grants", nil), adminPrincipal("acme"))
	if lrw.Code != 200 || !strings.Contains(lrw.Body.String(), created.ID) {
		t.Fatalf("list grants: code %d body %s", lrw.Code, lrw.Body)
	}

	// 3. Admin approves → grant becomes active for 4h.
	drw := httptest.NewRecorder()
	dreq := httptest.NewRequest("POST", "/api/v1/support/grants/"+created.ID+"/decide",
		strings.NewReader(`{"decision":"approve"}`))
	dreq.SetPathValue("id", created.ID)
	h.decideGrant(drw, dreq, adminPrincipal("acme"))
	if drw.Code != 200 {
		t.Fatalf("decide: code %d body %s", drw.Code, drw.Body)
	}
	g, ok, _ := h.Grants.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now)
	if !ok {
		t.Fatal("approved grant should be active")
	}
	if want := now.Add(defaultSupportGrantTTL); !g.ExpiresAt.Equal(want) {
		t.Errorf("expiry = %v, want now+4h = %v", g.ExpiresAt, want)
	}

	// 4. Agent revokes their own grant → no longer active.
	rrw := httptest.NewRecorder()
	rreq := httptest.NewRequest("POST", "/api/v1/support/grants/"+created.ID+"/revoke", nil)
	rreq.SetPathValue("id", created.ID)
	h.revokeGrant(rrw, rreq, agentPrincipal("agent-a"))
	if rrw.Code != 200 {
		t.Fatalf("revoke: code %d body %s", rrw.Code, rrw.Body)
	}
	if _, ok, _ := h.Grants.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now); ok {
		t.Error("revoked grant must not be active")
	}
}

func TestSupport_RequestRequiresAgentRole(t *testing.T) {
	h, _ := supportGateway()
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/support/grants",
		strings.NewReader(`{"tenant":"acme","flow_id":"f1"}`))
	// A plain org user (no support:agent) is forbidden.
	h.requestGrant(rw, req, core.Principal{Subject: "u1", Tenant: "acme", Roles: []core.Role{core.TeamRoleViewer()}})
	if rw.Code != 403 {
		t.Errorf("want 403 without support role, got %d", rw.Code)
	}
}

func TestSupport_DecideAuthz(t *testing.T) {
	h, _ := supportGateway()
	ctx := context.Background()
	// Seed a requested grant for acme.
	_ = h.Grants.Create(ctx, reqGrant("g1", "agent-a", h.supportTime()))

	// A non-admin can't decide → 404 (existence not leaked).
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/support/grants/g1/decide", strings.NewReader(`{"decision":"approve"}`))
	req.SetPathValue("id", "g1")
	h.decideGrant(rw, req, agentPrincipal("agent-a"))
	if rw.Code != 404 {
		t.Errorf("non-admin decide should 404, got %d", rw.Code)
	}

	// A DIFFERENT tenant's admin can't decide acme's grant → 404.
	rw2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/support/grants/g1/decide", strings.NewReader(`{"decision":"approve"}`))
	req2.SetPathValue("id", "g1")
	h.decideGrant(rw2, req2, adminPrincipal("beta"))
	if rw2.Code != 404 {
		t.Errorf("cross-tenant admin decide should 404, got %d", rw2.Code)
	}
	// The grant is untouched (still requested).
	g, _ := h.Grants.Get(ctx, "g1")
	if g.Status != core.GrantRequested {
		t.Errorf("grant should be untouched, got %q", g.Status)
	}
}

func TestSupport_DisabledReturns501(t *testing.T) {
	h := &HTTPGateway{} // no stores wired
	rw := httptest.NewRecorder()
	h.requestGrant(rw, httptest.NewRequest("POST", "/api/v1/support/grants", strings.NewReader(`{}`)), agentPrincipal("a"))
	if rw.Code != 501 {
		t.Errorf("support disabled should 501, got %d", rw.Code)
	}
}

// Session elevation stamps SupportAgentRole for a granted email (mirrors the
// platform-admin elevation).
func TestElevateSupportAgent(t *testing.T) {
	agents := support.NewMemAgentStore()
	_ = agents.Grant(context.Background(), "agent@vendor.com", "op")
	h := &HTTPGateway{SupportAgents: agents}

	got := h.elevateSupportAgent(context.Background(), auth.User{Email: "Agent@Vendor.com"})
	has := false
	for _, r := range got.Roles {
		if r.Has(core.PermSupportAgent) {
			has = true
		}
	}
	if !has {
		t.Error("granted email should get SupportAgentRole")
	}

	// Not granted → unchanged.
	other := h.elevateSupportAgent(context.Background(), auth.User{Email: "rando@example.com"})
	for _, r := range other.Roles {
		if r.Has(core.PermSupportAgent) {
			t.Error("non-granted email must not get the role")
		}
	}

	// Nil store → no-op, no panic.
	bare := &HTTPGateway{}
	_ = bare.elevateSupportAgent(context.Background(), auth.User{Email: "agent@vendor.com"})
}

// reqGrant is a grant in the requested state, the state every route test here
// starts from. The store package has its own copy for its own tests; this one
// exists because the two packages no longer share a test binary.
func reqGrant(id, agent string, now time.Time) core.AccessGrant {
	return core.AccessGrant{
		ID:           id,
		TicketID:     "ticket-1",
		Tenant:       "acme",
		FlowID:       "daily-invoice",
		AgentSubject: agent,
		Status:       core.GrantRequested,
		RequestedAt:  now,
		RequestedBy:  agent,
		ExpiresAt:    now.Add(time.Hour),
	}
}
