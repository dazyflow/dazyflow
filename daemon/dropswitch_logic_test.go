package daemon

import "testing"

// TestDropSwitchDisabled exercises the global-vs-per-tenant resolution of
// PgDropSwitchStore.Disabled against a hand-seeded cache, with no DB —
// the cache map is what the resolver hot path actually reads.
func TestDropSwitchDisabled(t *testing.T) {
	s := &PgDropSwitchStore{cache: map[string]bool{
		dropSwitchKey("globally_off", ""):       true, // global switch
		dropSwitchKey("org_scoped", "org_acme"): true, // one tenant only
	}}

	cases := []struct {
		drop, tenant string
		want         bool
	}{
		{"globally_off", "org_acme", true},  // global hits every tenant
		{"globally_off", "org_other", true}, //
		{"globally_off", "", true},          // and the bare/global lookup
		{"org_scoped", "org_acme", true},    // per-tenant match
		{"org_scoped", "org_other", false},  // other tenants unaffected
		{"org_scoped", "", false},           // global lookup misses a per-tenant switch
		{"on_drop", "org_acme", false},      // no switch at all
	}
	for _, c := range cases {
		if got := s.Disabled(c.drop, c.tenant); got != c.want {
			t.Errorf("Disabled(%q, %q) = %v, want %v", c.drop, c.tenant, got, c.want)
		}
	}
}
