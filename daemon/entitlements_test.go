package daemon

import (
	"testing"
	"time"
)

func ptrInt(v int) *int     { return &v }
func ptrI64(v int64) *int64 { return &v }
func ptrBool(v bool) *bool  { return &v }

var testDefaults = LimitDefaults{
	RunsPerMonth:      100,
	DiskQuotaBytes:    0,
	MaxGraphNodes:     50,
	MaxFlows:          0,
	MaxTimeoutSeconds: 300,
	PollingAllowed:    false, // global free-polling-disabled
}

func TestResolveEffective_DefaultsWhenEmpty(t *testing.T) {
	eff := ResolveEffective(nil, nil, testDefaults, PlanFree, time.Unix(0, 0))
	if eff.RunsPerMonth != 100 || eff.MaxGraphNodes != 50 || eff.MaxTimeoutSeconds != 300 {
		t.Fatalf("expected global defaults, got %+v", eff)
	}
	if eff.Plan != PlanFree {
		t.Fatalf("plan = %q, want free", eff.Plan)
	}
	if eff.PollingAllowed {
		t.Fatal("polling should be disabled by default")
	}
}

func TestResolveEffective_TierThenOverride(t *testing.T) {
	tier := &Tier{
		ID: "pro", Plan: PlanPro, RunsPerMonth: 10000,
		MaxGraphNodes: 500, PollingAllowed: ptrBool(true),
	}
	ent := &TenantEntitlement{
		Tenant: "org_x", TierID: "pro",
		RunsPerMonth: ptrInt(99999), // override beats the tier
	}
	eff := ResolveEffective(ent, tier, testDefaults, PlanFree, time.Unix(0, 0))
	if eff.RunsPerMonth != 99999 {
		t.Errorf("runs = %d, want override 99999", eff.RunsPerMonth)
	}
	if eff.MaxGraphNodes != 500 {
		t.Errorf("nodes = %d, want tier 500", eff.MaxGraphNodes)
	}
	if eff.MaxTimeoutSeconds != 300 {
		t.Errorf("timeout = %d, want global default 300 (tier left it 0)", eff.MaxTimeoutSeconds)
	}
	if eff.Plan != PlanPro || !eff.PollingAllowed {
		t.Errorf("pro tier should grant pro + polling, got plan=%q polling=%v", eff.Plan, eff.PollingAllowed)
	}
}

func TestResolveEffective_PlanResolution(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	freeTier := &Tier{ID: "free", Plan: PlanFree}

	cases := []struct {
		name string
		ent  *TenantEntitlement
		tier *Tier
		strp string
		want string
	}{
		{"stripe pro", nil, freeTier, PlanPro, PlanPro},
		{"comped grants pro", &TenantEntitlement{Comped: true}, freeTier, PlanFree, PlanPro},
		{"active trial grants pro", &TenantEntitlement{TrialEndsAt: &future}, freeTier, PlanFree, PlanPro},
		{"expired trial stays free", &TenantEntitlement{TrialEndsAt: &past}, freeTier, PlanFree, PlanFree},
		{"force free pins free over stripe pro", &TenantEntitlement{PlanOverride: PlanFree}, freeTier, PlanPro, PlanFree},
		{"force pro pins pro", &TenantEntitlement{PlanOverride: PlanPro}, freeTier, PlanFree, PlanPro},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff := ResolveEffective(c.ent, c.tier, testDefaults, c.strp, now)
			if eff.Plan != c.want {
				t.Fatalf("plan = %q, want %q", eff.Plan, c.want)
			}
		})
	}
}

