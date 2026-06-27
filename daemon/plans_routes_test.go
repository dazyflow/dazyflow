// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeEntitlements is a minimal in-memory EntitlementStore for handler tests:
// only GetTier / GetEntitlement are exercised by the plans path.
type fakeEntitlements struct {
	tiers map[string]Tier
	ents  map[string]TenantEntitlement
}

func (f *fakeEntitlements) ListTiers(context.Context) ([]Tier, error) { return nil, nil }
func (f *fakeEntitlements) GetTier(_ context.Context, id string) (Tier, bool) {
	t, ok := f.tiers[id]
	return t, ok
}
func (f *fakeEntitlements) PutTier(context.Context, Tier) error      { return nil }
func (f *fakeEntitlements) DeleteTier(context.Context, string) error { return nil }
func (f *fakeEntitlements) GetEntitlement(_ context.Context, tenant string) (TenantEntitlement, bool) {
	e, ok := f.ents[tenant]
	return e, ok
}
func (f *fakeEntitlements) PutEntitlement(context.Context, TenantEntitlement) error { return nil }
func (f *fakeEntitlements) ListEntitlements(context.Context) ([]TenantEntitlement, error) {
	return nil, nil
}

// fakePlans returns a fixed plan for every tenant.
type fakePlans struct{ p TenantPlan }

func (f fakePlans) GetPlan(context.Context, string) (TenantPlan, error) { return f.p, nil }
func (f fakePlans) SetPlan(context.Context, TenantPlan) error           { return nil }

func builtinTierStore() *fakeEntitlements {
	return &fakeEntitlements{
		tiers: map[string]Tier{
			// Built-ins seeded as inherit (nil polling) — the fixed bug.
			"free": {ID: "free", Name: "Free", Plan: PlanFree, BuiltIn: true},
			"pro":  {ID: "pro", Name: "Pro", Plan: PlanPro, BuiltIn: true},
		},
		ents: map[string]TenantEntitlement{},
	}
}

func getPlansResponse(t *testing.T, h *HTTPGateway, tenant string) plansResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/plans", nil)
	rw := httptest.NewRecorder()
	h.plansMe(rw, req, core.Principal{Tenant: tenant})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body.String())
	}
	var out plansResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func findPlan(plans []planOption, id string) (planOption, bool) {
	for _, p := range plans {
		if p.ID == id {
			return p, true
		}
	}
	return planOption{}, false
}

func TestPlansMe_FreeTenant(t *testing.T) {
	svc := &Service{
		Entitlements:           builtinTierStore(),
		FreeRunsPerMonth:       100,
		MaxGraphNodes:          50,
		MaxGraphTimeoutSeconds: 300,
		FreePollingDisabled:    false, // deployment allows free polling
	}
	out := getPlansResponse(t, &HTTPGateway{svc: svc}, "org_free")

	if out.CurrentPlan != PlanFree {
		t.Fatalf("current_plan = %q, want free", out.CurrentPlan)
	}
	if len(out.Plans) != 3 {
		t.Fatalf("want free+pro+enterprise, got %d plans", len(out.Plans))
	}
	ent, ok := findPlan(out.Plans, "enterprise")
	if !ok || !ent.IsContact || ent.IsCurrent {
		t.Fatalf("enterprise should be a non-current contact plan: %+v", ent)
	}
	free, ok := findPlan(out.Plans, "free")
	if !ok || !free.IsCurrent {
		t.Fatalf("free plan should be current: %+v", free)
	}
	if free.Limits.RunsPerMonth != 100 {
		t.Errorf("free runs = %d, want 100 (the global default)", free.Limits.RunsPerMonth)
	}
	if !free.Limits.PollingAllowed {
		t.Error("free polling should inherit the allow-by-default global default")
	}
	pro, ok := findPlan(out.Plans, "pro")
	if !ok || pro.IsCurrent {
		t.Fatalf("pro plan should exist and not be current: %+v", pro)
	}
	if pro.Limits.RunsPerMonth != 0 {
		t.Errorf("pro runs = %d, want 0 (unlimited)", pro.Limits.RunsPerMonth)
	}
	if !pro.Limits.PollingAllowed {
		t.Error("pro polling should be true")
	}
}

// A Stripe-pro org keeps the default "free" tier id but is really on pro —
// the pro card, not free, must read as current.
func TestPlansMe_StripeProMarksProCurrent(t *testing.T) {
	svc := &Service{
		Entitlements:           builtinTierStore(),
		Plans:                  fakePlans{p: TenantPlan{Tenant: "org_pro", Plan: PlanPro, StripeCustomerID: "cus_1"}},
		FreeRunsPerMonth:       100,
		MaxGraphNodes:          50,
		MaxGraphTimeoutSeconds: 300,
	}
	out := getPlansResponse(t, &HTTPGateway{svc: svc}, "org_pro")

	if out.CurrentPlan != PlanPro {
		t.Fatalf("current_plan = %q, want pro", out.CurrentPlan)
	}
	free, _ := findPlan(out.Plans, "free")
	if free.IsCurrent {
		t.Error("free must not be current for a pro org")
	}
	pro, _ := findPlan(out.Plans, "pro")
	if !pro.IsCurrent {
		t.Error("pro must be current for a Stripe-pro org (despite tier id 'free')")
	}
}
