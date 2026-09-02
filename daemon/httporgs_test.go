// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// --- createOrg validation branches ------------------------------------

func TestCreateOrg_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no Memberships/Profiles
	rw := h.do(t, "POST", "/api/v1/me/orgs", map[string]any{"display_name": "X"})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("create org no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestCreateOrg_DecodeError(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Memberships = newFakeMembershipStore()
	h.gw.Profiles = newRecordingOrgProfiles()
	req := newRawReq(t, h, "POST", "/api/v1/me/orgs", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("create org malformed = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestCreateOrg_NameTooLong(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Memberships = newFakeMembershipStore()
	h.gw.Profiles = newRecordingOrgProfiles()
	rw := h.do(t, "POST", "/api/v1/me/orgs", map[string]any{"display_name": strings.Repeat("x", 81)})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("long name = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

// --- getOrgAuthConfig / deleteOrgAuthConfig ---------------------------

func TestGetOrgAuthConfig_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no OrgAuth
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("no OrgAuth = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestGetOrgAuthConfig_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	// Default editor token lacks organization:admin.
	rw := h.do(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin OrgAuth = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestGetOrgAuthConfig_CrossTenantForbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config?tenant=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant OrgAuth = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestGetOrgAuthConfig_UnknownReturnsDefault(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("default OrgAuth = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"google_enabled":false`) {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestGetOrgAuthConfig_OK(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{
		Tenant: "t", GoogleClientID: "cid", GoogleClientSecret: "csec",
	})
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get OrgAuth = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"google_secret_set":true`) {
		t.Errorf("body %s, want secret_set", rw.Body.String())
	}
}

func TestDeleteOrgAuthConfig_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("delete no OrgAuth = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestDeleteOrgAuthConfig_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	rw := h.do(t, "DELETE", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete OrgAuth = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestDeleteOrgAuthConfig_OK(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{Tenant: "t", GoogleClientID: "cid"})
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete OrgAuth = %d (%s), want 204", rw.Code, rw.Body.String())
	}
}

// TestCallerIsOrgOwner_Cov covers callerIsOrgOwner: the nil-store guard, a
// matching home owner, and a non-owner.
func TestCallerIsOrgOwner_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// No Users store -> never an owner.
	if h.gw.orgAPI().callerIsOrgOwner(context.Background(), core.Principal{Subject: "x@y.z"}, "acme") {
		t.Fatal("nil Users store should not report ownership")
	}

	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	_ = users.PutUser(context.Background(), auth.User{
		Email: "owner@acme.test", Subject: "owner@acme.test", Tenant: "acme",
	})

	if !h.gw.orgAPI().callerIsOrgOwner(context.Background(), core.Principal{Subject: "Owner@Acme.test"}, "acme") {
		t.Fatal("home owner should be recognized (case-insensitive)")
	}
	if h.gw.orgAPI().callerIsOrgOwner(context.Background(), core.Principal{Subject: "owner@acme.test"}, "other") {
		t.Fatal("owner of acme should not own 'other'")
	}
	if h.gw.orgAPI().callerIsOrgOwner(context.Background(), core.Principal{Subject: "ghost@acme.test"}, "acme") {
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
	if h.gw.orgAPI().peerAdminBlocked(context.Background(), caller, "bob@acme.test", "acme", memberRoles) {
		t.Fatal("editing a non-admin should not be blocked")
	}
	// Acting on yourself -> not blocked.
	if h.gw.orgAPI().peerAdminBlocked(context.Background(), caller, "coadmin@acme.test", "acme", adminRoles) {
		t.Fatal("acting on yourself should not be blocked")
	}
	// A non-owner admin touching a peer admin -> blocked.
	if !h.gw.orgAPI().peerAdminBlocked(context.Background(), caller, "peer@acme.test", "acme", adminRoles) {
		t.Fatal("non-owner admin touching a peer admin should be blocked")
	}
	// The org owner touching a peer admin -> allowed.
	owner := core.Principal{Subject: "owner@acme.test"}
	if h.gw.orgAPI().peerAdminBlocked(context.Background(), owner, "peer@acme.test", "acme", adminRoles) {
		t.Fatal("org owner should be allowed to touch a peer admin")
	}
}

// TestSeatQuotaExceeded_Cov covers seatQuotaExceeded: no-cap default, and an
// at-capacity org under a free-tier seat limit.
func TestSeatQuotaExceeded_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// No cap (FreeMaxMembers 0) -> never exceeded.
	if ex, _ := h.gw.seats().seatQuotaExceeded(context.Background(), "acme"); ex {
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
	ex, limit := h.gw.seats().seatQuotaExceeded(context.Background(), "acme")
	if !ex || limit != 1 {
		t.Fatalf("seatQuotaExceeded = %v, limit=%d, want true/1", ex, limit)
	}
}

// sessionDo runs a request authenticated by a session token (dzs_…),
// which several org routes require (switch-org, accept-invitation reject
// API keys). The harness's auth chain gets a SessionAuthenticator added.
func sessionDo(t *testing.T, h *gatewayHarness, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewBuffer(b)
	} else {
		rdr = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// orgsSessionHarness wires Users + Sessions + Memberships and a session
// authenticator, returning a signed-in session token for `user`.
func orgsSessionHarness(t *testing.T, user auth.User) (*gatewayHarness, *fakeMembershipStore, *auth.MemSessionStore, string) {
	t.Helper()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(t.Context(), user)
	sessions := auth.NewMemSessionStore()
	mem := newFakeMembershipStore()
	h.gw.Users = users
	h.gw.Sessions = sessions
	h.gw.Memberships = mem
	// Add session auth to the chain so dzs_ tokens authenticate.
	h.svc.Auth = auth.Chain{
		&auth.APIKeyAuthenticator{Store: h.ks},
		&auth.SessionAuthenticator{Store: sessions},
	}
	_, tok, err := auth.IssueSession(t.Context(), sessions, user, time.Hour)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return h, mem, sessions, tok
}

func TestSwitchOrg_Cov(t *testing.T) {
	user := auth.User{Subject: "alice@example.com", Email: "alice@example.com", Tenant: "home", Workspace: "main", Roles: []core.Role{core.TeamRoleEditor()}}
	h, mem, _, tok := orgsSessionHarness(t, user)
	ctx := context.Background()
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "alice@example.com", Tenant: "acme", Workspace: "ws2", Roles: []core.Role{core.TeamRoleViewer()}})

	// Missing tenant -> 400.
	if rw := sessionDo(t, h, tok, "POST", "/api/v1/auth/switch-org", map[string]any{}); rw.Code != http.StatusBadRequest {
		t.Fatalf("no tenant = %d, want 400", rw.Code)
	}
	// Switch to current tenant (no-op OK).
	if rw := sessionDo(t, h, tok, "POST", "/api/v1/auth/switch-org", map[string]any{"tenant": "home"}); rw.Code != http.StatusOK {
		t.Fatalf("noop switch = %d: %s", rw.Code, rw.Body.String())
	}
	// Switch to a member org.
	rw := sessionDo(t, h, tok, "POST", "/api/v1/auth/switch-org", map[string]any{"tenant": "acme"})
	if rw.Code != http.StatusOK {
		t.Fatalf("switch = %d: %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Tenant    string `json:"tenant"`
		Workspace string `json:"workspace"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.Tenant != "acme" || resp.Workspace != "ws2" {
		t.Fatalf("switch resp = %+v", resp)
	}
	// Switch to a non-member org -> 403.
	if rw := sessionDo(t, h, tok, "POST", "/api/v1/auth/switch-org", map[string]any{"tenant": "stranger"}); rw.Code != http.StatusForbidden {
		t.Fatalf("non-member switch = %d, want 403", rw.Code)
	}
}

func TestSwitchOrg_APIKeyRejected(t *testing.T) {
	// An API-key principal has no User record -> can't switch.
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()
	rw := h.do(t, "POST", "/api/v1/auth/switch-org", map[string]any{"tenant": "other"})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("api-key switch = %d, want 403", rw.Code)
	}
}

func TestListMembers_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	mem := newFakeMembershipStore()
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Memberships = mem
	h.gw.Users = users
	ctx := context.Background()
	// Home owner of tenant "t".
	_ = users.PutUser(ctx, auth.User{Email: "owner@example.com", Subject: "owner@example.com", Tenant: "t", Workspace: "ws", Roles: []core.Role{core.TeamRoleAdmin()}})
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "member@example.com", Tenant: "t", Workspace: "ws", Roles: []core.Role{core.TeamRoleEditor()}})

	// Non-admin (editor token) -> 403.
	if rw := h.do(t, "GET", "/api/v1/admin/members", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("editor list members = %d, want 403", rw.Code)
	}

	rw := teamAdminDo(t, h, "GET", "/api/v1/admin/members", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list members = %d: %s", rw.Code, rw.Body.String())
	}
	var lr struct {
		Members []struct {
			Email string `json:"email"`
			Home  bool   `json:"home"`
		} `json:"members"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &lr)
	if len(lr.Members) != 2 {
		t.Fatalf("members = %+v, want 2 (owner + member)", lr.Members)
	}

	// Listing another tenant without platform admin -> 403.
	if rw := teamAdminDo(t, h, "GET", "/api/v1/admin/members?tenant=other", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant list = %d, want 403", rw.Code)
	}
}

func TestInvitationFlow_Cov(t *testing.T) {
	user := auth.User{Subject: "invitee@example.com", Email: "invitee@example.com", Tenant: "home", Workspace: "main"}
	h, mem, _, tok := orgsSessionHarness(t, user)
	invites, _ := auth.OpenJSONInvitationStore("")
	prof := newCovProfiles()
	h.gw.Invitations = invites
	h.gw.Profiles = prof
	ctx := context.Background()
	_ = prof.PutOrgProfile(ctx, auth.OrgProfile{Tenant: "acme", DisplayName: "Acme Inc"})

	pending := auth.Invitation{
		Token: "inv_good", Email: "invitee@example.com", Tenant: "acme",
		Workspace: "ws", Roles: []core.Role{core.TeamRoleEditor()},
		InvitedBy: "boss@acme", ExpiresAt: time.Now().Add(time.Hour),
	}
	_ = invites.PutInvitation(ctx, pending)

	// viewInvitation (unauthenticated) shows org display name.
	rw := rawDo(t, h, "GET", "/api/v1/invitations/inv_good", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("view = %d: %s", rw.Code, rw.Body.String())
	}
	var vr struct {
		TenantDisplay string `json:"tenant_display"`
		Pending       bool   `json:"pending"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &vr)
	if vr.TenantDisplay != "Acme Inc" || !vr.Pending {
		t.Fatalf("view resp = %+v", vr)
	}
	// Unknown token -> 404.
	if rw := rawDo(t, h, "GET", "/api/v1/invitations/nope", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("view unknown = %d, want 404", rw.Code)
	}

	// Accept it (session-authed, matching email).
	rw = sessionDo(t, h, tok, "POST", "/api/v1/invitations/inv_good/accept", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("accept = %d: %s", rw.Code, rw.Body.String())
	}
	if m, err := mem.GetMembership(ctx, "invitee@example.com", "acme"); err != nil || m.Workspace != "ws" {
		t.Fatalf("membership after accept = %+v / %v", m, err)
	}
	// Re-accepting a used invite -> 410 Gone.
	if rw := sessionDo(t, h, tok, "POST", "/api/v1/invitations/inv_good/accept", nil); rw.Code != http.StatusGone {
		t.Fatalf("re-accept = %d, want 410", rw.Code)
	}

	// Wrong-email invite -> 403.
	_ = invites.PutInvitation(ctx, auth.Invitation{
		Token: "inv_other", Email: "someoneelse@example.com", Tenant: "acme",
		Workspace: "ws", ExpiresAt: time.Now().Add(time.Hour),
	})
	if rw := sessionDo(t, h, tok, "POST", "/api/v1/invitations/inv_other/accept", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("wrong-email accept = %d, want 403", rw.Code)
	}
}

func TestRevokeInvitation_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites
	ctx := context.Background()
	_ = invites.PutInvitation(ctx, auth.Invitation{
		Token: "inv_rev", Email: "x@example.com", Tenant: "t",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	// Foreign-tenant invitation -> 403.
	_ = invites.PutInvitation(ctx, auth.Invitation{
		Token: "inv_foreign", Email: "y@example.com", Tenant: "other",
		ExpiresAt: time.Now().Add(time.Hour),
	})

	// Non-admin -> 403.
	if rw := h.do(t, "DELETE", "/api/v1/admin/invitations/inv_rev", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("editor revoke = %d, want 403", rw.Code)
	}
	// Admin revokes own-tenant invite.
	if rw := teamAdminDo(t, h, "DELETE", "/api/v1/admin/invitations/inv_rev", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d: %s", rw.Code, rw.Body.String())
	}
	// Unknown token -> 404.
	if rw := teamAdminDo(t, h, "DELETE", "/api/v1/admin/invitations/ghost", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("revoke ghost = %d, want 404", rw.Code)
	}
	// Foreign tenant -> 403.
	if rw := teamAdminDo(t, h, "DELETE", "/api/v1/admin/invitations/inv_foreign", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("revoke foreign = %d, want 403", rw.Code)
	}
}

// TestSeatQuota_CountsTheOwner — the seat gate must count the organization
// owner, who holds no membership row.
//
// Ownership is implicit in the home tenant: `memberships` carries a row for
// everyone who was invited, and nobody for the owner. listMembers knows this
// and adds the owner back before rendering the People page, so the page shows
// N+1 people where the table holds N rows. The seat gate read the rows alone,
// so the two disagreed by exactly one and every plan seated one person too
// many — a 3-seat org reached four before anything refused.
//
// Walking it through at limit 3: owner + 2 invited = 3 people, which is the
// cap, so the next invitation must be refused. Counting rows alone reads 2,
// sees 2 >= 3 is false, and lets a fourth person in.
func TestSeatQuota_CountsTheOwner(t *testing.T) {
	h := newGatewayHarness(t)
	invites, err := auth.OpenJSONInvitationStore("")
	if err != nil {
		t.Fatalf("open invitation store: %v", err)
	}
	h.gw.Invitations = invites

	// adminDo authenticates as an organization:admin bound to tenant "t".
	const tenant = "t"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(t.Context(), auth.User{
		Subject: "owner@example.com", Email: "owner@example.com",
		Tenant: tenant, Workspace: "main",
	})
	h.gw.Users = users

	mem := newFakeMembershipStore()
	h.gw.Memberships = mem

	ents := newCovMemEntitlements()
	_ = ents.PutEntitlement(t.Context(), TenantEntitlement{
		Tenant: tenant, TierID: "free", MaxMembers: ptrInt(3),
	})
	h.svc.Entitlements = ents

	// Owner alone: one seat of three used, so inviting is fine.
	if rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "first@example.com"}); rw.Code != http.StatusCreated {
		t.Fatalf("invite with 1/3 seats used = %d: %s", rw.Code, rw.Body.String())
	}

	// Two invited members joined. That is owner + 2 = 3 people = the cap.
	for _, email := range []string{"first@example.com", "second@example.com"} {
		if err := mem.PutMembership(t.Context(), auth.Membership{
			UserEmail: email, Tenant: tenant, Workspace: "main",
			Roles: []core.Role{core.TeamRoleViewer()},
		}); err != nil {
			t.Fatalf("seat %s: %v", email, err)
		}
	}

	rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "fourth@example.com"})
	if rw.Code != http.StatusPaymentRequired {
		t.Fatalf("invite at 3/3 seats (owner + 2 members) = %d, want 402: %s",
			rw.Code, rw.Body.String())
	}
	if body := rw.Body.String(); !strings.Contains(body, "3 members") {
		t.Errorf("refusal should name the limit, got: %s", body)
	}
}

