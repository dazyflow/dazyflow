// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestHostIsSubdomainOf(t *testing.T) {
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"acme.dazyflow.app", "dazyflow.app", true},
		{"a.b.dazyflow.app", "dazyflow.app", true}, // multi-level still a subdomain
		{"dazyflow.app", "dazyflow.app", false},    // apex is not a subdomain
		{"evildazyflow.app", "dazyflow.app", false},
		{"acme.dazyflow.app.evil.com", "dazyflow.app", false},
		{"ACME.DazyFlow.App", "dazyflow.app", true}, // case-insensitive
		{"", "dazyflow.app", false},
		{"acme.dazyflow.app", "", false},
	}
	for _, c := range cases {
		if got := hostIsSubdomainOf(c.host, c.domain); got != c.want {
			t.Errorf("hostIsSubdomainOf(%q, %q) = %v, want %v", c.host, c.domain, got, c.want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	h := &HTTPGateway{
		AllowedOrigins: []string{"https://dazyflow.app"},
		WildcardDomain: "dazyflow.app",
	}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://dazyflow.app", true},      // exact apex
		{"https://acme.dazyflow.app", true}, // wildcard subdomain
		{"https://a.b.dazyflow.app", true},  // nested subdomain
		{"https://evil.com", false},         // unrelated
		{"https://evildazyflow.app", false}, // suffix-but-not-subdomain
		{"http://acme.dazyflow.app", true},  // scheme not pinned for subdomains (Origin is browser-set)
		{"https://dazyflow.app.evil.com", false},
	}
	for _, c := range cases {
		if got := h.originAllowed(c.origin); got != c.want {
			t.Errorf("originAllowed(%q) = %v, want %v", c.origin, got, c.want)
		}
	}

	// With the feature off, only the exact list matches.
	off := &HTTPGateway{AllowedOrigins: []string{"https://dazyflow.app"}}
	if off.originAllowed("https://acme.dazyflow.app") {
		t.Error("subdomain should not be allowed when WildcardDomain is empty")
	}

	// An overly-broad wildcard ("com") must trust nobody, not every ".com".
	broad := &HTTPGateway{WildcardDomain: "com"}
	for _, o := range []string{"https://evil.com", "https://anything.com"} {
		if broad.originAllowed(o) {
			t.Errorf("originAllowed(%q) = true with WildcardDomain=\"com\"; a single-label wildcard must match nothing", o)
		}
	}
}

func TestIsValidWildcardDomain(t *testing.T) {
	cases := []struct {
		domain string
		want   bool
	}{
		{"dazyflow.app", true},
		{"a.b.example.com", true},
		{"DazyFlow.App", true},   // case-insensitive
		{".dazyflow.app.", true}, // surrounding dots trimmed
		{"", false},              // disabled
		{"com", false},           // single label (public suffix)
		{"localhost", false},     // single label
		{"dazyflow..app", false}, // empty inner label
		{".", false},
	}
	for _, c := range cases {
		if got := IsValidWildcardDomain(c.domain); got != c.want {
			t.Errorf("IsValidWildcardDomain(%q) = %v, want %v", c.domain, got, c.want)
		}
	}
}

func TestSafeReturnPath(t *testing.T) {
	good := []string{"/", "/flows", "/invite/abc?x=1"}
	bad := []string{"", "//evil.com", "/\\evil.com", "https://evil.com", "evil"}
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

func TestSignInStartHost(t *testing.T) {
	h := &HTTPGateway{WildcardDomain: "dazyflow.app"}
	mk := func(host string) string {
		r := httptest.NewRequest("GET", "/api/v1/auth/google/start", nil)
		r.Host = host
		return h.signInStartHost(r)
	}
	if got := mk("acme.dazyflow.app"); got != "acme.dazyflow.app" {
		t.Errorf("subdomain host = %q, want acme.dazyflow.app", got)
	}
	if got := mk("dazyflow.app"); got != "dazyflow.app" {
		t.Errorf("apex host = %q, want dazyflow.app", got)
	}
	if got := mk("someone-else.com"); got != "" {
		t.Errorf("foreign host = %q, want empty", got)
	}

	// Feature off: never track a host.
	off := &HTTPGateway{}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "acme.dazyflow.app"
	if got := off.signInStartHost(r); got != "" {
		t.Errorf("host tracked with feature off = %q, want empty", got)
	}
}

func TestHandoffStoreSingleUse(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	code, err := mintHandoff("sess-token-123", exp)
	if err != nil {
		t.Fatalf("mintHandoff: %v", err)
	}
	entry, ok := consumeHandoff(code)
	if !ok {
		t.Fatal("first consume should succeed")
	}
	if entry.Token != "sess-token-123" {
		t.Errorf("token = %q, want sess-token-123", entry.Token)
	}
	if _, ok := consumeHandoff(code); ok {
		t.Error("second consume should fail (single-use)")
	}
	if _, ok := consumeHandoff("never-minted"); ok {
		t.Error("unknown code should not consume")
	}
}
