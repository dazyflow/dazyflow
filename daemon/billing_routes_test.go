// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// billingHarness extends the gateway harness with a plan store, usage
// store, and a Stripe fake.
// stripeFormRecorder captures the POST form of each call the daemon makes to
// Stripe, keyed by path, so a test can assert on the success/cancel/return URLs
// the daemon supplies.
type stripeFormRecorder struct {
	mu    sync.Mutex
	forms map[string]url.Values
}

func (s *stripeFormRecorder) record(path string, form url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forms[path] = form
}

func (s *stripeFormRecorder) get(path string) url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forms[path]
}

func billingHarness(t *testing.T) (*gatewayHarness, *MemPlanStore, *httptest.Server) {
	h, plans, srv, _ := billingHarnessWithStripeForms(t)
	return h, plans, srv
}

func billingHarnessWithStripeForms(t *testing.T) (*gatewayHarness, *MemPlanStore, *httptest.Server, *stripeFormRecorder) {
	t.Helper()
	stripeForms := &stripeFormRecorder{forms: map[string]url.Values{}}
	h := newGatewayHarness(t)
	plans := NewMemPlanStore()
	h.svc.Plans = plans
	h.svc.Usage = NewMemUsageStore()
	h.svc.PublicBaseURL = "https://app.example"

	fakeStripe := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// Record the form so a test can assert on the URLs Stripe will send the
		// user back to — those are the deployment's own links, not Stripe's, so
		// nothing else would catch a mistake in them.
		_ = r.ParseForm()
		stripeForms.record(r.URL.Path, r.PostForm)
		switch r.URL.Path {
		case "/v1/checkout/sessions":
			fmt.Fprint(rw, `{"id":"cs_1","url":"https://checkout.stripe.com/c/cs_1"}`)
		case "/v1/billing_portal/sessions":
			fmt.Fprint(rw, `{"url":"https://billing.stripe.com/p/1"}`)
		default:
			rw.WriteHeader(404)
		}
	}))
	t.Cleanup(fakeStripe.Close)

	sc := NewStripeClient("sk_test", "price_pro")
	sc.BaseURL = fakeStripe.URL
	h.gw.Billing = NewBillingHandler(sc, "whsec_test")
	return h, plans, fakeStripe, stripeForms
}

// The /usage page a Stripe round trip returns to is org-scoped, so the return
// URLs must name the org that was billed: Stripe hands the user back to a
// browser whose active org may have moved on (switching org in another tab
// mid-checkout is enough), and an unpinned return would show the wrong org's
// usage immediately after an upgrade.
func TestBillingReturnURLsPinTheOrg(t *testing.T) {
	h, plans, _, forms := billingHarnessWithStripeForms(t)

	rw := h.do(t, "POST", "/api/v1/me/billing/checkout", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("checkout: status = %d (%s)", rw.Code, rw.Body.String())
	}
	form := forms.get("/v1/checkout/sessions")
	if got := form.Get("success_url"); got != "https://app.example/usage?checkout=success&org=t" {
		t.Errorf("success_url = %q", got)
	}
	if got := form.Get("cancel_url"); got != "https://app.example/usage?checkout=cancelled&org=t" {
		t.Errorf("cancel_url = %q", got)
	}

	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_1"})
	rw = h.do(t, "POST", "/api/v1/me/billing/portal", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("portal: status = %d (%s)", rw.Code, rw.Body.String())
	}
	if got := forms.get("/v1/billing_portal/sessions").Get("return_url"); got != "https://app.example/usage?org=t" {
		t.Errorf("return_url = %q", got)
	}
}

// A base URL configured with a trailing slash must not yield "//usage" in the
// redirect Stripe bounces the user through.
func TestBillingReturnURLsTrimTrailingSlash(t *testing.T) {
	h, _, _, forms := billingHarnessWithStripeForms(t)
	h.svc.PublicBaseURL = "https://app.example/"

	rw := h.do(t, "POST", "/api/v1/me/billing/checkout", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("checkout: status = %d (%s)", rw.Code, rw.Body.String())
	}
	if got := forms.get("/v1/checkout/sessions").Get("success_url"); got != "https://app.example/usage?checkout=success&org=t" {
		t.Errorf("success_url = %q", got)
	}
}

func TestBillingMe(t *testing.T) {
	h, plans, _ := billingHarness(t)
	h.svc.FreeRunsPerMonth = 100
	_ = h.svc.Usage.AddRun(t.Context(), "t", time.Now())

	rw := h.do(t, "GET", "/api/v1/me/billing", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &got)
	if got["plan"] != "free" || got["free_runs_per_month"] != float64(100) ||
		got["runs_this_month"] != float64(1) || got["can_upgrade"] != true ||
		got["can_manage"] != false || got["billing_enabled"] != true {
		t.Errorf("got %+v", got)
	}

	// After an upgrade the flags flip.
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_1"})
	rw = h.do(t, "GET", "/api/v1/me/billing", nil)
	_ = json.Unmarshal(rw.Body.Bytes(), &got)
	if got["plan"] != "pro" || got["can_upgrade"] != false || got["can_manage"] != true {
		t.Errorf("after upgrade: %+v", got)
	}
}

