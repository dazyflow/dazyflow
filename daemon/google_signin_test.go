// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

func TestValidateGoogleClaims(t *testing.T) {
	cases := []struct {
		name       string
		vc         verifiedGoogleClaims
		cfg        auth.OrgAuthConfig
		wantReason string
		wantStatus int
	}{
		{
			name: "ok, no domain restriction",
			vc:   verifiedGoogleClaims{Email: "a@gmail.com", EmailVerified: true},
			cfg:  auth.OrgAuthConfig{},
		},
		{
			name: "ok, domain matches (case-insensitive)",
			vc:   verifiedGoogleClaims{Email: "a@acme.test", EmailVerified: true, HD: "ACME.test"},
			cfg:  auth.OrgAuthConfig{GoogleWorkspaceDomain: "acme.test"},
		},
		{
			name:       "empty email",
			vc:         verifiedGoogleClaims{Email: "", EmailVerified: true, HD: "acme.test"},
			cfg:        auth.OrgAuthConfig{},
			wantReason: "no_email",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "unverified email",
			vc:         verifiedGoogleClaims{Email: "a@acme.test", EmailVerified: false},
			cfg:        auth.OrgAuthConfig{},
			wantReason: "not_verified",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "domain mismatch",
			vc:         verifiedGoogleClaims{Email: "a@gmail.com", EmailVerified: true, HD: "gmail.com"},
			cfg:        auth.OrgAuthConfig{GoogleWorkspaceDomain: "acme.test"},
			wantReason: "domain_mismatch",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "domain restricted but hd empty (personal account)",
			vc:         verifiedGoogleClaims{Email: "a@gmail.com", EmailVerified: true, HD: ""},
			cfg:        auth.OrgAuthConfig{GoogleWorkspaceDomain: "acme.test"},
			wantReason: "domain_mismatch",
			wantStatus: http.StatusForbidden,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, status, msg := validateGoogleClaims(c.vc, c.cfg)
			if reason != c.wantReason || status != c.wantStatus {
				t.Fatalf("validateGoogleClaims = (%q, %d), want (%q, %d)", reason, status, c.wantReason, c.wantStatus)
			}
			// Success returns no message; failures must carry one.
			if (msg == "") != (c.wantReason == "") {
				t.Errorf("msg = %q for reason %q", msg, c.wantReason)
			}
			// The domain-mismatch message names the required domain so the
			// user knows which account to use.
			if c.wantReason == "domain_mismatch" && !strings.Contains(msg, c.cfg.GoogleWorkspaceDomain) {
				t.Errorf("domain_mismatch msg %q should name %q", msg, c.cfg.GoogleWorkspaceDomain)
			}
		})
	}
}

// stubGoogleVerifier is an injectable IDTokenVerifier: it returns canned
// claims (or an error) for a given raw id_token, so verifyGoogleIDToken can be
// exercised without OIDC discovery / a live JWKS fetch.
type stubGoogleVerifier struct {
	claims auth.Claims
	err    error
}

func (s stubGoogleVerifier) Verify(_ context.Context, _ string) (auth.Claims, error) {
	return s.claims, s.err
}

// installGoogleVerifier seeds the package-level verifier cache for clientID so
// verifyGoogleIDToken hits the cache instead of building a real OIDC verifier.
func installGoogleVerifier(t *testing.T, clientID string, v auth.IDTokenVerifier) {
	t.Helper()
	googleVerifierCache.mu.Lock()
	googleVerifierCache.m[clientID] = v
	googleVerifierCache.mu.Unlock()
	t.Cleanup(func() {
		googleVerifierCache.mu.Lock()
		delete(googleVerifierCache.m, clientID)
		googleVerifierCache.mu.Unlock()
	})
}

