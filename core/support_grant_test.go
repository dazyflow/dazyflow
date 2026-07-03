// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"testing"
	"time"
)

func supportAgent(subject string) Principal {
	return Principal{Subject: subject, Roles: []Role{SupportAgentRole()}}
}

func supportGraph() Graph {
	return Graph{ID: "daily-invoice", Tenant: "acme", Workspace: "main"}
}

// approvedGrant builds an active grant for (agent, tenant, flow) expiring in 1h.
func approvedGrant(now time.Time) AccessGrant {
	return AccessGrant{
		ID:           "grant-1",
		TicketID:     "ticket-1",
		Tenant:       "acme",
		FlowID:       "daily-invoice",
		AgentSubject: "agent-a",
		Status:       GrantApproved,
		RequestedAt:  now.Add(-time.Hour),
		RequestedBy:  "agent-a",
		DecidedBy:    "admin-1",
		ExpiresAt:    now.Add(time.Hour),
	}
}

func TestAuthorizeGraphSupportView_Approved(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	err := AuthorizeGraphSupportView(supportAgent("agent-a"), supportGraph(), approvedGrant(now), now)
	if err != nil {
		t.Fatalf("approved + unexpired grant should authorize, got %v", err)
	}
}

func TestAuthorizeGraphSupportView_Rejections(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	base := approvedGrant(now)

	cases := []struct {
		name  string
		p     Principal
		graph Graph
		grant AccessGrant
		now   time.Time
	}{
		{
			name:  "expired",
			p:     supportAgent("agent-a"),
			graph: supportGraph(),
			grant: func() AccessGrant { g := base; g.ExpiresAt = now.Add(-time.Minute); return g }(),
			now:   now,
		},
		{
			name:  "expiry boundary (now == ExpiresAt)",
			p:     supportAgent("agent-a"),
			graph: supportGraph(),
			grant: func() AccessGrant { g := base; g.ExpiresAt = now; return g }(),
			now:   now,
		},
		{
			name:  "denied",
			p:     supportAgent("agent-a"),
			graph: supportGraph(),
			grant: func() AccessGrant { g := base; g.Status = GrantDenied; return g }(),
			now:   now,
		},
		{
			name:  "revoked",
			p:     supportAgent("agent-a"),
			graph: supportGraph(),
			grant: func() AccessGrant {
				g := base
				g.Status = GrantRevoked
				rt := now.Add(-time.Minute)
				g.RevokedAt = &rt
				return g
			}(),
			now: now,
		},
		{
			name:  "still-approved but revoked timestamp set",
			p:     supportAgent("agent-a"),
			graph: supportGraph(),
			grant: func() AccessGrant { g := base; rt := now.Add(-time.Minute); g.RevokedAt = &rt; return g }(),
			now:   now,
		},
		{
			name:  "requested (not yet approved)",
			p:     supportAgent("agent-a"),
			graph: supportGraph(),
			grant: func() AccessGrant { g := base; g.Status = GrantRequested; return g }(),
			now:   now,
		},
		{
			name:  "wrong agent",
			p:     supportAgent("agent-b"),
			graph: supportGraph(),
			grant: base,
			now:   now,
		},
		{
			name:  "wrong flow",
			p:     supportAgent("agent-a"),
			graph: func() Graph { g := supportGraph(); g.ID = "other-flow"; return g }(),
			grant: base,
			now:   now,
		},
		{
			name:  "wrong tenant",
			p:     supportAgent("agent-a"),
			graph: func() Graph { g := supportGraph(); g.Tenant = "evil-corp"; return g }(),
			grant: base,
			now:   now,
		},
		{
			name:  "not a support agent",
			p:     Principal{Subject: "agent-a", Roles: []Role{TeamRoleAdmin()}},
			graph: supportGraph(),
			grant: base,
			now:   now,
		},
		{
			name:  "empty subject can't match empty grant agent",
			p:     supportAgent(""),
			graph: supportGraph(),
			grant: func() AccessGrant { g := base; g.AgentSubject = ""; return g }(),
			now:   now,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := AuthorizeGraphSupportView(c.p, c.graph, c.grant, c.now)
			if !errors.Is(err, ErrUnauthorized) {
				t.Errorf("want ErrUnauthorized, got %v", err)
			}
		})
	}
}

// A support agent must have NO ambient access: without a grant, the normal
// tenant/run/edit checks reject them, and RequireTenant still blocks a foreign
// tenant. The support-view capability never leaks into Run/Edit.
func TestSupportAgent_NoAmbientAccess(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	agent := supportAgent("agent-a") // no Tenant set
	g := supportGraph()

	if err := RequireTenant(agent, "acme"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("support agent must not cross tenant via RequireTenant, got %v", err)
	}
	if err := AuthorizeGraphView(agent, g); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("support agent has no ordinary view access, got %v", err)
	}
	// Read-only capability must never gate Run/Edit — even with a valid grant,
	// those paths reject a support principal (missing perms + tenant).
	if err := AuthorizeGraphRun(agent, g); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("support agent must not run, got %v", err)
	}
	if err := AuthorizeGraphEdit(agent, g); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("support agent must not edit, got %v", err)
	}
	// Sanity: with an active grant the support VIEW does open (the capability
	// path), proving the rejections above aren't just "support can't do anything".
	if err := AuthorizeGraphSupportView(agent, g, approvedGrant(now), now); err != nil {
		t.Errorf("active grant should still authorize the support view, got %v", err)
	}
}

func TestAccessGrant_StateHelpers(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	active := approvedGrant(now)

	if !active.IsActive(now) {
		t.Error("approved + unexpired should be active")
	}
	if active.IsActive(active.ExpiresAt) {
		t.Error("expiry boundary is exclusive — now == ExpiresAt is inactive")
	}
	if !active.CanRevoke() || active.CanDecide() {
		t.Error("approved grant: CanRevoke=true, CanDecide=false")
	}

	requested := active
	requested.Status = GrantRequested
	if !requested.CanDecide() || requested.CanRevoke() {
		t.Error("requested grant: CanDecide=true, CanRevoke=false")
	}
	if requested.IsActive(now) {
		t.Error("a requested grant is not active")
	}
}
