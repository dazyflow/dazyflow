// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Forwarding an apex deep link to the org's own subdomain.
//
// Mail carries apex links on purpose — the apex is the one host that stays
// valid when an org renames or drops its subdomain label, and an emailed link
// outlives that. But session cookies are host-only, so a member of an org that
// HAS a subdomain arrived at the apex signed out and had to authenticate a
// second time, on a second host.
//
// The link already names the org, so the apex forwards the whole request to
// where that member's session lives. These are the guards on a redirect that is
// driven by a URL parameter.

package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
)

func bounceAPI(t *testing.T, wildcard string) (*staticAPI, *recordingOrgProfiles) {
	t.Helper()
	profiles := newRecordingOrgProfiles()
	if err := profiles.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "acme", Subdomain: "acme"}); err != nil {
		t.Fatal(err)
	}
	// An org that has claimed no label.
	if err := profiles.PutOrgProfile(t.Context(), auth.OrgProfile{Tenant: "plain"}); err != nil {
		t.Fatal(err)
	}
	return &staticAPI{
		svc:            &Service{PublicBaseURL: "https://" + wildcard},
		WildcardDomain: wildcard,
		Profiles:       profiles,
	}, profiles
}

func bounceFor(t *testing.T, api *staticAPI, method, host, target string) string {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	r.Host = host
	return api.orgBounceTarget(r)
}

// The case this exists for: a "View run details" link out of a failure email.
func TestOrgBounce_ApexDeepLinkGoesToTheOrgSubdomain(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc123?org=acme")
	want := "https://acme.dazyflow.app/runs/abc123?org=acme"
	if got != want {
		t.Fatalf("bounce = %q, want %q", got, want)
	}
}

// The port has to come across. Behind a proxy on 443 the browser sends none and
// none is added — which is why every other test here reads correctly without
// one, and why dropping the port went unnoticed until the redirect was followed
// by a real browser against a deployment on :8642 and refused at :80.
func TestOrgBounce_CarriesThePortAcross(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.test")
	api.svc.PublicBaseURL = "http://dazyflow.test:8642"

	got := bounceFor(t, api, "GET", "dazyflow.test:8642", "/runs/abc?org=acme")
	if got != "http://acme.dazyflow.test:8642/runs/abc?org=acme" {
		t.Fatalf("bounce = %q, want the port carried across", got)
	}
	// And the proxy shape, where the browser sends no port at all.
	if got := bounceFor(t, api, "GET", "dazyflow.test", "/runs/abc?org=acme"); got != "http://acme.dazyflow.test/runs/abc?org=acme" {
		t.Fatalf("bounce = %q, want no port invented", got)
	}
}

// Path and query have to survive, or the link stops being a deep link.
func TestOrgBounce_PreservesPathAndQuery(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	// The query rides across verbatim rather than being re-encoded, so a link
	// is forwarded exactly as it was mailed.
	got := bounceFor(t, api, "GET", "dazyflow.app", "/approvals?org=acme&filter=mine&page=2")
	if got != "https://acme.dazyflow.app/approvals?org=acme&filter=mine&page=2" {
		t.Fatalf("bounce = %q, lost part of the link", got)
	}
}

// Someone signed in ON the apex is already where they should be. Bouncing them
// would hand them a host where they have no session — the very problem this is
// meant to solve, inverted.
func TestOrgBounce_LeavesAnApexSessionAlone(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	ks := auth.NewMemKeyStore()
	_, token, err := auth.IssueAPIKey(ks, t.Context(), "k", "acme", "main", "u", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	api.svc.Auth = auth.Chain{&auth.APIKeyAuthenticator{Store: ks}}

	r := httptest.NewRequest("GET", "/runs/abc?org=acme", nil)
	r.Host = "dazyflow.app"
	r.Header.Set("Authorization", "Bearer "+token)
	if got := api.orgBounceTarget(r); got != "" {
		t.Fatalf("bounced a request that is already authenticated here: %q", got)
	}
}

// Already on the subdomain — bouncing again is an infinite redirect.
func TestOrgBounce_DoesNotLoopOnTheSubdomain(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	if got := bounceFor(t, api, "GET", "acme.dazyflow.app", "/runs/abc?org=acme"); got != "" {
		t.Fatalf("bounced a request already on the subdomain: %q", got)
	}
}

func TestOrgBounce_NoOrgParamIsLeftAlone(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc"); got != "" {
		t.Fatalf("bounced a link that names no org: %q", got)
	}
}