// TestSeatQuota_NoOwnerRowStillCounts — a tenant with no home user (an org
// created by an operator, say) falls back to counting rows alone rather than
// erroring, so seats still cap and nobody is locked out.
func TestSeatQuota_NoOwnerRowStillCounts(t *testing.T) {
	h := newGatewayHarness(t)
	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites
	const tenant = "t"

	users, _ := auth.OpenJSONUserStore("") // nobody's home tenant is "t"
	h.gw.Users = users
	mem := newFakeMembershipStore()
	h.gw.Memberships = mem

	ents := newCovMemEntitlements()
	_ = ents.PutEntitlement(t.Context(), TenantEntitlement{
		Tenant: tenant, TierID: "free", MaxMembers: ptrInt(2),
	})
	h.svc.Entitlements = ents

	for _, email := range []string{"a@example.com", "b@example.com"} {
		_ = mem.PutMembership(t.Context(), auth.Membership{
			UserEmail: email, Tenant: tenant, Workspace: "main",
			Roles: []core.Role{core.TeamRoleViewer()},
		})
	}
	if rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "c@example.com"}); rw.Code != http.StatusPaymentRequired {
		t.Fatalf("invite at 2/2 rows, no owner = %d, want 402: %s", rw.Code, rw.Body.String())
	}
}

