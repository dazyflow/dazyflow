package daemon

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// TestCallerIsOrgOwner_Cov covers callerIsOrgOwner: the nil-store guard, a
// matching home owner, and a non-owner.
func TestCallerIsOrgOwner_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// No Users store -> never an owner.
	if h.gw.callerIsOrgOwner(context.Background(), core.Principal{Subject: "x@y.z"}, "acme") {
		t.Fatal("nil Users store should not report ownership")
	}

	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	_ = users.PutUser(context.Background(), auth.User{
		Email: "owner@acme.test", Subject: "owner@acme.test", Tenant: "acme",
	})

	if !h.gw.callerIsOrgOwner(context.Background(), core.Principal{Subject: "Owner@Acme.test"}, "acme") {
		t.Fatal("home owner should be recognized (case-insensitive)")
	}
	if h.gw.callerIsOrgOwner(context.Background(), core.Principal{Subject: "owner@acme.test"}, "other") {
		t.Fatal("owner of acme should not own 'other'")
	}
	if h.gw.callerIsOrgOwner(context.Background(), core.Principal{Subject: "ghost@acme.test"}, "acme") {
		t.Fatal("unknown user should not own anything")
	}
}

// TestPeerAdminBlocked_Cov covers peerAdminBlocked's legs: non-admin target,
// self-action, and a non-owner admin blocked from touching a peer admin.
func TestPeerAdminBlocked_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	// Make "owner@acme.test" the home owner of acme.
	_ = users.PutUser(context.Background(), auth.User{
		Email: "owner@acme.test", Subject: "owner@acme.test", Tenant: "acme",
	})

	adminRoles := []core.Role{core.TeamRoleAdmin()}
	memberRoles := []core.Role{core.TeamRoleEditor()}
	caller := core.Principal{Subject: "coadmin@acme.test"}

	// Target isn't an admin -> not blocked.
	if h.gw.peerAdminBlocked(context.Background(), caller, "bob@acme.test", "acme", memberRoles) {
		t.Fatal("editing a non-admin should not be blocked")
	}
	// Acting on yourself -> not blocked.
	if h.gw.peerAdminBlocked(context.Background(), caller, "coadmin@acme.test", "acme", adminRoles) {
		t.Fatal("acting on yourself should not be blocked")
	}
	// A non-owner admin touching a peer admin -> blocked.
	if !h.gw.peerAdminBlocked(context.Background(), caller, "peer@acme.test", "acme", adminRoles) {
		t.Fatal("non-owner admin touching a peer admin should be blocked")
	}
	// The org owner touching a peer admin -> allowed.
	owner := core.Principal{Subject: "owner@acme.test"}
	if h.gw.peerAdminBlocked(context.Background(), owner, "peer@acme.test", "acme", adminRoles) {
		t.Fatal("org owner should be allowed to touch a peer admin")
	}
}

// TestSeatQuotaExceeded_Cov covers seatQuotaExceeded: no-cap default, and an
// at-capacity org under a free-tier seat limit.
func TestSeatQuotaExceeded_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// No cap (FreeMaxMembers 0) -> never exceeded.
	if ex, _ := h.gw.seatQuotaExceeded(context.Background(), "acme"); ex {
		t.Fatal("uncapped org should not exceed seats")
	}

	// Cap of 1, with 1 existing member -> exceeded.
	h.svc.FreeMaxMembers = 1
	mem := newFakeMembershipStore()
	h.gw.Memberships = mem
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "a@acme.test", Tenant: "acme", Workspace: "main",
		Roles: []core.Role{core.TeamRoleEditor()},
	})
	ex, limit := h.gw.seatQuotaExceeded(context.Background(), "acme")
	if !ex || limit != 1 {
		t.Fatalf("seatQuotaExceeded = %v, limit=%d, want true/1", ex, limit)
	}
}
