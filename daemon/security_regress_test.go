// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// An org admin used to be able to mint a key naming ANY subject — e.g. another
// org's user — and then act as that account on /me endpoints.
func TestAdminIssueAPIKey_SubjectPinnedToMembers(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	members := newFakeMembershipStore()
	h.gw.Memberships = members
	_ = users.PutUser(t.Context(), auth.User{Email: "victim@other.test", Subject: "victim@other.test", Tenant: "other"})
	_ = users.PutUser(t.Context(), auth.User{Email: "homed@t.test", Subject: "homed@t.test", Tenant: "t"})
	seedMember(t, members, "member@t.test", "t", core.TeamRoleEditor())

	issue := func(subject string) int {
		return teamAdminDo(t, h, "POST", "/api/v1/admin/api-keys", map[string]any{
			"subject": subject,
			"roles":   []map[string]any{{"name": "r", "permissions": []string{"graph:run"}}},
		}).Code
	}
	for subject, want := range map[string]int{
		"victim@other.test":   http.StatusForbidden, // someone else's account
		"nobody@nowhere.test": http.StatusForbidden, // email-shaped, not a member
		"homed@t.test":        http.StatusCreated,
		"member@t.test":       http.StatusCreated,
		"ci-bot":              http.StatusCreated, // a service identity
		"boss":                http.StatusCreated, // the caller itself
	} {
		if got := issue(subject); got != want {
			t.Errorf("subject %q: code %d, want %d", subject, got, want)
		}
	}
}

func TestDeleteMyAccount_RefusesAPIKey(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	_ = users.PutUser(t.Context(), auth.User{Email: "alice", Subject: "alice", Tenant: "t"})
	rw := h.do(t, "DELETE", "/api/v1/me/account?confirm=alice", nil)
	if rw.Code != http.StatusForbidden || !strings.Contains(rw.Body.String(), "session_required") {
		t.Fatalf("API-key account delete = %d %s, want 403 session_required", rw.Code, rw.Body.String())
	}
	if _, err := users.GetByEmail(t.Context(), "alice"); err != nil {
		t.Fatal("account was erased through an API key")
	}
}

func issueTestKey(t *testing.T, ks *auth.MemKeyStore, id, tenant, subject string, roles ...core.Role) {
	t.Helper()
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), id, tenant, "ws", subject, roles, nil); err != nil {
		t.Fatal(err)
	}
}

func keyRevoked(t *testing.T, ks *auth.MemKeyStore, id string) bool {
	t.Helper()
	k, err := ks.GetKey(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return k.RevokedAt != nil
}

// API keys snapshot tenant + roles, so removal/demotion must sweep them.
func TestMemberChanges_RevokeAPIKeys(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	members := newFakeMembershipStore()
	h.gw.Memberships = members
	seedMember(t, members, "gone@t.test", "t", core.TeamRoleEditor())
	seedMember(t, members, "demoted@t.test", "t", core.TeamRoleEditor())
	issueTestKey(t, h.ks, "gone-t", "t", "gone@t.test", core.TeamRoleEditor())
	issueTestKey(t, h.ks, "gone-elsewhere", "other", "gone@t.test", core.TeamRoleEditor())
	issueTestKey(t, h.ks, "demoted-editor", "t", "demoted@t.test", core.TeamRoleEditor())
	issueTestKey(t, h.ks, "demoted-viewer", "t", "demoted@t.test", core.TeamRoleViewer())

	if rw := teamAdminDo(t, h, "DELETE", "/api/v1/admin/members/gone@t.test", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rw.Code, rw.Body.String())
	}
	if !keyRevoked(t, h.ks, "gone-t") {
		t.Error("removed member's key in the tenant still active")
	}
	if keyRevoked(t, h.ks, "gone-elsewhere") {
		t.Error("removal revoked the member's key in another tenant")
	}

	if rw := teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/demoted@t.test", map[string]any{
		"roles": []map[string]any{{"name": "viewer"}},
	}); rw.Code != http.StatusOK {
		t.Fatalf("demote = %d %s", rw.Code, rw.Body.String())
	}
	if !keyRevoked(t, h.ks, "demoted-editor") {
		t.Error("demoted member's editor key still active")
	}
	if keyRevoked(t, h.ks, "demoted-viewer") {
		t.Error("key within the new role was revoked")
	}
}