// seatHarness stands up a gateway with a home owner, an entitlement limit, and
// an invitation store — the shape every seat-counting test needs.
func seatHarness(t *testing.T, limit int) (*gatewayHarness, *fakeMembershipStore, auth.InvitationStore) {
	t.Helper()
	h := newGatewayHarness(t)
	const tenant = "t" // adminDo authenticates as organization:admin on "t"

	invites, err := auth.OpenJSONInvitationStore("")
	if err != nil {
		t.Fatalf("open invitation store: %v", err)
	}
	h.gw.Invitations = invites

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(t.Context(), auth.User{
		Subject: "owner@example.com", Email: "owner@example.com",
		Tenant: tenant, Workspace: "main",
	})
	h.gw.Users = users

	mem := newFakeMembershipStore()
	h.gw.Memberships = mem

	ents := newCovMemEntitlements()
	_ = ents.PutEntitlement(t.Context(), TenantEntitlement{
		Tenant: tenant, TierID: "free", MaxMembers: ptrInt(limit),
	})
	h.svc.Entitlements = ents
	return h, mem, invites
}

// TestSeatQuota_PendingInvitationsHoldASeat — an outstanding invitation counts
// against the cap, so an admin can't hand out more promises than the plan can
// honour.
//
// Counting only the seated let every invitation pass on its own, because none
// of them had been accepted yet. Three invitations went out on a 3-seat plan
// that already had two people, and the refusal surfaced on whichever invitee
// clicked second — as "ask an admin to upgrade". The admin saw nothing wrong.
func TestSeatQuota_PendingInvitationsHoldASeat(t *testing.T) {
	h, mem, _ := seatHarness(t, 3)

	// Owner + one member = 2 of 3 seats used.
	_ = mem.PutMembership(t.Context(), auth.Membership{
		UserEmail: "first@example.com", Tenant: "t", Workspace: "main",
		Roles: []core.Role{core.TeamRoleViewer()},
	})

	// The third seat is available, so one invitation is fine.
	if rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "second@example.com"}); rw.Code != http.StatusCreated {
		t.Fatalf("invite for the last free seat = %d: %s", rw.Code, rw.Body.String())
	}
	// That invitation now holds the last seat: the next one has nowhere to go.
	rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "third@example.com"})
	if rw.Code != http.StatusPaymentRequired {
		t.Fatalf("invite beyond the last seat = %d, want 402: %s", rw.Code, rw.Body.String())
	}
	if body := rw.Body.String(); !strings.Contains(body, "3 members") {
		t.Errorf("refusal should name the limit, got: %s", body)
	}
}

