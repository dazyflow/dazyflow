// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"git.sr.ht/~klahr/dazyflow/auth"
)

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
	if _, reason, status, _ := h.gw.verifyGoogleIDToken(ctx, cfg, "", googleUserInfo{}); reason != "no_id_token" || status != http.StatusBadGateway {
		t.Fatalf("empty token reason=%q status=%d", reason, status)
	}

	// Verifier rejects the token.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{err: errors.New("bad sig")})
	if _, reason, status, _ := h.gw.verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{}); reason != "id_token_invalid" || status != http.StatusForbidden {
		t.Fatalf("invalid token reason=%q status=%d", reason, status)
	}

	// Verified, but the signed token has no email claim.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{claims: auth.Claims{Subject: "s"}})
	if _, reason, status, _ := h.gw.verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{}); reason != "no_email" || status != http.StatusBadGateway {
		t.Fatalf("no-email reason=%q status=%d", reason, status)
	}

	// Verified email disagrees with the userinfo email -> mismatch.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{claims: auth.Claims{Extras: map[string]any{"email": "Signed@Acme.test"}}})
	if _, reason, status, _ := h.gw.verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{Email: "other@acme.test"}); reason != "email_mismatch" || status != http.StatusForbidden {
		t.Fatalf("mismatch reason=%q status=%d", reason, status)
	}

	// Happy path: signed email is lower-cased and returned; userinfo agrees.
	installGoogleVerifier(t, clientID, stubGoogleVerifier{claims: auth.Claims{Extras: map[string]any{"email": "User@Acme.test"}}})
	email, reason, _, _ := h.gw.verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{Email: "user@acme.test"})
	if reason != "" || email != "user@acme.test" {
		t.Fatalf("happy path email=%q reason=%q", email, reason)
	}

	// Userinfo email omitted is allowed (signed token is authoritative).
	email, reason, _, _ = h.gw.verifyGoogleIDToken(ctx, cfg, "tok", googleUserInfo{})
	if reason != "" || email != "user@acme.test" {
		t.Fatalf("empty-userinfo email=%q reason=%q", email, reason)
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
	user, isNew, ok := h.gw.resolveSignInUser(rw, r, "new@acme.test", st)
	if !ok || !isNew || user.Tenant != "acme" || user.Email != "new@acme.test" {
		t.Fatalf("new user = %+v isNew=%v ok=%v", user, isNew, ok)
	}
	if prof, err := profiles.GetOrgProfile(r.Context(), "acme"); err != nil || prof.DisplayName == "" {
		t.Errorf("org profile not seeded: %+v err=%v", prof, err)
	}

	// Second sign-in for the same email: existing user, not new.
	rw = httptest.NewRecorder()
	user, isNew, ok = h.gw.resolveSignInUser(rw, r, "new@acme.test", st)
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
	h.gw.completeSignIn(rw, r, googleSignInState{ReturnTo: "/dashboard"}, sess, "tok-abc")
	if rw.Code != http.StatusFound || rw.Header().Get("Location") != "/dashboard" {
		t.Fatalf("same-host redirect = %d %q", rw.Code, rw.Header().Get("Location"))
	}
	if len(rw.Result().Cookies()) == 0 {
		t.Error("expected a session cookie to be set inline")
	}

	// An unsafe return path falls back to "/".
	rw = httptest.NewRecorder()
	h.gw.completeSignIn(rw, r, googleSignInState{ReturnTo: "https://evil.test/x"}, sess, "tok")
	if loc := rw.Header().Get("Location"); loc != "/" {
		t.Fatalf("unsafe return path redirect = %q, want /", loc)
	}

	// Different host (per-org subdomain): bounces through /auth/handoff with a
	// one-time token instead of setting an apex cookie.
	rw = httptest.NewRecorder()
	h.gw.completeSignIn(rw, r, googleSignInState{Host: "acme.dazyflow.test", ReturnTo: "/x"}, sess, "tok")
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
	state, err := mintGoogleState("acme", "/", "", false)
	if err != nil {
		t.Fatalf("mint state: %v", err)
	}
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state, nil))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("missing code = %d, want 400", rw.Code)
	}

	// Valid state + code, but the org's Google config is gone (not enabled) ->
	// signInError -> JSON 400 not_configured.
	state2, _ := mintGoogleState("acme", "/", "", false)
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+state2+"&code=abc", nil))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("not-enabled config = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	// Admin "Test" sign-in: a state minted with test=true routes failures to
	// the friendly ?test_error= page (redirectTestError) instead of JSON.
	testState, _ := mintGoogleState("acme", "/admin/sso", "", true)
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+testState+"&code=abc", nil))
	if rw.Code != http.StatusFound {
		t.Fatalf("test-mode not-configured = %d, want 302; body=%s", rw.Code, rw.Body.String())
	}
	if loc := rw.Header().Get("Location"); !strings.Contains(loc, "test_error=not_configured") {
		t.Fatalf("test redirect = %q, want test_error=not_configured", loc)
	}

	// Admin "Test" sign-in where the user declined consent (?error=) also
	// routes to the test-error page (denied) — state is consumed up front.
	deniedState, _ := mintGoogleState("acme", "/admin/sso", "", true)
	rw = httptest.NewRecorder()
	ServeForTest(h.gw, rw, httptest.NewRequest("GET", "/api/v1/auth/google/callback?state="+deniedState+"&error=access_denied", nil))
	if rw.Code != http.StatusFound {
		t.Fatalf("test-mode denied = %d, want 302", rw.Code)
	}
	if loc := rw.Header().Get("Location"); !strings.Contains(loc, "test_error=denied") {
		t.Fatalf("denied redirect = %q, want test_error=denied", loc)
	}
}
