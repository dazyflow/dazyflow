package net

import "testing"

func TestEgressAllowlist_Disabled(t *testing.T) {
	// No allowlist → everything public is allowed.
	if err := SetEgressAllowlist(nil); err != nil {
		t.Fatal(err)
	}
	if err := egressAllowed("https://anything.example.org/x"); err != nil {
		t.Errorf("disabled allowlist should permit all: %v", err)
	}
	// Whitespace-only entries also clear it.
	if err := SetEgressAllowlist([]string{"  ", ""}); err != nil {
		t.Fatal(err)
	}
	if err := egressAllowed("https://anything.example.org"); err != nil {
		t.Errorf("blank entries should clear the allowlist: %v", err)
	}
}

func TestEgressAllowlist_ExactAndWildcard(t *testing.T) {
	if err := SetEgressAllowlist([]string{"api.stripe.com", "*.slack.com"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetEgressAllowlist(nil) })

	allow := []string{
		"https://api.stripe.com/v1/charges",
		"https://API.STRIPE.COM/x", // case-insensitive
		"https://hooks.slack.com/services/xxx",
		"https://a.b.slack.com/x",       // multi-level subdomain
		"https://api.stripe.com:8443/x", // port stripped
	}
	for _, u := range allow {
		if err := egressAllowed(u); err != nil {
			t.Errorf("should allow %s: %v", u, err)
		}
	}
	block := []string{
		"https://evil.com/x",
		"https://stripe.com/x", // apex not matched by exact api.stripe.com
		"https://slack.com/x",  // *.slack.com does NOT match bare apex
		"https://notslack.com/x",
		"https://api.stripe.com.evil.com/x", // suffix-trick must not pass exact
	}
	for _, u := range block {
		if err := egressAllowed(u); err == nil {
			t.Errorf("should block %s", u)
		}
	}
}

func TestEgressAllowlist_CIDRAndIP(t *testing.T) {
	if err := SetEgressAllowlist([]string{"203.0.113.0/24", "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetEgressAllowlist(nil) })

	if err := egressAllowed("http://203.0.113.42/x"); err != nil {
		t.Errorf("CIDR member should be allowed: %v", err)
	}
	if err := egressAllowed("http://198.51.100.7/x"); err != nil {
		t.Errorf("exact IP should be allowed: %v", err)
	}
	if err := egressAllowed("http://203.0.114.1/x"); err == nil {
		t.Error("IP outside CIDR should be blocked")
	}
	// A hostname (not an IP literal) isn't covered by IP-only rules.
	if err := egressAllowed("https://example.com/x"); err == nil {
		t.Error("hostname should be blocked when only IP rules exist")
	}
}

func TestEgressAllowlist_RejectsBadEntries(t *testing.T) {
	if err := SetEgressAllowlist([]string{"10.0.0.0/99"}); err == nil {
		t.Error("bad CIDR should error")
	}
	if err := SetEgressAllowlist([]string{"*.com"}); err == nil {
		t.Error("overly-broad wildcard *.com should error")
	}
	// Leave the package in the disabled state for other tests.
	_ = SetEgressAllowlist(nil)
}
