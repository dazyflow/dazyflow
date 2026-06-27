// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// googleCallbackEnv wires the full happy-path environment for the Google
// sign-in callback: an OrgAuth config, user/session/profile stores, httptest
// servers for the token + userinfo endpoints, and a stubbed ID-token verifier
// whose signed email matches userinfo. Returns the harness ready to serve.
func googleCallbackEnv(t *testing.T, signedEmail, hd string, emailVerified bool) *gatewayHarness {
	t.Helper()
	h := newGatewayHarness(t)
	const clientID = "cid-cb.apps.googleusercontent.com"

	h.gw.OrgAuth = newMemOrgAuth()
	cfg := auth.OrgAuthConfig{
		Tenant: "acme", GoogleClientID: clientID, GoogleClientSecret: "sec",
	}
	if hd != "" {
		cfg.GoogleWorkspaceDomain = hd
	}
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), cfg)

	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()
	h.gw.Profiles = newCovProfiles()
	h.gw.Memberships = newFakeMembershipStore()
	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","id_token":"idt","token_type":"Bearer"}`))
	}))
	t.Cleanup(tokenSrv.Close)
	uiBody := `{"email":"` + signedEmail + `","email_verified":` + boolJSON(emailVerified) + `,"sub":"s1"`
	if hd != "" {
		uiBody += `,"hd":"` + hd + `"`
	}
	uiBody += `}`
	uiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(uiBody))
	}))
	t.Cleanup(uiSrv.Close)
	withGoogleEndpoints(t, tokenSrv.URL, uiSrv.URL)

	installGoogleVerifier(t, clientID, stubGoogleVerifier{
		claims: auth.Claims{Extras: map[string]any{"email": signedEmail}},
	})
	return h
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestGoogleSignInCallback_NewUserSuccess drives the full callback to a new
// user creation, session issuance, and same-host cookie redirect.
func TestGoogleSignInCallback_NewUserSuccess(t *testing.T) {
	h := googleCallbackEnv(t, "fresh@acme.test", "", true)
	state, _ := mintGoogleState("acme", "/dashboard", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.Host = "app.dazyflow.test"
	ServeForTest(h.gw, rw, r)

	if rw.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	if loc := rw.Header().Get("Location"); loc != "/dashboard" {
		t.Fatalf("redirect = %q, want /dashboard", loc)
	}
	if len(rw.Result().Cookies()) == 0 {
		t.Error("expected a session cookie to be set")
	}
	// The user was created in the SSO org.
	u, err := h.gw.Users.GetByEmail(context.Background(), "fresh@acme.test")
	if err != nil || u.Tenant != "acme" {
		t.Fatalf("new user = %+v err=%v", u, err)
	}
}

// TestGoogleSignInCallback_ExistingUserSuccess covers the existing-user leg
// (resolveSignInUser returns isNew=false; resolveActiveOrg home-tenant path).
func TestGoogleSignInCallback_ExistingUserSuccess(t *testing.T) {
	h := googleCallbackEnv(t, "returning@acme.test", "", true)
	_ = h.gw.Users.PutUser(context.Background(), auth.User{
		Email: "returning@acme.test", Subject: "returning@acme.test",
		Tenant: "acme", Workspace: "main", Roles: []core.Role{core.TeamRoleEditor()},
	})
	state, _ := mintGoogleState("acme", "/", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.Host = "app.dazyflow.test"
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusFound {
		t.Fatalf("callback = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
}

// TestGoogleSignInCallback_TestModeSuccess: an admin "Test" sign-in verifies
// the Google side then stops, redirecting with ?test=ok and minting no user.
func TestGoogleSignInCallback_TestModeSuccess(t *testing.T) {
	h := googleCallbackEnv(t, "tester@acme.test", "acme.test", true)
	state, _ := mintGoogleState("acme", "/admin/sso", "", true)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusFound {
		t.Fatalf("test callback = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	if loc := rw.Header().Get("Location"); !strings.Contains(loc, "test=ok") {
		t.Fatalf("test redirect = %q, want test=ok", loc)
	}
	// No user must have been created by a test sign-in.
	if _, err := h.gw.Users.GetByEmail(context.Background(), "tester@acme.test"); err == nil {
		t.Error("test sign-in must not mint a user")
	}
}

// TestGoogleSignInCallback_EmailNotVerified drives validateGoogleClaims's
// not-verified rejection through the live callback.
func TestGoogleSignInCallback_EmailNotVerified(t *testing.T) {
	h := googleCallbackEnv(t, "unverified@acme.test", "", false)
	state, _ := mintGoogleState("acme", "/", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("unverified callback = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}
}

// TestGoogleSignInCallback_DomainMismatch: org restricts to a Workspace
// domain the signed-in user is not part of.
func TestGoogleSignInCallback_DomainMismatch(t *testing.T) {
	// Org requires hd=acme.test but userinfo carries no hd.
	h := newGatewayHarness(t)
	const clientID = "cid-dm.apps.googleusercontent.com"
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{
		Tenant: "acme", GoogleClientID: clientID, GoogleClientSecret: "sec",
		GoogleWorkspaceDomain: "acme.test",
	})
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","id_token":"idt"}`))
	}))
	t.Cleanup(tokenSrv.Close)
	uiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"email":"x@gmail.com","email_verified":true,"sub":"s"}`))
	}))
	t.Cleanup(uiSrv.Close)
	withGoogleEndpoints(t, tokenSrv.URL, uiSrv.URL)
	installGoogleVerifier(t, clientID, stubGoogleVerifier{
		claims: auth.Claims{Extras: map[string]any{"email": "x@gmail.com"}},
	})

	state, _ := mintGoogleState("acme", "/", "", false)
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("domain-mismatch callback = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}
}

// TestGoogleSignInCallback_ExchangeFailure: the token endpoint 4xxs, so the
// callback routes through classifyGoogleError -> signInError (502).
func TestGoogleSignInCallback_ExchangeFailure(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{
		Tenant: "acme", GoogleClientID: "c.apps.googleusercontent.com", GoogleClientSecret: "s",
	})
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	t.Cleanup(tokenSrv.Close)
	withGoogleEndpoints(t, tokenSrv.URL, "http://unused.invalid")

	state, _ := mintGoogleState("acme", "/", "", false)
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("exchange-failure callback = %d, want 502; body=%s", rw.Code, rw.Body.String())
	}
}

// TestGoogleSignInCallback_DomainAutoEnroll covers resolveActiveOrg's
// domain-authorized auto-join leg through the live callback: an existing user
// whose home tenant differs from the SSO org, with a matching Workspace
// domain, is enrolled and gets a session.
func TestGoogleSignInCallback_DomainAutoEnroll(t *testing.T) {
	h := googleCallbackEnv(t, "joiner@acme.test", "acme.test", true)
	// Existing user whose home tenant is "elsewhere".
	_ = h.gw.Users.PutUser(context.Background(), auth.User{
		Email: "joiner@acme.test", Subject: "joiner@acme.test",
		Tenant: "elsewhere", Workspace: "main", Roles: []core.Role{core.TeamRoleEditor()},
	})
	state, _ := mintGoogleState("acme", "/", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.Host = "app.dazyflow.test"
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusFound {
		t.Fatalf("auto-enroll callback = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	if _, err := h.gw.Memberships.GetMembership(context.Background(), "joiner@acme.test", "acme"); err != nil {
		t.Fatalf("auto-enroll did not create membership: %v", err)
	}
}