// TestSeatQuota_ReInviteDoesNotCountTwice — re-sending an invitation to someone
// who already has one outstanding must not run them against their own seat.
func TestSeatQuota_ReInviteDoesNotCountTwice(t *testing.T) {
	h, mem, _ := seatHarness(t, 3)
	_ = mem.PutMembership(t.Context(), auth.Membership{
		UserEmail: "first@example.com", Tenant: "t", Workspace: "main",
		Roles: []core.Role{core.TeamRoleViewer()},
	})
	for i := 0; i < 2; i++ {
		if rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
			map[string]any{"email": "second@example.com"}); rw.Code != http.StatusCreated {
			t.Fatalf("re-invite #%d = %d: %s", i+1, rw.Code, rw.Body.String())
		}
	}
}

// TestSeatQuota_SpentInvitationsFreeTheirSeat — revoked, expired and accepted
// invitations stop holding a seat. Only one that can still be walked through
// the door counts.
func TestSeatQuota_SpentInvitationsFreeTheirSeat(t *testing.T) {
	h, mem, invites := seatHarness(t, 3)
	_ = mem.PutMembership(t.Context(), auth.Membership{
		UserEmail: "first@example.com", Tenant: "t", Workspace: "main",
		Roles: []core.Role{core.TeamRoleViewer()},
	})
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	for _, inv := range []auth.Invitation{
		{Token: "inv_revoked", Email: "a@example.com", Tenant: "t", Workspace: "main",
			ExpiresAt: future, RevokedAt: &past},
		{Token: "inv_expired", Email: "b@example.com", Tenant: "t", Workspace: "main",
			ExpiresAt: past},
		{Token: "inv_accepted", Email: "c@example.com", Tenant: "t", Workspace: "main",
			ExpiresAt: future, AcceptedAt: &past},
	} {
		if err := invites.PutInvitation(t.Context(), inv); err != nil {
			t.Fatalf("seed %s: %v", inv.Token, err)
		}
	}
	// None of those three hold the last seat, so a real invitation still fits.
	if rw := h.adminDo(t, "POST", "/api/v1/admin/invitations",
		map[string]any{"email": "live@example.com"}); rw.Code != http.StatusCreated {
		t.Fatalf("spent invitations should not hold seats, got %d: %s", rw.Code, rw.Body.String())
	}
}

