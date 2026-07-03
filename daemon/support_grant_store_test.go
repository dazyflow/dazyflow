// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

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

func TestMemGrantStore_RequestApproveActive(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	s := NewMemGrantStore()

	if err := s.Create(ctx, reqGrant("g1", "agent-a", now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Not active while merely requested.
	if _, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now); ok {
		t.Fatal("a requested grant must not be active")
	}
	// Approve it (4h box).
	if err := s.Decide(ctx, "g1", core.GrantApproved, "admin-1", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("decide: %v", err)
	}
	g, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now)
	if !ok {
		t.Fatal("approved + unexpired grant should be active")
	}
	if g.DecidedBy != "admin-1" || g.DecidedAt == nil {
		t.Errorf("decision metadata not stamped: %+v", g)
	}
	// Wrong flow / agent / tenant never match.
	for _, bad := range [][3]string{{"agent-a", "acme", "other"}, {"agent-b", "acme", "daily-invoice"}, {"agent-a", "evil", "daily-invoice"}} {
		if _, ok, _ := s.ActiveGrant(ctx, bad[0], bad[1], bad[2], now); ok {
			t.Errorf("grant should not match %v", bad)
		}
	}
}

func TestMemGrantStore_ExpiryAndRevoke(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	s := NewMemGrantStore()
	_ = s.Create(ctx, reqGrant("g1", "agent-a", now))
	_ = s.Decide(ctx, "g1", core.GrantApproved, "admin-1", now, now.Add(time.Hour))

	// After expiry, no longer active (lazy expiry — status stays approved).
	if _, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now.Add(2*time.Hour)); ok {
		t.Error("expired grant must not be active")
	}
	// Revoke ends it immediately.
	if err := s.Revoke(ctx, "g1", "admin-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok, _ := s.ActiveGrant(ctx, "agent-a", "acme", "daily-invoice", now.Add(2*time.Minute)); ok {
		t.Error("revoked grant must not be active")
	}
	g, _ := s.Get(ctx, "g1")
	if g.Status != core.GrantRevoked || g.RevokedBy != "admin-1" || g.RevokedAt == nil {
		t.Errorf("revoke metadata not stamped: %+v", g)
	}
}

func TestMemGrantStore_TransitionGuards(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	s := NewMemGrantStore()
	_ = s.Create(ctx, reqGrant("g1", "agent-a", now))

	// Duplicate create.
	if err := s.Create(ctx, reqGrant("g1", "agent-a", now)); !errors.Is(err, errGrantExists) {
		t.Errorf("duplicate create should fail, got %v", err)
	}
	// Can't revoke a merely-requested grant.
	if err := s.Revoke(ctx, "g1", "admin-1", now); !errors.Is(err, errGrantNotRevocable) {
		t.Errorf("revoking a requested grant should fail, got %v", err)
	}
	// Decide must be approved|denied.
	if err := s.Decide(ctx, "g1", core.GrantRevoked, "admin-1", now, now); !errors.Is(err, errBadDecision) {
		t.Errorf("deciding with a non-decision status should fail, got %v", err)
	}
	// Approve, then a second decide is rejected (no double-decide).
	_ = s.Decide(ctx, "g1", core.GrantApproved, "admin-1", now, now.Add(time.Hour))
	if err := s.Decide(ctx, "g1", core.GrantDenied, "admin-2", now, now); !errors.Is(err, errGrantNotDecidable) {
		t.Errorf("double-decide should fail, got %v", err)
	}
	// Missing grant.
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get missing should be ErrNotFound, got %v", err)
	}
}

func TestMemGrantStore_ListForTenant(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	s := NewMemGrantStore()
	_ = s.Create(ctx, reqGrant("g1", "agent-a", now))
	g2 := reqGrant("g2", "agent-b", now.Add(time.Minute))
	_ = s.Create(ctx, g2)
	other := reqGrant("g3", "agent-c", now)
	other.Tenant = "beta"
	_ = s.Create(ctx, other)

	got, _ := s.ListForTenant(ctx, "acme")
	if len(got) != 2 {
		t.Fatalf("want 2 acme grants, got %d", len(got))
	}
	// Newest request first.
	if got[0].ID != "g2" {
		t.Errorf("want g2 first (newest), got %s", got[0].ID)
	}
}