func TestVerifyGoogleIDToken_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	const clientID = "cid-verify.apps.googleusercontent.com"
	cfg := auth.OrgAuthConfig{Tenant: "acme", GoogleClientID: clientID, GoogleClientSecret: "sec"}
	ctx := context.Background()

	// Missing id_token -> no_id_token (no verifier needed).
	if _, reason, status, _ := h.gw.authAPI().verifyGoogleIDToken(ctx, cfg, "", googleUserInfo{}); reason != "no_id_token" || status != http.StatusBadGateway {
		t.Fatalf("empty token reason=%q status=%d", reason, status)
	}

	// Verifier rejects the token.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{err: errors.New("bad sig")})
	if _, reason, status, _ := h.gw.authAPI().verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{}); reason != "id_token_invalid" || status != http.StatusForbidden {
		t.Fatalf("invalid token reason=%q status=%d", reason, status)
	}

	// Verified, but the signed token has no email claim.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{claims: auth.Claims{Subject: "s"}})
	if _, reason, status, _ := h.gw.authAPI().verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{}); reason != "no_email" || status != http.StatusBadGateway {
		t.Fatalf("no-email reason=%q status=%d", reason, status)
	}

	// Verified email disagrees with the userinfo email -> mismatch.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{claims: auth.Claims{Extras: map[string]any{"email": "Signed@Acme.test"}}})
	if _, reason, status, _ := h.gw.authAPI().verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{Email: "other@acme.test"}); reason != "email_mismatch" || status != http.StatusForbidden {
		t.Fatalf("mismatch reason=%q status=%d", reason, status)
	}

	// Happy path: signed email is lower-cased and returned; userinfo agrees.
	// email_verified + hd come from the SIGNED claims, not userinfo, and must be
	// surfaced for validateGoogleClaims to gate on.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{claims: auth.Claims{Extras: map[string]any{
		"email": "User@Acme.test", "email_verified": true, "hd": "acme.test",
	}}})
	vc, reason, _, _ := h.gw.authAPI().verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{Email: "user@acme.test"})
	if reason != "" || vc.Email != "user@acme.test" {
		t.Fatalf("happy path email=%q reason=%q", vc.Email, reason)
	}
	if !vc.EmailVerified || vc.HD != "acme.test" {
		t.Fatalf("signed claims not extracted: %+v", vc)
	}

	// Userinfo email omitted is allowed (signed token is authoritative).
	vc, reason, _, _ = h.gw.authAPI().verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{})
	if reason != "" || vc.Email != "user@acme.test" {
		t.Fatalf("empty-userinfo email=%q reason=%q", vc.Email, reason)
	}
}

func TestResolveSignInUser_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	profiles := newCovProfiles()
	h.gw.Profiles = profiles
	r := httptest.NewRequest("GET", "/cb", nil)
	st := googleSignInState{Tenant: "acme"}

	// First sign-in: a user is created in st.Tenant with a seeded org profile.
	rw := httptest.NewRecorder()
	user, isNew, ok := h.gw.authAPI().resolveSignInUser(rw, r, "new@acme.test", st)
	if !ok || !isNew || user.Tenant != "acme" || user.Email != "new@acme.test" {
		t.Fatalf("new user = %+v isNew=%v ok=%v", user, isNew, ok)
	}
	if prof, err := profiles.GetOrgProfile(r.Context(), "acme"); err != nil || prof.DisplayName == "" {
		t.Errorf("org profile not seeded: %+v err=%v", prof, err)
	}

	// Second sign-in for the same email: existing user, not new.
	rw = httptest.NewRecorder()
	user, isNew, ok = h.gw.authAPI().resolveSignInUser(rw, r, "new@acme.test", st)
	if !ok || isNew {
		t.Fatalf("existing user isNew=%v ok=%v", isNew, ok)
	}
}