// TestSeatQuota_AcceptIgnoresOtherPendingInvitations — accept-time counts real
// occupancy, not promises.
//
// The two gates deliberately count differently. If accepting also counted
// outstanding invitations, an invitation nobody ever opened would keep a real
// person out of a seat that is genuinely free — the invitee would be refused
// on behalf of someone who never showed up.
func TestSeatQuota_AcceptIgnoresOtherPendingInvitations(t *testing.T) {
	user := auth.User{Subject: "joiner@example.com", Email: "joiner@example.com",
		Tenant: "home", Workspace: "main"}
	h, mem, _, tok := orgsSessionHarness(t, user)
	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites
	ents := newCovMemEntitlements()
	_ = ents.PutEntitlement(t.Context(), TenantEntitlement{
		Tenant: "acme", TierID: "free", MaxMembers: ptrInt(2),
	})
	h.svc.Entitlements = ents

	future := time.Now().UTC().Add(time.Hour)
	// One seat is genuinely taken; the other is spoken for by an invitation
	// that nobody has opened.
	_ = mem.PutMembership(t.Context(), auth.Membership{
		UserEmail: "seated@example.com", Tenant: "acme", Workspace: "main",
		Roles: []core.Role{core.TeamRoleViewer()},
	})
	_ = invites.PutInvitation(t.Context(), auth.Invitation{
		Token: "inv_ghost", Email: "ghost@example.com", Tenant: "acme",
		Workspace: "main", ExpiresAt: future,
	})
	_ = invites.PutInvitation(t.Context(), auth.Invitation{
		Token: "inv_joiner", Email: "joiner@example.com", Tenant: "acme",
		Workspace: "main", Roles: []core.Role{core.TeamRoleViewer()}, ExpiresAt: future,
	})

	if rw := sessionDo(t, h, tok, "POST", "/api/v1/invitations/inv_joiner/accept", nil); rw.Code != http.StatusOK {
		t.Fatalf("accept = %d, want 200 — a ghost invitation must not hold the seat: %s",
			rw.Code, rw.Body.String())
	}
}

