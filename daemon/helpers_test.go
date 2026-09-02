// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestGoogleStateRoundTrip(t *testing.T) {
	t.Parallel()
	s, err := mintGoogleState("acme", "/dash", "acme.example.com", "bind-nonce", true)
	if err != nil {
		t.Fatalf("mintGoogleState: %v", err)
	}
	if s == "" {
		t.Fatal("empty state")
	}
	st, ok := consumeGoogleState(s)
	if !ok {
		t.Fatal("consumeGoogleState: not found")
	}
	if st.Tenant != "acme" || st.ReturnTo != "/dash" || st.Host != "acme.example.com" ||
		!st.Test || st.Binding != "bind-nonce" {
		t.Fatalf("state = %+v", st)
	}
	// Single-use: a second consume misses.
	if _, ok := consumeGoogleState(s); ok {
		t.Fatal("state consumable twice")
	}
	// Unknown state misses.
	if _, ok := consumeGoogleState("deadbeef"); ok {
		t.Fatal("unknown state found")
	}
}

func TestClassifyGoogleError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{errors.New("oauth2: invalid_client foo"), "invalid_client"},
		{errors.New("redirect_uri_mismatch here"), "redirect_uri_mismatch"},
		{errors.New("server said invalid_grant"), "invalid_grant"},
		{errors.New("unauthorized_client!"), "unauthorized_client"},
		{errors.New("something else entirely"), "exchange_failed"},
	}
	for _, c := range cases {
		if got := classifyGoogleError(c.err); got != c.want {
			t.Errorf("classifyGoogleError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestAppendQuery(t *testing.T) {
	t.Parallel()
	if got := appendQuery("/dash", "x", "a b"); got != "/dash?x=a+b" {
		t.Errorf("appendQuery no-query = %q", got)
	}
	if got := appendQuery("/dash?y=1", "x", "2"); got != "/dash?y=1&x=2" {
		t.Errorf("appendQuery with-query = %q", got)
	}
}

func TestSafeReturnPathCov(t *testing.T) {
	t.Parallel()
	good := []string{"/", "/dash", "/a/b?c=d"}
	bad := []string{"", "//evil.com", "/\\evil.com", "https://evil.com", "dash"}
	for _, p := range good {
		if !safeReturnPath(p) {
			t.Errorf("safeReturnPath(%q) = false, want true", p)
		}
	}
	for _, p := range bad {
		if safeReturnPath(p) {
			t.Errorf("safeReturnPath(%q) = true, want false", p)
		}
	}
}

func TestBareHostAndSameHost(t *testing.T) {
	t.Parallel()
	if got := bareHost("example.com:8080"); got != "example.com" {
		t.Errorf("bareHost with port = %q", got)
	}
	if got := bareHost("example.com"); got != "example.com" {
		t.Errorf("bareHost no port = %q", got)
	}
	if !sameHost("Example.com:80", "example.com:443") {
		t.Error("sameHost should ignore port and case")
	}
	if sameHost("a.com", "b.com") {
		t.Error("sameHost a vs b = true")
	}
}

func TestEmailDomainHelpers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		email      string
		wantDomain string
		wantOf     string
	}{
		{"alice@Example.COM", "example.com", "Example.COM"},
		{"noatsign", "", ""},
		{"trailing@", "", ""},
		{"@leading.com", "leading.com", ""},
	}
	for _, c := range cases {
		if got := emailDomain(c.email); got != c.wantDomain {
			t.Errorf("emailDomain(%q) = %q, want %q", c.email, got, c.wantDomain)
		}
		if got := emailDomainOf(c.email); got != c.wantOf {
			t.Errorf("emailDomainOf(%q) = %q, want %q", c.email, got, c.wantOf)
		}
	}
}

func TestGoogleRedirectURI(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.svc.PublicBaseURL = "https://app.example.com/"
	if got := h.gw.authAPI().googleRedirectURI(); got != "https://app.example.com/api/v1/auth/google/callback" {
		t.Errorf("googleRedirectURI = %q", got)
	}
}

func TestSignInRedirectURL(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	r := httptest.NewRequest("GET", "https://apex.example.com/api/v1/auth/google/callback", nil)

	// No host tracked -> path unchanged.
	if got := h.gw.authAPI().signInRedirectURL(r, googleSignInState{}, "/dash"); got != "/dash" {
		t.Errorf("no host = %q, want /dash", got)
	}
	// Same host -> path unchanged.
	if got := h.gw.authAPI().signInRedirectURL(r, googleSignInState{Host: "apex.example.com"}, "/dash"); got != "/dash" {
		t.Errorf("same host = %q, want /dash", got)
	}
	// Different host -> absolute URL on the start host. Request is TLS so https.
	got := h.gw.authAPI().signInRedirectURL(r, googleSignInState{Host: "org.example.com"}, "/dash")
	if got != "https://org.example.com/dash" {
		t.Errorf("cross host = %q, want https://org.example.com/dash", got)
	}
}

func TestRedactionHelpers(t *testing.T) {
	t.Parallel()
	in := map[string]any{
		"name":        "ok",
		"api_token":   "shh",
		"nested":      map[string]any{"password": "p", "city": "nyc"},
		"list":        []any{map[string]any{"client_secret": "x"}, "plain"},
		"webhook_url": "https://hook",
		"count":       3,
	}
	out := redactParams(in)
	if out["name"] != "ok" || out["count"] != 3 {
		t.Errorf("non-secret scalars altered: %+v", out)
	}
	if out["api_token"] != redactedValue || out["webhook_url"] != redactedValue {
		t.Errorf("secret keys not redacted: %+v", out)
	}
	nested := out["nested"].(map[string]any)
	if nested["password"] != redactedValue || nested["city"] != "nyc" {
		t.Errorf("nested redaction wrong: %+v", nested)
	}
	list := out["list"].([]any)
	first := list[0].(map[string]any)
	if first["client_secret"] != redactedValue {
		t.Errorf("list element not redacted: %+v", list)
	}
	if list[1] != "plain" {
		t.Errorf("list scalar altered: %+v", list)
	}

	// redactValueDeep on a bare scalar passes through.
	if redactValueDeep(42) != 42 {
		t.Error("redactValueDeep scalar changed")
	}
}

func TestLooksSecretKey(t *testing.T) {
	t.Parallel()
	secret := []string{"SECRET", "api_token", "PASSWORD", "passwd", "apikey", "access_key",
		"private_key", "client_secret", "credential", "X-Auth", "bearer_tok", "webhook",
		"cookie", "session_id", "DSN", "connection_string", "signature"}
	for _, k := range secret {
		if !looksSecretKey(k) {
			t.Errorf("looksSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"name", "count", "city", "url"} {
		if looksSecretKey(k) {
			t.Errorf("looksSecretKey(%q) = true, want false", k)
		}
	}
}

func TestRedactEnv(t *testing.T) {
	t.Parallel()
	if got := redactEnv(nil); got != nil {
		t.Errorf("redactEnv(nil) = %v, want nil", got)
	}
	out := redactEnv(map[string]string{"HOME": "/root", "API_TOKEN": "shh"})
	if out["HOME"] != "/root" || out["API_TOKEN"] != redactedValue {
		t.Errorf("redactEnv = %+v", out)
	}
}

func TestCatalogStringHelpers(t *testing.T) {
	t.Parallel()
	if got := toString("hi"); got != "hi" {
		t.Errorf("toString string = %q", got)
	}
	if got := toString(42); got != "42" {
		t.Errorf("toString int = %q", got)
	}
	if got := jsonStringOf("quoted"); got != "quoted" {
		t.Errorf("jsonStringOf = %q", got)
	}
}