// TestResolveEffective_TierPollingInherits is the regression guard for the
// bug that silently disabled scheduling for every free org: a built-in tier
// with PollingAllowed unset (nil) must INHERIT the deployment-global default,
// not force it false. A non-nil tier value still wins.
func TestResolveEffective_TierPollingInherits(t *testing.T) {
	allowDefaults := testDefaults
	allowDefaults.PollingAllowed = true // deployment allows free polling

	// Built-in free tier, polling unset → inherit the (allow) default.
	freeTier := &Tier{ID: "free", Plan: PlanFree} // PollingAllowed nil
	eff := ResolveEffective(nil, freeTier, allowDefaults, PlanFree, time.Unix(0, 0))
	if !eff.PollingAllowed {
		t.Fatal("free tier with nil polling must inherit the allow-by-default global default")
	}

	// An explicit tier value still overrides the default.
	denyTier := &Tier{ID: "free", Plan: PlanFree, PollingAllowed: ptrBool(false)}
	eff = ResolveEffective(nil, denyTier, allowDefaults, PlanFree, time.Unix(0, 0))
	if eff.PollingAllowed {
		t.Fatal("explicit tier polling=false must override the global default")
	}
}

func TestResolveEffective_AllOverrideKinds(t *testing.T) {
	ent := &TenantEntitlement{
		RunsPerMonth:      ptrInt(7),
		DiskQuotaBytes:    ptrI64(1 << 30),
		MaxGraphNodes:     ptrInt(3),
		MaxFlows:          ptrInt(9),
		MaxTimeoutSeconds: ptrInt(42),
		RetentionDays:     ptrInt(14),
		MaxConcurrency:    ptrInt(4),
		MaxMembers:        ptrInt(6),
		PollingAllowed:    ptrBool(true),
	}
	eff := ResolveEffective(ent, nil, testDefaults, PlanFree, time.Unix(0, 0))
	if eff.RunsPerMonth != 7 || eff.DiskQuotaBytes != 1<<30 || eff.MaxGraphNodes != 3 ||
		eff.MaxFlows != 9 || eff.MaxTimeoutSeconds != 42 || !eff.PollingAllowed {
		t.Fatalf("overrides not all applied: %+v", eff)
	}
	if eff.RetentionDays != 14 || eff.MaxConcurrency != 4 || eff.MaxMembers != 6 {
		t.Fatalf("new-dimension overrides not applied: %+v", eff)
	}
}

// TestResolveEffective_NewDimsPrecedence verifies the three three-tier
// dimensions (retention, concurrency, seats) resolve override → tier → default
// like the original numeric limits, and that 0 means inherit.
func TestResolveEffective_NewDimsPrecedence(t *testing.T) {
	def := testDefaults
	def.RetentionDays = 7
	def.MaxConcurrency = 2
	def.MaxMembers = 2

	// No tier, no override → global free defaults.
	eff := ResolveEffective(nil, nil, def, PlanFree, time.Unix(0, 0))
	if eff.RetentionDays != 7 || eff.MaxConcurrency != 2 || eff.MaxMembers != 2 {
		t.Fatalf("expected free defaults, got %+v", eff)
	}

	// Tier raises them; a 0 tier field still inherits the default.
	tier := &Tier{ID: "team", Plan: PlanFree, RetentionDays: 90, MaxMembers: 25} // MaxConcurrency 0 = inherit
	eff = ResolveEffective(nil, tier, def, PlanFree, time.Unix(0, 0))
	if eff.RetentionDays != 90 {
		t.Errorf("retention = %d, want tier 90", eff.RetentionDays)
	}
	if eff.MaxConcurrency != 2 {
		t.Errorf("concurrency = %d, want inherited default 2 (tier left it 0)", eff.MaxConcurrency)
	}
	if eff.MaxMembers != 25 {
		t.Errorf("members = %d, want tier 25", eff.MaxMembers)
	}

	// Override beats the tier.
	ent := &TenantEntitlement{TierID: "team", MaxMembers: ptrInt(3)}
	eff = ResolveEffective(ent, tier, def, PlanFree, time.Unix(0, 0))
	if eff.MaxMembers != 3 {
		t.Errorf("members = %d, want override 3", eff.MaxMembers)
	}
}