// TestSeatMembership_ConcurrentAcceptsCannotOverfill — the last free seat goes
// to exactly one of the people racing for it.
//
// The gate used to count seats and then insert as two separate steps, so
// several accepts arriving together all read the same free seat and all took
// it. Small window, but a seat limit that a bit of timing can walk past isn't
// a limit. seatMembership now hands the decision to the store, which makes it
// atomically — a transaction and a per-tenant advisory lock in Postgres, a
// mutex in the fake.
//
// Run with -race to also catch the fake being touched unsafely.
func TestSeatMembership_ConcurrentAcceptsCannotOverfill(t *testing.T) {
	// Repeated, because the bug this guards is a timing window: one round can
	// happen to serialize itself and pass even when the window is wide open.
	// A handful of rounds makes the old check-then-write fail reliably instead
	// of about a third of the time.
	const rounds, racers = 25, 8
	for round := 0; round < rounds; round++ {
		h, mem, _ := seatHarness(t, 3) // owner + 3-person plan = 2 membership rows
		_ = mem.PutMembership(t.Context(), auth.Membership{
			UserEmail: "first@example.com", Tenant: "t", Workspace: "main",
			Roles: []core.Role{core.TeamRoleViewer()},
		})

		var wg sync.WaitGroup
		seated := make([]bool, racers)
		errs := make([]error, racers)
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // line them all up on the same instant
				ok, _, err := h.gw.seats().seatMembership(context.Background(), auth.Membership{
					UserEmail: fmt.Sprintf("racer%d@example.com", i),
					Tenant:    "t", Workspace: "main",
					Roles: []core.Role{core.TeamRoleViewer()},
				})
				seated[i], errs[i] = ok, err
			}(i)
		}
		close(start)
		wg.Wait()

		got := 0
		for i, ok := range seated {
			if errs[i] != nil {
				t.Fatalf("round %d, racer %d: %v", round, i, errs[i])
			}
			if ok {
				got++
			}
		}
		if got != 1 {
			t.Fatalf("round %d: %d racers were seated, want exactly 1 — the last seat was handed out more than once", round, got)
		}
		rows, err := mem.ListByTenant(t.Context(), "t")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		// Owner (no row) + 2 rows = the 3 the plan allows.
		if len(rows) != 2 {
			t.Fatalf("round %d: membership rows = %d, want 2 (owner holds the third seat without one)", round, len(rows))
		}
	}
}

