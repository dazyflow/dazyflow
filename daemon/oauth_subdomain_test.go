// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Connecting an app over OAuth from an ORG SUBDOMAIN.
//
// The provider only ever redirects to the apex callback — that is the one
// registered redirect_uri — so a flow begun on "acme.dazyflow.app" finishes on
// "dazyflow.app". Two things have to survive that hop, and neither did:
//
//   - the browser-binding cookie, which was host-only and therefore simply not
//     sent to the apex, so every such flow was rejected with "OAuth state did
//     not match this browser session";
//   - the return trip, which was path-relative and so left the user on the
//     apex, where their host-only session cookie does not exist — the
//     connection worked but they appeared to be signed out.

package daemon

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	apexHost = "dazyflow.app"
	orgHost  = "acme.dazyflow.app"
)

// authorizeFrom starts a flow with the browser on the given host.
func authorizeFrom(t *testing.T, h *gatewayHarness, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = host
	req.Header.Set("Authorization", "Bearer "+h.token)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func bindingCookie(rw *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rw.Result().Cookies() {
		if c.Name == oauthStateCookie {
			return c
		}
	}
	return nil
}

// attachCookiesFor adds the cookies a real browser would send to host,
// applying RFC 6265's scoping rule. httptest does not model cookie storage, so
// without this a test can hand the callback a cookie the browser would never
// have sent — which is exactly the bug, and would go unnoticed.
func attachCookiesFor(req *http.Request, setBy string, resp *httptest.ResponseRecorder, host string) {
	for _, c := range resp.Result().Cookies() {
		send := false
		switch {
		case c.Domain == "":
			send = strings.EqualFold(host, setBy) // host-only
		default:
			d := strings.TrimPrefix(c.Domain, ".")
			send = strings.EqualFold(host, d) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(d))
		}
		if send {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
}

func stateFrom(t *testing.T, rw *httptest.ResponseRecorder) string {
	t.Helper()
	u, err := url.Parse(rw.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("authorize redirect carried no state")
	}
	return state
}

// The cookie has to be readable at the apex, or the callback can never match it.
func TestOAuthSubdomain_BindingCookieReachesTheApexCallback(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost

	rw := authorizeFrom(t, h, orgHost, "/api/v1/oauth/test/authorize?account=main&return_to=/apps")
	if rw.Code != http.StatusFound {
		t.Fatalf("authorize status=%d body=%s", rw.Code, rw.Body.String())
	}
	c := bindingCookie(rw)
	if c == nil {
		t.Fatal("authorize set no binding cookie")
	}
	if c.Domain != apexHost {
		t.Fatalf("binding cookie Domain=%q, want %q — a host-only cookie is not sent to the apex callback", c.Domain, apexHost)
	}
}

// The regression itself, end to end: start on the subdomain, come back on the
// apex carrying the cookie the browser would have kept.
func TestOAuthSubdomain_CallbackFromApexIsAccepted(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost

	auth := authorizeFrom(t, h, orgHost, "/api/v1/oauth/test/authorize?account=main&return_to=/apps")
	state := stateFrom(t, auth)

	cb := httptest.NewRequest("GET", "/api/v1/oauth/test/callback?code=abc&state="+url.QueryEscape(state), nil)
	cb.Host = apexHost // the provider always lands on the apex
	attachCookiesFor(cb, orgHost, auth, apexHost)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, cb)

	if rw.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s — want a redirect, not a rejection", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "did not match this browser session") {
		t.Fatal("callback rejected a flow started on an org subdomain")
	}
}

// And it must put the user back on the subdomain, where their session lives.
func TestOAuthSubdomain_CallbackReturnsToTheOriginatingHost(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost

	auth := authorizeFrom(t, h, orgHost, "/api/v1/oauth/test/authorize?account=main&return_to=/apps")
	state := stateFrom(t, auth)

	cb := httptest.NewRequest("GET", "/api/v1/oauth/test/callback?code=abc&state="+url.QueryEscape(state), nil)
	cb.Host = apexHost
	attachCookiesFor(cb, orgHost, auth, apexHost)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, cb)

	loc := rw.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse callback redirect %q: %v", loc, err)
	}
	if u.Host != orgHost {
		t.Fatalf("callback sent the user to %q, want back to %q — on the apex their session cookie does not exist", loc, orgHost)
	}
	if u.Path != "/apps" || u.Query().Get("oauth") != "success" {
		t.Fatalf("redirect = %q, want /apps with oauth=success", loc)
	}
}