func TestCompleteSignIn_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	sess := auth.Session{Tenant: "acme", Subject: "a@acme.test", ExpiresAt: time.Now().Add(time.Hour)}

	// Same-host (no Host pinned): sets a cookie inline and redirects to the
	// (sanitized) return path.
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback", nil)
	r.Host = "app.dazyflow.test"
	h.gw.authAPI().completeSignIn(rw, r, googleSignInState{ReturnTo: "/dashboard"}, sess, "tok-abc")
	if rw.Code != http.StatusFound || rw.Header().Get("Location") != "/dashboard" {
		t.Fatalf("same-host redirect = %d %q", rw.Code, rw.Header().Get("Location"))
	}
	if len(rw.Result().Cookies()) == 0 {
		t.Error("expected a session cookie to be set inline")
	}

	// An unsafe return path falls back to "/".
	rw = httptest.NewRecorder()
	h.gw.authAPI().completeSignIn(rw, r, googleSignInState{ReturnTo: "https://evil.test/x"}, sess, "tok")
	if loc := rw.Header().Get("Location"); loc != "/" {
		t.Fatalf("unsafe return path redirect = %q, want /", loc)
	}

	// Different host (per-org subdomain): bounces through /auth/handoff with a
	// one-time token instead of setting an apex cookie.
	rw = httptest.NewRecorder()
	h.gw.authAPI().completeSignIn(rw, r, googleSignInState{Host: "acme.dazyflow.test", ReturnTo: "/x"}, sess, "tok")
	loc := rw.Header().Get("Location")
	if rw.Code != http.StatusFound || !strings.Contains(loc, "acme.dazyflow.test/api/v1/auth/handoff?ot=") {
		t.Fatalf("handoff redirect = %d %q", rw.Code, loc)
	}
	for _, ck := range rw.Result().Cookies() {
		if ck.Name != "" && ck.Value != "" {
			// The apex must not get the session cookie on the handoff path.
			t.Errorf("handoff path should not set an apex cookie, got %s", ck.Name)
		}
	}
}

// TestGoogleSignInStart_Cov covers googleSignInStart: the not-configured and
// missing-tenant guards, an org without Google enabled, and the happy-path
// 302 to Google's auth endpoint carrying client_id, state, and the hd hint.
func TestGoogleSignInStart_Cov(t *testing.T) {
	// No OrgAuth store -> 501.
	h := newGatewayHarness(t)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/start?tenant=acme", nil))
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("no OrgAuth = %d, want 501", rw.Code)
	}

	h.gw.OrgAuth = newMemOrgAuth()
	h.svc.PublicBaseURL = "https://app.dazyflow.test"

	// Missing tenant -> 400.
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/start", nil))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant = %d, want 400", rw.Code)
	}

	// Tenant with no Google config -> 404.
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/start?tenant=acme", nil))
	if rw.Code != http.StatusNotFound {
		t.Fatalf("unconfigured tenant = %d, want 404", rw.Code)
	}

	// Configure Google for acme, with a workspace domain (hd hint path).
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{
		Tenant: "acme", GoogleClientID: "cid.apps.googleusercontent.com",
		GoogleClientSecret: "sec", GoogleWorkspaceDomain: "acme.test",
	})
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/start?tenant=acme&return_to=/dash", nil))
	if rw.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	loc := rw.Header().Get("Location")
	if !strings.HasPrefix(loc, googleAuthURL) {
		t.Fatalf("redirect not to Google: %q", loc)
	}
	for _, want := range []string{"client_id=cid", "state=", "hd=acme.test", "redirect_uri="} {
		if !strings.Contains(loc, want) {
			t.Errorf("redirect %q missing %q", loc, want)
		}
	}
}

func TestClassifyGoogleError_Cov(t *testing.T) {
	cases := map[string]string{
		"oauth invalid_client here":      "invalid_client",
		"redirect_uri_mismatch happened": "redirect_uri_mismatch",
		"invalid_grant: bad code":        "invalid_grant",
		"unauthorized_client for this":   "unauthorized_client",
		"some unrelated network timeout": "exchange_failed",
	}
	for msg, want := range cases {
		if got := classifyGoogleError(errors.New(msg)); got != want {
			t.Errorf("classifyGoogleError(%q) = %q, want %q", msg, got, want)
		}
	}
	if got := classifyGoogleError(nil); got != "" {
		t.Errorf("nil error = %q, want empty", got)
	}
}