// TestSeatMembership_UpdatingAnExistingMemberIsNeverRefused — a role change on
// someone already seated must go through even when the org is full. They
// occupy a seat already; refusing would make a full org unable to fix a role.
func TestSeatMembership_UpdatingAnExistingMemberIsNeverRefused(t *testing.T) {
	h, mem, _ := seatHarness(t, 2) // owner + 1 row = full
	_ = mem.PutMembership(t.Context(), auth.Membership{
		UserEmail: "only@example.com", Tenant: "t", Workspace: "main",
		Roles: []core.Role{core.TeamRoleViewer()},
	})
	seated, _, err := h.gw.seats().seatMembership(t.Context(), auth.Membership{
		UserEmail: "only@example.com", Tenant: "t", Workspace: "main",
		Roles: []core.Role{core.TeamRoleAdmin()},
	})
	if err != nil {
		t.Fatalf("seat: %v", err)
	}
	if !seated {
		t.Fatal("re-seating an existing member was refused as if it needed a new seat")
	}
	m, _ := mem.GetMembership(t.Context(), "only@example.com", "t")
	if len(m.Roles) == 0 || m.Roles[0].Name != core.TeamRoleAdmin().Name {
		t.Errorf("roles = %+v, want the update applied", m.Roles)
	}
	// And a genuinely new person is still refused.
	if ok, _, _ := h.gw.seats().seatMembership(t.Context(), auth.Membership{
		UserEmail: "newcomer@example.com", Tenant: "t", Workspace: "main",
	}); ok {
		t.Error("a new person was seated in a full org")
	}
}