// A self-hosted deployment with no Stripe wired up reports billing_enabled
// false (and can_upgrade/can_manage false), so the web client hides the whole
// plan/billing surface and shows usage only.
func TestBillingMeNoStripe(t *testing.T) {
	h, _, _ := billingHarness(t)
	h.gw.Billing = nil // no Stripe configured

	rw := h.do(t, "GET", "/api/v1/me/billing", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &got)
	if got["billing_enabled"] != false || got["can_upgrade"] != false || got["can_manage"] != false {
		t.Errorf("self-host: got %+v, want billing_enabled/can_upgrade/can_manage all false", got)
	}
}

// An admin-granted entitlement (comp/trial/override/pro tier) makes a tenant
// effectively pro with no Stripe subscription, so the raw plan record stays
// free. /me/billing must report the effective plan and hide the upgrade CTA —
// otherwise a comped tenant is told to "Upgrade to Pro". Regression for the
// divergence between this endpoint and /me/plans.
func TestBillingMeCompedEntitlement(t *testing.T) {
	h, _, _ := billingHarness(t)
	ents := builtinTierStore()
	ents.ents["t"] = TenantEntitlement{Tenant: "t", Comped: true}
	h.svc.Entitlements = ents

	rw := h.do(t, "GET", "/api/v1/me/billing", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rw.Body.Bytes(), &got)
	// Plan store has no Stripe sub (no SetPlan), but the comp grant makes the
	// tenant effectively pro: plan reads pro and upgrade is not offered.
	if got["plan"] != "pro" || got["can_upgrade"] != false {
		t.Errorf("comped tenant: got %+v, want plan=pro can_upgrade=false", got)
	}
}

func TestBillingCheckout(t *testing.T) {
	h, _, _ := billingHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/billing/checkout", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "checkout.stripe.com") {
		t.Errorf("body %s, want checkout URL", rw.Body.String())
	}
}

func TestBillingCheckout_AlreadySubscribedRejected(t *testing.T) {
	// A live subscription must not be able to mint a second Checkout
	// session (which would double-bill on a fresh customer).
	h, plans, _ := billingHarness(t)
	_ = plans.SetPlan(t.Context(), TenantPlan{
		Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_1",
		StripeSubscriptionID: "sub_1", SubscriptionStatus: "active",
	})
	rw := h.do(t, "POST", "/api/v1/me/billing/checkout", nil)
	if rw.Code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "already_subscribed") {
		t.Errorf("body %s, want already_subscribed code", rw.Body.String())
	}

	// A lapsed subscription (canceled) may re-checkout — and reuses the
	// stored customer rather than spawning a new one.
	_ = plans.SetPlan(t.Context(), TenantPlan{
		Tenant: "t", Plan: PlanFree, StripeCustomerID: "cus_1",
		StripeSubscriptionID: "sub_1", SubscriptionStatus: "canceled",
	})
	rw = h.do(t, "POST", "/api/v1/me/billing/checkout", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("re-checkout after lapse: status = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestBillingCheckout_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/billing/checkout", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rw.Code)
	}
}

func TestBillingPortal_RequiresExistingCustomer(t *testing.T) {
	h, plans, _ := billingHarness(t)

	rw := h.do(t, "POST", "/api/v1/me/billing/portal", nil)
	if rw.Code != http.StatusConflict {
		t.Fatalf("portal without customer: status = %d, want 409", rw.Code)
	}
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_1"})
	rw = h.do(t, "POST", "/api/v1/me/billing/portal", nil)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "billing.stripe.com") {
		t.Fatalf("portal with customer: %d %s", rw.Code, rw.Body.String())
	}
}