// An org with no claimed label has nowhere to go, and an unknown org must not
// be able to steer the redirect at all.
func TestOrgBounce_OrgWithoutASubdomainStaysOnTheApex(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc?org=plain"); got != "" {
		t.Fatalf("bounced an org that has claimed no subdomain: %q", got)
	}
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc?org=nosuchorg"); got != "" {
		t.Fatalf("bounced an unknown org: %q", got)
	}
}

// The host is built from OUR store plus configured apex, never from the
// request — so nothing in the URL can point it somewhere else.
func TestOrgBounce_CannotBeSteeredByTheRequest(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	for _, org := range []string{
		"evil.example.com",
		"acme.dazyflow.app.evil.example.com",
		"../acme",
		"acme%2Eevil",
		"//evil.example.com",
	} {
		r := httptest.NewRequest("GET", "/runs/abc", nil)
		q := r.URL.Query()
		q.Set("org", org)
		r.URL.RawQuery = q.Encode()
		r.Host = "dazyflow.app"
		if got := api.orgBounceTarget(r); got != "" {
			t.Fatalf("org=%q produced a redirect to %q", org, got)
		}
	}
}

// A label that is somehow no longer a valid DNS label must not become a host.
func TestOrgBounce_RejectsAnUnusableLabel(t *testing.T) {
	t.Parallel()
	api, profiles := bounceAPI(t, "dazyflow.app")
	profiles.saved["acme"] = auth.OrgProfile{Tenant: "acme", Subdomain: "not a label"}
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc?org=acme"); got != "" {
		t.Fatalf("built a host from an invalid label: %q", got)
	}
	profiles.saved["acme"] = auth.OrgProfile{Tenant: "acme", Subdomain: "   "}
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc?org=acme"); got != "" {
		t.Fatalf("built a host from a blank label: %q", got)
	}
}

// Single-host deployments must be untouched.
func TestOrgBounce_SingleHostDeploymentNeverBounces(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "")
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/runs/abc?org=acme"); got != "" {
		t.Fatalf("bounced with no wildcard domain configured: %q", got)
	}
}

// Only document navigations. A POST is not something to redirect across hosts,
// and an unregistered /api/ path is a 404, not a redirect.
func TestOrgBounce_OnlyGetAndNeverTheAPI(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	if got := bounceFor(t, api, "POST", "dazyflow.app", "/runs/abc?org=acme"); got != "" {
		t.Fatalf("bounced a POST: %q", got)
	}
	if got := bounceFor(t, api, "GET", "dazyflow.app", "/api/v1/runs/abc?org=acme"); got != "" {
		t.Fatalf("bounced an API path: %q", got)
	}
}

// And the wrapper actually redirects, rather than just computing a target.
func TestOrgBounce_WrapperRedirects(t *testing.T) {
	t.Parallel()
	api, _ := bounceAPI(t, "dazyflow.app")
	served := false
	h := api.withOrgBounce(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }))

	rw := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/runs/abc?org=acme", nil)
	r.Host = "dazyflow.app"
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302", rw.Code)
	}
	if loc := rw.Header().Get("Location"); loc != "https://acme.dazyflow.app/runs/abc?org=acme" {
		t.Fatalf("Location=%q", loc)
	}
	if served {
		t.Fatal("the wrapped handler ran as well as the redirect")
	}

	// And it passes everything else straight through.
	rw = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/runs/abc", nil)
	r.Host = "dazyflow.app"
	h.ServeHTTP(rw, r)
	if !served {
		t.Fatal("a request with nothing to bounce was not served")
	}
}
