package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

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
