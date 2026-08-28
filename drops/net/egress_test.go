// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestEgressAllowlist_Disabled(t *testing.T) {
	// No allowlist → everything public is allowed.
	if err := SetEgressAllowlist(nil); err != nil {
		t.Fatal(err)
	}
	if err := EgressAllowed("https://anything.example.org/x"); err != nil {
		t.Errorf("disabled allowlist should permit all: %v", err)
	}
	// Whitespace-only entries also clear it.
	if err := SetEgressAllowlist([]string{"  ", ""}); err != nil {
		t.Fatal(err)
	}
	if err := EgressAllowed("https://anything.example.org"); err != nil {
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
		if err := EgressAllowed(u); err != nil {
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
		if err := EgressAllowed(u); err == nil {
			t.Errorf("should block %s", u)
		}
	}
}

func TestEgressAllowlist_CIDRAndIP(t *testing.T) {
	if err := SetEgressAllowlist([]string{"203.0.113.0/24", "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetEgressAllowlist(nil) })

	if err := EgressAllowed("http://203.0.113.42/x"); err != nil {
		t.Errorf("CIDR member should be allowed: %v", err)
	}
	if err := EgressAllowed("http://198.51.100.7/x"); err != nil {
		t.Errorf("exact IP should be allowed: %v", err)
	}
	if err := EgressAllowed("http://203.0.114.1/x"); err == nil {
		t.Error("IP outside CIDR should be blocked")
	}
	// A hostname (not an IP literal) isn't covered by IP-only rules.
	if err := EgressAllowed("https://example.com/x"); err == nil {
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

// fakeTenantPolicy is a per-tenant allowlist resolver for tests.
type fakeTenantPolicy map[string][]string

func (f fakeTenantPolicy) AllowlistFor(tenant string) ([]string, bool) {
	entries, ok := f[tenant]
	return entries, ok
}

// TestEgressAllowedFor_PerTenant covers the per-tenant policy: a tenant with
// its own allowlist is bound by it (independent of the global list), and a
// tenant with no per-tenant policy falls back to the global allowlist.
func TestEgressAllowedFor_PerTenant(t *testing.T) {
	t.Cleanup(func() { SetEgressPolicy(nil); _ = SetEgressAllowlist(nil) })

	// Global allowlist permits global.example only.
	if err := SetEgressAllowlist([]string{"global.example"}); err != nil {
		t.Fatal(err)
	}
	// acme has its own allowlist (acme-api.example); globex has none.
	SetEgressPolicy(fakeTenantPolicy{
		"acme": {"acme-api.example"},
	})

	ctxFor := func(tenant string) context.Context {
		return core.WithTenant(context.Background(), tenant)
	}

	// acme: bound by its own list, NOT the global one.
	if err := EgressAllowedFor(ctxFor("acme"), "https://acme-api.example/x"); err != nil {
		t.Errorf("acme should reach its own allowed host: %v", err)
	}
	if err := EgressAllowedFor(ctxFor("acme"), "https://global.example/x"); err == nil {
		t.Error("acme must NOT inherit the global host — per-tenant policy replaces it")
	}

	// globex: no per-tenant policy → falls back to the global allowlist.
	if err := EgressAllowedFor(ctxFor("globex"), "https://global.example/x"); err != nil {
		t.Errorf("globex should fall back to the global allowlist: %v", err)
	}
	if err := EgressAllowedFor(ctxFor("globex"), "https://acme-api.example/x"); err == nil {
		t.Error("globex must not reach acme's host")
	}

	// With no resolver installed at all, EgressAllowedFor == global check.
	SetEgressPolicy(nil)
	if err := EgressAllowedFor(ctxFor("acme"), "https://global.example/x"); err != nil {
		t.Errorf("with no resolver, should use the global list: %v", err)
	}
}

// TestEgressAllowedFor_InvalidTenantPolicyFailsClosed: a per-tenant policy
// that fails to compile blocks rather than silently allowing.
func TestEgressAllowedFor_InvalidTenantPolicyFailsClosed(t *testing.T) {
	t.Cleanup(func() { SetEgressPolicy(nil); _ = SetEgressAllowlist(nil) })
	_ = SetEgressAllowlist(nil)                          // global allows all
	SetEgressPolicy(fakeTenantPolicy{"acme": {"*.com"}}) // too-broad wildcard → compile error
	err := EgressAllowedFor(core.WithTenant(context.Background(), "acme"), "https://anything.com/x")
	if err == nil {
		t.Error("an invalid per-tenant policy must fail closed, not allow")
	}
}
