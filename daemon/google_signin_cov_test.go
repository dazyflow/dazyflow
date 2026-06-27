package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

func TestPendingInvitation_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()

	// Nil store -> no invite.
	if _, ok := h.gw.pendingInvitation(ctx, "a@x.com", "t"); ok {
		t.Fatal("nil invitations store returned an invite")
	}

	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites
	_ = invites.PutInvitation(ctx, auth.Invitation{
		Token: "i1", Email: "a@x.com", Tenant: "acme", ExpiresAt: time.Now().Add(time.Hour),
	})
	// Expired invite is ignored.
	_ = invites.PutInvitation(ctx, auth.Invitation{
		Token: "i2", Email: "old@x.com", Tenant: "acme", ExpiresAt: time.Now().Add(-time.Hour),
	})

	if inv, ok := h.gw.pendingInvitation(ctx, "A@X.com", "acme"); !ok || inv.Token != "i1" {
		t.Fatalf("pending invite = %+v ok=%v", inv, ok)
	}
	if _, ok := h.gw.pendingInvitation(ctx, "old@x.com", "acme"); ok {
		t.Fatal("expired invite returned as pending")
	}
	if _, ok := h.gw.pendingInvitation(ctx, "nobody@x.com", "acme"); ok {
		t.Fatal("nonexistent invite returned")
	}
}

func TestResolveActiveOrg_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	mem := newFakeMembershipStore()
	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Memberships = mem
	h.gw.Invitations = invites
	r := httptest.NewRequest("GET", "/cb", nil)
	cfg := auth.OrgAuthConfig{Tenant: "acme"}

	// New user: lands in their own (home) tenant.
	newUser := auth.User{Email: "n@x.com", Tenant: "home", Workspace: "main", Roles: []core.Role{core.TeamRoleViewer()}}
	tn, ws, _, reason, _, _ := h.gw.resolveActiveOrg(r, cfg, newUser, true, "n@x.com", googleSignInState{Tenant: "acme"})
	if reason != "" || tn != "home" || ws != "main" {
		t.Fatalf("new user resolve = %q/%q reason=%q", tn, ws, reason)
	}

	// Existing user, signing into home tenant (st.Tenant == user.Tenant).
	home := auth.User{Email: "h@x.com", Tenant: "acme", Workspace: "main"}
	if _, _, _, reason, _, _ := h.gw.resolveActiveOrg(r, cfg, home, false, "h@x.com", googleSignInState{Tenant: "acme"}); reason != "" {
		t.Fatalf("home tenant resolve reason = %q", reason)
	}

	// Existing user with a membership in the target org.
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "m@x.com", Tenant: "acme", Workspace: "ws2", Roles: []core.Role{core.TeamRoleEditor()},
	})
	other := auth.User{Email: "m@x.com", Tenant: "home"}
	if tn, ws, _, reason, _, _ := h.gw.resolveActiveOrg(r, cfg, other, false, "m@x.com", googleSignInState{Tenant: "acme"}); reason != "" || tn != "acme" || ws != "ws2" {
		t.Fatalf("membership resolve = %q/%q reason=%q", tn, ws, reason)
	}

	// Existing user, no membership, no domain match, no invite -> not_invited.
	stranger := auth.User{Email: "s@x.com", Tenant: "home"}
	_, _, _, reason, status, _ := h.gw.resolveActiveOrg(r, cfg, stranger, false, "s@x.com", googleSignInState{Tenant: "acme"})
	if reason != "not_invited" || status != http.StatusForbidden {
		t.Fatalf("stranger resolve reason=%q status=%d, want not_invited/403", reason, status)
	}

	// Domain-authorized auto-join.
	domainCfg := auth.OrgAuthConfig{Tenant: "acme", GoogleWorkspaceDomain: "acme.com"}
	dom := auth.User{Email: "d@acme.com", Tenant: "home"}
	tn, ws, roles, reason, _, _ := h.gw.resolveActiveOrg(r, domainCfg, dom, false, "d@acme.com", googleSignInState{Tenant: "acme"})
	if reason != "" || tn != "acme" || ws != "main" || len(roles) == 0 {
		t.Fatalf("domain join = %q/%q roles=%v reason=%q", tn, ws, roles, reason)
	}
	if _, err := mem.GetMembership(context.Background(), "d@acme.com", "acme"); err != nil {
		t.Fatalf("domain join did not create membership: %v", err)
	}

	// Invitation-authorized auto-join honors invite roles/workspace.
	_ = invites.PutInvitation(context.Background(), auth.Invitation{
		Token: "inv", Email: "i@x.com", Tenant: "acme", Workspace: "wsInv",
		Roles: []core.Role{core.TeamRoleAdmin()}, ExpiresAt: time.Now().Add(time.Hour),
	})
	invUser := auth.User{Email: "i@x.com", Tenant: "home"}
	tn, ws, _, reason, _, _ = h.gw.resolveActiveOrg(r, cfg, invUser, false, "i@x.com", googleSignInState{Tenant: "acme"})
	if reason != "" || tn != "acme" || ws != "wsInv" {
		t.Fatalf("invite join = %q/%q reason=%q", tn, ws, reason)
	}
}

func TestSignInError_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Non-test: writes a JSON error with the given status.
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/cb", nil)
	h.gw.signInError(rw, r, googleSignInState{}, "exchange_failed", http.StatusBadGateway, "boom")
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("non-test signInError = %d, want 502", rw.Code)
	}

	// Test mode: redirects to the SSO settings page with a test_error code.
	rw = httptest.NewRecorder()
	h.gw.signInError(rw, r, googleSignInState{Test: true, ReturnTo: "/admin/sso"}, "invalid_grant", http.StatusForbidden, "x")
	if rw.Code != http.StatusFound {
		t.Fatalf("test signInError = %d, want 302", rw.Code)
	}
	if loc := rw.Header().Get("Location"); loc == "" || loc[:11] != "/admin/sso?" {
		t.Fatalf("redirect location = %q", rw.Header().Get("Location"))
	}
}
