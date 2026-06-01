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
		{"acme.hazyflow.app", "hazyflow.app", true},
		{"a.b.hazyflow.app", "hazyflow.app", true}, // multi-level still a subdomain
		{"hazyflow.app", "hazyflow.app", false},    // apex is not a subdomain
		{"evilhazyflow.app", "hazyflow.app", false},
		{"acme.hazyflow.app.evil.com", "hazyflow.app", false},
		{"ACME.HazyFlow.App", "hazyflow.app", true}, // case-insensitive
		{"", "hazyflow.app", false},
		{"acme.hazyflow.app", "", false},
	}
	for _, c := range cases {
		if got := hostIsSubdomainOf(c.host, c.domain); got != c.want {
			t.Errorf("hostIsSubdomainOf(%q, %q) = %v, want %v", c.host, c.domain, got, c.want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	h := &HTTPGateway{
		AllowedOrigins: []string{"https://hazyflow.app"},
		WildcardDomain: "hazyflow.app",
	}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://hazyflow.app", true},      // exact apex
		{"https://acme.hazyflow.app", true}, // wildcard subdomain
		{"https://a.b.hazyflow.app", true},  // nested subdomain
		{"https://evil.com", false},         // unrelated
		{"https://evilhazyflow.app", false}, // suffix-but-not-subdomain
		{"http://acme.hazyflow.app", true},  // scheme not pinned for subdomains (Origin is browser-set)
		{"https://hazyflow.app.evil.com", false},
	}
	for _, c := range cases {
		if got := h.originAllowed(c.origin); got != c.want {
			t.Errorf("originAllowed(%q) = %v, want %v", c.origin, got, c.want)
		}
	}

	// With the feature off, only the exact list matches.
	off := &HTTPGateway{AllowedOrigins: []string{"https://hazyflow.app"}}
	if off.originAllowed("https://acme.hazyflow.app") {
		t.Error("subdomain should not be allowed when WildcardDomain is empty")
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
	h := &HTTPGateway{WildcardDomain: "hazyflow.app"}
	mk := func(host string) string {
		r := httptest.NewRequest("GET", "/api/v1/auth/google/start", nil)
		r.Host = host
		return h.signInStartHost(r)
	}
	if got := mk("acme.hazyflow.app"); got != "acme.hazyflow.app" {
		t.Errorf("subdomain host = %q, want acme.hazyflow.app", got)
	}
	if got := mk("hazyflow.app"); got != "hazyflow.app" {
		t.Errorf("apex host = %q, want hazyflow.app", got)
	}
	if got := mk("someone-else.com"); got != "" {
		t.Errorf("foreign host = %q, want empty", got)
	}

	// Feature off: never track a host.
	off := &HTTPGateway{}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "acme.hazyflow.app"
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
