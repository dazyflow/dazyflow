package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// billingHarness extends the gateway harness with a plan store, usage
// store, and a Stripe fake.
func billingHarness(t *testing.T) (*gatewayHarness, *MemPlanStore, *httptest.Server) {
	t.Helper()
	h := newGatewayHarness(t)
	plans := NewMemPlanStore()
	h.svc.Plans = plans
	h.svc.Usage = NewMemUsageStore()
	h.svc.PublicBaseURL = "https://app.example"

	fakeStripe := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
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
	return h, plans, fakeStripe
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
		got["runs_this_month"] != float64(1) || got["can_upgrade"] != true || got["can_manage"] != false {
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