func TestRevokeSubjectKeys_PlatformAdminOnly(t *testing.T) {
	t.Parallel()
	ks := auth.NewMemKeyStore()
	svc := &Service{AdminKeys: ks}
	issueTestKey(t, ks, "pa", "", "op@x.test", core.PlatformAdminRole())
	issueTestKey(t, ks, "plain", "t", "op@x.test", core.TeamRoleEditor())
	n, err := svc.revokeSubjectKeys(t.Context(), "OP@x.test", "", func(k auth.APIKey) bool {
		return !keyWithinRoles(k, []core.Role{core.TeamRoleEditor()})
	})
	if err != nil || n != 1 {
		t.Fatalf("revoked %d, err %v; want 1", n, err)
	}
	if keyRevoked(t, ks, "pa") || !keyRevoked(t, ks, "plain") {
		t.Fatal("keep filter not honoured")
	}
}

// With verification running, an unverified account holding an allowlisted
// email must not be elevated — anyone could have registered that address.
func TestElevatePlatformAdmin_RequiresVerifiedEmail(t *testing.T) {
	t.Parallel()
	users, _ := auth.OpenJSONUserStore("")
	gw := &HTTPGateway{
		svc:            &Service{Mailer: &Mailer{}, PublicBaseURL: "https://app.example"},
		Users:          users,
		PlatformAdmins: []string{"boss@example.com"},
	}
	u := auth.User{Email: "boss@example.com", Tenant: "usr_x"}
	if got := gw.authAPI().elevatePlatformAdmin(context.Background(), u); (core.Principal{Roles: got.Roles}).Has(core.PermPlatformAdmin) {
		t.Fatal("unverified allowlisted email was elevated to platform admin")
	}
	now := time.Now()
	u.VerifiedAt = &now
	got := gw.authAPI().elevatePlatformAdmin(context.Background(), u)
	if !(core.Principal{Roles: got.Roles}).Has(core.PermPlatformAdmin) {
		t.Fatal("verified allowlisted email was not elevated")
	}
}

func TestBillingCheckoutPortal_RequireOrgAdmin(t *testing.T) {
	t.Parallel()
	h, _, _ := billingHarness(t)
	for _, path := range []string{"/api/v1/me/billing/checkout", "/api/v1/me/billing/portal"} {
		if rw := h.do(t, "POST", path, nil); rw.Code != http.StatusForbidden {
			t.Errorf("editor %s = %d, want 403", path, rw.Code)
		}
	}
}

func TestCheckReservedSecretWrite_AdminNamespaces(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"oauth.google.default", "emailtmpl.brand", "orgauth.google.client_secret",
		"gitcred.acct.key", "sshcred.box.key", "sshlogin.box.pw", "res.db", "cursor.x", "pollstate.x",
	} {
		if err := checkReservedSecretWrite(ScopeTenant, name); err == nil {
			t.Errorf("%q writable through the generic secrets API", name)
		}
	}
	for _, name := range []string{"API_KEY", "conn.ntfy.server", "GITHUB_WEBHOOK_SECRET"} {
		if err := checkReservedSecretWrite(ScopeTenant, name); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
}

func TestMintTenantID_Entropy(t *testing.T) {
	t.Parallel()
	for _, mint := range []func() (string, error){mintTenantID, mintOrgTenantID} {
		id, err := mint()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 4+2*tenantIDRandomBytes || tenantIDRandomBytes < 10 {
			t.Errorf("tenant id %q too short", id)
		}
	}
}

func TestMintFreshTenantID_SkipsTaken(t *testing.T) {
	t.Parallel()
	profiles := newCovProfiles()
	_ = profiles.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "usr_taken", DisplayName: "x"})
	a := &authAPI{Profiles: profiles}
	seq := []string{"usr_taken", "usr_free"}
	i := 0
	got, err := a.mintFreshTenantID(t.Context(), func() (string, error) { i++; return seq[i-1], nil })
	if err != nil || got != "usr_free" {
		t.Fatalf("got %q err %v, want usr_free", got, err)
	}
}

// A workspace-bound credential must not reach another workspace's files.
func TestWorkspaceFiles_EnforcesBoundWorkspace(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // editor key bound to t/ws
	if rw := h.do(t, "GET", "/api/v1/workspaces/t/other/files/usage", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("foreign workspace = %d, want 403", rw.Code)
	}
	if rw := h.do(t, "GET", "/api/v1/workspaces/t/ws/files/usage", nil); rw.Code == http.StatusForbidden {
		t.Fatalf("own workspace refused: %s", rw.Body.String())
	}
}