// postStripeEvent signs and POSTs a webhook event to the gateway.
func postStripeEvent(t *testing.T, h *gatewayHarness, payload string) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(payload)
	req := httptest.NewRequest("POST", "/api/v1/events/stripe", strings.NewReader(payload))
	req.Header.Set("Stripe-Signature", signStripe(t, "whsec_test", time.Now(), body))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestStripeWebhook_CheckoutCompletedUpgrades(t *testing.T) {
	h, plans, _ := billingHarness(t)

	rw := postStripeEvent(t, h, `{
		"type": "checkout.session.completed",
		"data": {"object": {
			"id": "cs_1", "customer": "cus_9", "subscription": "sub_9",
			"client_reference_id": "t"
		}}
	}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	p, _ := plans.GetPlan(t.Context(), "t")
	if p.Plan != PlanPro || p.StripeCustomerID != "cus_9" || p.StripeSubscriptionID != "sub_9" {
		t.Errorf("plan after checkout = %+v", p)
	}
}

func TestStripeWebhook_SubscriptionDeletedDowngrades(t *testing.T) {
	h, plans, _ := billingHarness(t)
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_9"})

	rw := postStripeEvent(t, h, `{
		"type": "customer.subscription.deleted",
		"data": {"object": {
			"id": "sub_9", "customer": "cus_9", "status": "canceled",
			"metadata": {"tenant": "t"}
		}}
	}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	p, _ := plans.GetPlan(t.Context(), "t")
	if p.Plan != PlanFree || p.SubscriptionStatus != "canceled" {
		t.Errorf("plan after delete = %+v", p)
	}
}

func TestStripeWebhook_PastDueStaysPro(t *testing.T) {
	// Stripe is still retrying the charge — don't cut access on the
	// first payment hiccup.
	h, plans, _ := billingHarness(t)
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_9"})

	postStripeEvent(t, h, `{
		"type": "customer.subscription.updated",
		"data": {"object": {
			"id": "sub_9", "customer": "cus_9", "status": "past_due",
			"metadata": {"tenant": "t"}, "current_period_end": 1781000000
		}}
	}`)
	p, _ := plans.GetPlan(t.Context(), "t")
	if p.Plan != PlanPro || p.SubscriptionStatus != "past_due" || p.CurrentPeriodEnd.IsZero() {
		t.Errorf("plan after past_due = %+v", p)
	}
}

func TestStripeWebhook_PeriodEndFromLineItems(t *testing.T) {
	// Stripe API 2025-03-31+ (incl. recent default versions) drops the
	// top-level current_period_end and carries it per line item instead;
	// the latest across items becomes the renewal boundary.
	h, plans, _ := billingHarness(t)
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_9"})

	postStripeEvent(t, h, `{
		"type": "customer.subscription.updated",
		"data": {"object": {
			"id": "sub_9", "customer": "cus_9", "status": "active",
			"metadata": {"tenant": "t"},
			"items": {"data": [
				{"current_period_end": 1781000000},
				{"current_period_end": 1781500000}
			]}
		}}
	}`)
	p, _ := plans.GetPlan(t.Context(), "t")
	if p.Plan != PlanPro || p.SubscriptionStatus != "active" {
		t.Fatalf("plan after update = %+v", p)
	}
	if got := p.CurrentPeriodEnd.Unix(); got != 1781500000 {
		t.Errorf("current_period_end = %d, want latest item 1781500000", got)
	}
}

func TestStripeWebhook_BadSignatureRejected(t *testing.T) {
	h, plans, _ := billingHarness(t)

	body := `{"type":"checkout.session.completed","data":{"object":{"client_reference_id":"t"}}}`
	req := httptest.NewRequest("POST", "/api/v1/events/stripe", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", signStripe(t, "whsec_WRONG", time.Now(), []byte(body)))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
	p, _ := plans.GetPlan(t.Context(), "t")
	if p.Plan != PlanFree {
		t.Errorf("forged event changed the plan: %+v", p)
	}
}

func TestStripeWebhook_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("POST", "/api/v1/events/stripe", strings.NewReader("{}"))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rw.Code)
	}
}

func TestStripeWebhook_UnknownEventAcked(t *testing.T) {
	h, _, _ := billingHarness(t)
	rw := postStripeEvent(t, h, `{"type":"invoice.finalized","data":{"object":{}}}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 ack", rw.Code)
	}
}

// The full loop: a capped free tenant upgrades via webhook and the next
// run goes through.
func TestPlanGate_WebhookUpgradeLiftsCap(t *testing.T) {
	h, _, _ := billingHarness(t)
	h.svc.FreeRunsPerMonth = 1
	_ = h.svc.Usage.AddRun(t.Context(), "t", time.Now()) // already at cap

	if err := h.svc.checkRunQuota(t.Context(), "t"); err == nil {
		t.Fatal("free tenant at cap should be gated")
	}
	postStripeEvent(t, h, `{
		"type": "checkout.session.completed",
		"data": {"object": {"customer": "cus_9", "subscription": "sub_9", "client_reference_id": "t"}}
	}`)
	if err := h.svc.checkRunQuota(t.Context(), "t"); err != nil {
		t.Fatalf("pro tenant still gated: %v", err)
	}
}

// A replayed delivery (same event id) acks without re-applying: the
// downgrade in the replay must not undo a later upgrade.
func TestStripeWebhook_ReplayedEventIgnored(t *testing.T) {
	h, plans, _ := billingHarness(t)

	ev := `{
		"id": "evt_once",
		"type": "checkout.session.completed",
		"data": {"object": {"customer": "cus_9", "subscription": "sub_9", "client_reference_id": "t"}}
	}`
	if rw := postStripeEvent(t, h, ev); rw.Code != http.StatusOK {
		t.Fatalf("first delivery: %d", rw.Code)
	}
	// Simulate state moving on after the first processing…
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "t", Plan: PlanPro, StripeCustomerID: "cus_LATER"})
	// …then Stripe retries the original delivery.
	if rw := postStripeEvent(t, h, ev); rw.Code != http.StatusOK {
		t.Fatalf("replay: %d", rw.Code)
	}
	p, _ := plans.GetPlan(t.Context(), "t")
	if p.StripeCustomerID != "cus_LATER" {
		t.Errorf("replay re-applied the stale event: %+v", p)
	}
}