// TestGoogleSignInCallback_EarlyErrors covers the callback's guard branches via
// the real mux: not-configured stores, an OAuth ?error=, an invalid state, and
// a missing code. None of these reach the live token exchange.
func TestGoogleSignInCallback_EarlyErrors(t *testing.T) {
	// Stores not configured -> 501.
	h := newGatewayHarness(t)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?code=x&state=y", nil))
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured callback = %d, want 501; body=%s", rw.Code, rw.Body.String())
	}

	// Wire the stores; now the state/code guards apply.
	h.gw.OrgAuth = newMemOrgAuth()
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()

	// ?error= with no (consumable) state -> JSON 400 "denied".
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?error=access_denied", nil))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("error param = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	// Missing/expired state -> 400.
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?code=abc", nil))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("missing state = %d, want 400", rw.Code)
	}

	// Valid state but missing code -> 400.
	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)
	rw = httptest.NewRecorder()
	rNoCode := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state, nil)
	rNoCode.AddCookie(bindCookie)
	ServeForTest(h.gw, rw, rNoCode)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("missing code = %d, want 400", rw.Code)
	}

	// Valid state + code, but the org's Google config is gone (not enabled) ->
	// signInError -> JSON 400 not_configured.
	state2, bind2 := boundGoogleState(t, "acme", "/", "", false)
	rw = httptest.NewRecorder()
	rNotEnabled := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state2+"&code=abc", nil)
	rNotEnabled.AddCookie(bind2)
	ServeForTest(h.gw, rw, rNotEnabled)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("not-enabled config = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	// Admin "Test" sign-in: a state minted with test=true routes failures to
	// the friendly ?test_error= page (redirectTestError) instead of JSON.
	testState, bind3 := boundGoogleState(t, "acme", "/admin/sso", "", true)
	rw = httptest.NewRecorder()
	rTest := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+testState+"&code=abc", nil)
	rTest.AddCookie(bind3)
	ServeForTest(h.gw, rw, rTest)
	if rw.Code != http.StatusFound {
		t.Fatalf("test-mode not-configured = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	if loc := rw.Header().Get("Location"); !strings.Contains(loc, "test_error=not_configured") {
		t.Fatalf("test redirect = %q, want test_error=not_configured", loc)
	}

	// Admin "Test" sign-in where the user declined consent (?error=) also
	// routes to the test-error page (denied) — state is consumed up front.
	deniedState, _ := boundGoogleState(t, "acme", "/admin/sso", "", true)
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+deniedState+"&error=access_denied", nil))
	if rw.Code != http.StatusFound {
		t.Fatalf("test-mode denied = %d, want 302", rw.Code)
	}
	if loc := rw.Header().Get("Location"); !strings.Contains(loc, "test_error=denied") {
		t.Fatalf("denied redirect = %q, want test_error=denied", loc)
	}
}

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

	// email_verified + hd are now read from the SIGNED token (not userinfo), so
	// the stub must carry them the way Google's id_token does.
	extras := map[string]any{"email": signedEmail, "email_verified": emailVerified}
	if hd != "" {
		extras["hd"] = hd
	}
	installGoogleVerifier(t, clientID, stubGoogleVerifier{
		claims: auth.Claims{Extras: extras},
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
	state, bindCookie := boundGoogleState(t, "acme", "/dashboard", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
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
	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
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
	state, bindCookie := boundGoogleState(t, "acme", "/admin/sso", "", true)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
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
	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
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

	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
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

	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
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
	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
	r.AddCookie(bindCookie)
	r.Host = "app.dazyflow.test"
	ServeForTest(h.gw, rw, r)
	if rw.Code != http.StatusFound {
		t.Fatalf("auto-enroll callback = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	if _, err := h.gw.Memberships.GetMembership(context.Background(), "joiner@acme.test", "acme"); err != nil {
		t.Fatalf("auto-enroll did not create membership: %v", err)
	}
}

func TestPendingInvitation_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	ctx := context.Background()

	// Nil store -> no invite.
	if _, ok := h.gw.authAPI().pendingInvitation(ctx, "a@x.com", "t"); ok {
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

	if inv, ok := h.gw.authAPI().pendingInvitation(ctx, "A@X.com", "acme"); !ok || inv.Token != "i1" {
		t.Fatalf("pending invite = %+v ok=%v", inv, ok)
	}
	if _, ok := h.gw.authAPI().pendingInvitation(ctx, "old@x.com", "acme"); ok {
		t.Fatal("expired invite returned as pending")
	}
	if _, ok := h.gw.authAPI().pendingInvitation(ctx, "nobody@x.com", "acme"); ok {
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
	tn, ws, _, reason, _, _ := h.gw.authAPI().resolveActiveOrg(r, cfg, newUser, true, "n@x.com", googleSignInState{Tenant: "acme"})
	if reason != "" || tn != "home" || ws != "main" {
		t.Fatalf("new user resolve = %q/%q reason=%q", tn, ws, reason)
	}

	// Existing user, signing into home tenant (st.Tenant == user.Tenant).
	home := auth.User{Email: "h@x.com", Tenant: "acme", Workspace: "main"}
	if _, _, _, reason, _, _ := h.gw.authAPI().resolveActiveOrg(r, cfg, home, false, "h@x.com", googleSignInState{Tenant: "acme"}); reason != "" {
		t.Fatalf("home tenant resolve reason = %q", reason)
	}

	// Existing user with a membership in the target org.
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "m@x.com", Tenant: "acme", Workspace: "ws2", Roles: []core.Role{core.TeamRoleEditor()},
	})
	other := auth.User{Email: "m@x.com", Tenant: "home"}
	if tn, ws, _, reason, _, _ := h.gw.authAPI().resolveActiveOrg(r, cfg, other, false, "m@x.com", googleSignInState{Tenant: "acme"}); reason != "" || tn != "acme" || ws != "ws2" {
		t.Fatalf("membership resolve = %q/%q reason=%q", tn, ws, reason)
	}

	// Existing user, no membership, no domain match, no invite -> not_invited.
	stranger := auth.User{Email: "s@x.com", Tenant: "home"}
	_, _, _, reason, status, _ := h.gw.authAPI().resolveActiveOrg(r, cfg, stranger, false, "s@x.com", googleSignInState{Tenant: "acme"})
	if reason != "not_invited" || status != http.StatusForbidden {
		t.Fatalf("stranger resolve reason=%q status=%d, want not_invited/403", reason, status)
	}

	// Domain-authorized auto-join.
	domainCfg := auth.OrgAuthConfig{Tenant: "acme", GoogleWorkspaceDomain: "acme.com"}
	dom := auth.User{Email: "d@acme.com", Tenant: "home"}
	tn, ws, roles, reason, _, _ := h.gw.authAPI().resolveActiveOrg(r, domainCfg, dom, false, "d@acme.com", googleSignInState{Tenant: "acme"})
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
	tn, ws, _, reason, _, _ = h.gw.authAPI().resolveActiveOrg(r, cfg, invUser, false, "i@x.com", googleSignInState{Tenant: "acme"})
	if reason != "" || tn != "acme" || ws != "wsInv" {
		t.Fatalf("invite join = %q/%q reason=%q", tn, ws, reason)
	}
}

func TestSignInError_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Non-test: writes a JSON error with the given status.
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/cb", nil)
	h.gw.authAPI().signInError(rw, r, googleSignInState{}, "exchange_failed", http.StatusBadGateway, "boom")
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("non-test signInError = %d, want 502", rw.Code)
	}

	// Test mode: redirects to the SSO settings page with a test_error code.
	rw = httptest.NewRecorder()
	h.gw.authAPI().signInError(rw, r, googleSignInState{Test: true, ReturnTo: "/admin/sso"}, "invalid_grant", http.StatusForbidden, "x")
	if rw.Code != http.StatusFound {
		t.Fatalf("test signInError = %d, want 302", rw.Code)
	}
	if loc := rw.Header().Get("Location"); loc == "" || loc[:11] != "/admin/sso?" {
		t.Fatalf("redirect location = %q", rw.Header().Get("Location"))
	}
}