func TestStripeWebhook_UnpaidCheckoutDoesNotGrant(t *testing.T) {
	t.Parallel()
	h, plans, _ := billingHarness(t)
	postStripeEvent(t, h, `{"type":"checkout.session.completed","data":{"object":{
		"customer":"cus_1","subscription":"sub_1","client_reference_id":"t","payment_status":"unpaid"}}}`)
	if p, _ := plans.GetPlan(t.Context(), "t"); p.Plan == PlanPro {
		t.Fatalf("unpaid checkout granted Pro: %+v", p)
	}
}

func TestStripeWebhook_LateUpdateAfterDeleteStaysFree(t *testing.T) {
	t.Parallel()
	h, plans, _ := billingHarness(t)
	postStripeEvent(t, h, `{"type":"customer.subscription.deleted","data":{"object":{
		"id":"sub_1","customer":"cus_1","status":"canceled","metadata":{"tenant":"t"}}}}`)
	postStripeEvent(t, h, `{"type":"customer.subscription.updated","data":{"object":{
		"id":"sub_1","customer":"cus_1","status":"active","metadata":{"tenant":"t"}}}}`)
	postStripeEvent(t, h, `{"type":"checkout.session.completed","data":{"object":{
		"customer":"cus_1","subscription":"sub_1","client_reference_id":"t","payment_status":"paid"}}}`)
	if p, _ := plans.GetPlan(t.Context(), "t"); p.Plan != PlanFree {
		t.Fatalf("out-of-order events restored Pro after deletion: %+v", p)
	}
	for _, status := range []string{"incomplete", "paused", "unpaid"} {
		postStripeEvent(t, h, `{"type":"customer.subscription.updated","data":{"object":{
			"id":"sub_2","customer":"cus_1","status":"`+status+`","metadata":{"tenant":"t"}}}}`)
		if p, _ := plans.GetPlan(t.Context(), "t"); p.Plan != PlanFree {
			t.Errorf("status %s kept Pro", status)
		}
	}
}

func TestListMembers_ShowsEveryHomeUser(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Memberships = newFakeMembershipStore()
	_ = users.PutUser(t.Context(), auth.User{Email: "a@t.test", Subject: "a@t.test", Tenant: "t"})
	_ = users.PutUser(t.Context(), auth.User{Email: "b@t.test", Subject: "b@t.test", Tenant: "t"})
	rw := h.adminDo(t, "GET", "/api/v1/admin/members", nil)
	var out struct {
		Members []struct {
			Email string `json:"email"`
		} `json:"members"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &out)
	if rw.Code != http.StatusOK || len(out.Members) != 2 {
		t.Fatalf("members = %d %s, want both home users", rw.Code, rw.Body.String())
	}
}

// switch-org used to replace the session's roles with the membership's,
// silently dropping the platform-admin elevation added at sign-in.
func TestSwitchOrg_KeepsPlatformAdminElevation(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	sessions := auth.NewMemSessionStore()
	members := newFakeMembershipStore()
	h.gw.Users, h.gw.Sessions, h.gw.Memberships = users, sessions, members
	h.gw.PlatformAdmins = []string{"op@x.test"}
	h.svc.Auth = auth.Chain{&auth.SessionAuthenticator{Store: sessions}}
	u := auth.User{Email: "op@x.test", Subject: "op@x.test", Tenant: "usr_op", Workspace: "main", Roles: defaultSignupRoles()}
	_ = users.PutUser(t.Context(), u)
	seedMember(t, members, "op@x.test", "org_b", core.TeamRoleViewer())
	_, token, err := auth.IssueSession(t.Context(), sessions, h.gw.authAPI().elevateSessionRoles(t.Context(), u), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/auth/switch-org", strings.NewReader(`{"tenant":"org_b"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("switch = %d %s", rw.Code, rw.Body.String())
	}
	sess, err := sessions.GetSession(t.Context(), auth.SessionLookupKey(token))
	if err != nil {
		t.Fatal(err)
	}
	if sess.Tenant != "org_b" || !(core.Principal{Roles: sess.Roles}).Has(core.PermPlatformAdmin) {
		t.Fatalf("after switch: tenant %q roles %+v, want org_b with platform admin kept", sess.Tenant, sess.Roles)
	}
}