// A single-host deployment must be untouched: host-only cookie, relative
// redirect, exactly as before.
func TestOAuthSubdomain_SingleHostDeploymentIsUnchanged(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = "" // no per-org subdomains

	auth := authorizeFrom(t, h, "app.example.test", "/api/v1/oauth/test/authorize?account=main&return_to=/apps")
	c := bindingCookie(auth)
	if c == nil {
		t.Fatal("authorize set no binding cookie")
	}
	if c.Domain != "" {
		t.Fatalf("binding cookie Domain=%q on a single-host deployment, want host-only", c.Domain)
	}
	state := stateFrom(t, auth)

	cb := httptest.NewRequest("GET", "/api/v1/oauth/test/callback?code=abc&state="+url.QueryEscape(state), nil)
	cb.Host = "app.example.test"
	attachCookiesFor(cb, "app.example.test", auth, "app.example.test")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, cb)
	loc := rw.Header().Get("Location")
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		t.Fatalf("redirect = %q, want a path-relative redirect when there are no subdomains", loc)
	}
}

// A host that is not ours must never be redirected to, however it got into the
// state — the callback is unauthenticated, so this is the open-redirect guard.
func TestOAuthSubdomain_ForeignHostIsNotRedirectedTo(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost

	api := h.gw.oauthAPI()
	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/oauth/test/callback", nil)
	r.Host = apexHost
	api.redirectBack(rw, r, pendingOAuth{
		returnTo: "/apps", account: "main", host: "evil.example.com",
	}, "test", "success", "")

	loc := rw.Header().Get("Location")
	if strings.Contains(loc, "evil.example.com") {
		t.Fatalf("callback redirected to a foreign host: %q", loc)
	}
}

// Starting on the apex itself needs no absolute redirect — it is already there.
func TestOAuthSubdomain_ApexFlowStaysRelative(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost

	auth := authorizeFrom(t, h, apexHost, "/api/v1/oauth/test/authorize?account=main&return_to=/apps")
	state := stateFrom(t, auth)
	cb := httptest.NewRequest("GET", "/api/v1/oauth/test/callback?code=abc&state="+url.QueryEscape(state), nil)
	cb.Host = apexHost
	attachCookiesFor(cb, apexHost, auth, apexHost)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, cb)
	loc := rw.Header().Get("Location")
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		t.Fatalf("apex flow redirected absolutely to %q, want relative", loc)
	}
}

// The same defect, in the SSO flow's own binding cookie: it is widened to the
// apex when a sign-in starts on an org subdomain, but the clear did not match,
// so the callback "expired" a host-only cookie that was never there and left
// the real one live for its full ten minutes. Set and clear now share one rule.
func TestSignInCookie_ClearMatchesTheDomainItWasSetWith(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost
	h.gw.svc.PublicBaseURL = "https://" + apexHost
	api := h.gw.authAPI()

	// A flow that began on an org subdomain: the cookie is scoped to the apex.
	set := httptest.NewRecorder()
	api.setGoogleSignInCookie(set, "nonce-abc", orgHost)
	setC := cookieNamed(set, googleSignInCookie)
	if setC == nil || setC.Domain != apexHost {
		t.Fatalf("set cookie Domain=%v, want %q", setC, apexHost)
	}

	// Clearing it must target the same Domain, or the browser keeps the
	// original and only adds an expired host-only twin.
	clear := httptest.NewRecorder()
	api.clearGoogleSignInCookie(clear, orgHost)
	clearC := cookieNamed(clear, googleSignInCookie)
	if clearC == nil {
		t.Fatal("clear set no cookie")
	}
	if clearC.Domain != setC.Domain {
		t.Fatalf("clear Domain=%q but it was set with %q — the real cookie survives", clearC.Domain, setC.Domain)
	}
	if clearC.MaxAge >= 0 {
		t.Fatalf("clear MaxAge=%d, want negative", clearC.MaxAge)
	}
}

// An apex-origin sign-in stays host-only on both halves.
func TestSignInCookie_ApexFlowStaysHostOnly(t *testing.T) {
	t.Parallel()
	h, _ := newOAuthHarness(t)
	h.gw.WildcardDomain = apexHost
	h.gw.svc.PublicBaseURL = "https://" + apexHost
	api := h.gw.authAPI()

	set := httptest.NewRecorder()
	api.setGoogleSignInCookie(set, "nonce-abc", apexHost)
	clear := httptest.NewRecorder()
	api.clearGoogleSignInCookie(clear, apexHost)

	if c := cookieNamed(set, googleSignInCookie); c == nil || c.Domain != "" {
		t.Fatalf("apex sign-in cookie Domain=%v, want host-only", c)
	}
	if c := cookieNamed(clear, googleSignInCookie); c == nil || c.Domain != "" {
		t.Fatalf("apex clear Domain=%v, want host-only", c)
	}
}

func cookieNamed(rw *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rw.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