// boundGoogleState mints sign-in state together with the browser-binding
// cookie the callback requires. Tests that drive the callback must attach the
// returned cookie to the request: a callback arriving without it is precisely
// the login-CSRF attempt the binding exists to reject, so omitting it is a
// 400 rather than a convenience.
func boundGoogleState(t *testing.T, tenant, returnTo, host string, test bool) (string, *http.Cookie) {
	t.Helper()
	binding, err := newOAuthBinding()
	if err != nil {
		t.Fatalf("newOAuthBinding: %v", err)
	}
	state, err := mintGoogleState(tenant, returnTo, host, binding, test)
	if err != nil {
		t.Fatalf("mintGoogleState: %v", err)
	}
	return state, &http.Cookie{Name: googleSignInCookie, Value: binding}
}

// TestGoogleSignInStart_SetsBindingCookie asserts the start leg actually mints
// the binding — the callback gate is worthless if nothing sets the cookie.
func TestGoogleSignInStart_SetsBindingCookie(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{
		Tenant: "acme", GoogleClientID: "c.apps.googleusercontent.com", GoogleClientSecret: "s",
	})

	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/start?tenant=acme", nil))
	if rw.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	var got *http.Cookie
	for _, c := range rw.Result().Cookies() {
		if c.Name == googleSignInCookie {
			got = c
		}
	}
	if got == nil {
		t.Fatal("start set no binding cookie")
	}
	if len(got.Value) < 32 {
		t.Errorf("binding = %q, want >=32 chars of entropy", got.Value)
	}
	if !got.HttpOnly {
		t.Error("binding cookie must be HttpOnly")
	}
	if got.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax (must survive the redirect back)", got.SameSite)
	}
}

// TestGoogleSignInCallback_RejectsUnboundBrowser is the login-CSRF regression.
// The attacker holds a valid state token (they started the flow) and a valid
// authorization code for THEIR Google account, and gets the victim to load the
// callback. Without a matching binding cookie the victim's browser must not
// come away holding a session.
func TestGoogleSignInCallback_RejectsUnboundBrowser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie at all", nil},
		{"wrong binding", &http.Cookie{Name: googleSignInCookie, Value: "not-the-nonce"}},
		{"empty binding", &http.Cookie{Name: googleSignInCookie, Value: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := googleCallbackEnv(t, "attacker@acme.test", "", true)
			state, _ := boundGoogleState(t, "acme", "/dashboard", "", false)

			rw := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
			if tc.cookie != nil {
				r.AddCookie(tc.cookie)
			}
			r.Host = "app.dazyflow.test"
			ServeForTest(h.gw, rw, r)

			if rw.Code != http.StatusBadRequest {
				t.Fatalf("callback = %d, want 400; body=%s", rw.Code, rw.Body.String())
			}
			for _, c := range rw.Result().Cookies() {
				if c.Name == googleSignInCookie {
					continue // the gate expires its own cookie; that's fine
				}
				if c.Value != "" && c.MaxAge >= 0 {
					t.Fatalf("unbound callback issued cookie %q — session leaked to the victim's browser", c.Name)
				}
			}
			// And no account was provisioned off the back of it.
			if _, err := h.gw.Users.GetByEmail(context.Background(), "attacker@acme.test"); err == nil {
				t.Error("unbound callback created a user")
			}
		})
	}
}

// TestGoogleSignInCallback_StateIsSingleUse pins that a binding cookie can't be
// replayed: the state is consumed even by the rejected attempt, so a second
// request carrying the correct cookie still fails.
func TestGoogleSignInCallback_StateIsSingleUse(t *testing.T) {
	h := googleCallbackEnv(t, "replay@acme.test", "", true)
	state, bindCookie := boundGoogleState(t, "acme", "/", "", false)

	for i, wantCode := range []int{http.StatusFound, http.StatusBadRequest} {
		rw := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state+"&code=abc", nil)
		r.AddCookie(bindCookie)
		r.Host = "app.dazyflow.test"
		ServeForTest(h.gw, rw, r)
		if rw.Code != wantCode {
			t.Fatalf("attempt %d = %d, want %d; body=%s", i+1, rw.Code, wantCode, rw.Body.String())
		}
	}
}
