package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"

	_ "git.sr.ht/~klahr/dazyflow/drops/stripe"
)

// Signing helper: signStripe in stripe_test.go (shared with the
// billing-webhook tests — same Stripe-Signature scheme).

type stripeHarness struct {
	*gatewayHarness
	secret string
}

// newStripeHarness wires the events handler plus an in-memory encrypted
// secret store holding tenant "t"'s signing secret — the per-tenant
// model the handler authenticates against (no operator-level secret).
func newStripeHarness(t *testing.T) *stripeHarness {
	t.Helper()
	gh := newGatewayHarness(t)
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("encrypted secrets: %v", err)
	}
	const secret = "whsec_test_secret"
	if err := es.Put(t.Context(), "t", stripeTriggerSecretName, secret); err != nil {
		t.Fatalf("put secret: %v", err)
	}
	gh.svc.EncryptedSecrets = es
	gh.gw.StripeEvents = NewStripeEventsHandler(gh.svc)
	return &stripeHarness{gatewayHarness: gh, secret: secret}
}

func (h *stripeHarness) post(t *testing.T, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", signStripe(t, h.secret, time.Now(), body))
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestStripeEvents_BadSignatureRejected(t *testing.T) {
	h := newStripeHarness(t)
	body := []byte(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/events/stripe/t", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=deadbeef", time.Now().Unix()))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestStripeEvents_MissingSignatureRejected(t *testing.T) {
	h := newStripeHarness(t)
	req := httptest.NewRequest("POST", "/api/v1/events/stripe/t", bytes.NewReader([]byte(`{}`)))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

// TestStripeEvents_StaleTimestampRejected — a correctly-signed delivery
// whose timestamp is outside the 5-minute tolerance must not validate
// (replay protection).
func TestStripeEvents_StaleTimestampRejected(t *testing.T) {
	h := newStripeHarness(t)
	body := []byte(`{"type":"payment_intent.succeeded"}`)
	req := httptest.NewRequest("POST", "/api/v1/events/stripe/t", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", signStripe(t, h.secret, time.Now().Add(-10*time.Minute), body))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("stale timestamp code=%d want 401", rw.Code)
	}
}

// TestStripeEvents_NoTenantSecretRejected — a tenant that never saved a
// STRIPE_WEBHOOK_SECRET gets the same 401 as a bad signature, so probing
// tenant names leaks nothing.
func TestStripeEvents_NoTenantSecretRejected(t *testing.T) {
	h := newStripeHarness(t)
	body := []byte(`{}`)
	req := httptest.NewRequest("POST", "/api/v1/events/stripe/other-tenant", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", signStripe(t, h.secret, time.Now(), body))
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusUnauthorized {
		t.Errorf("code=%d want 401", rw.Code)
	}
}

func TestStripeEvents_NotConfiguredReturns501(t *testing.T) {
	gh := newGatewayHarness(t)
	// gw.StripeEvents intentionally nil.
	req := httptest.NewRequest("POST", "/api/v1/events/stripe/t", bytes.NewReader([]byte(`{}`)))
	rw := httptest.NewRecorder()
	ServeForTest(gh.gw, rw, req)
	if rw.Code != http.StatusNotImplemented {
		t.Errorf("code=%d want 501", rw.Code)
	}
}

func TestStripeEvents_PaymentDispatchesToSubscribedGraphs(t *testing.T) {
	h := newStripeHarness(t)
	g := core.Graph{
		ID: "payment-alert", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "stripe_on_payment"}},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	event := map[string]any{
		"id":   "evt_1",
		"type": "payment_intent.succeeded",
		"data": map[string]any{
			"object": map[string]any{
				"id":              "pi_123",
				"object":          "payment_intent",
				"amount":          4999,
				"amount_received": 4999,
				"currency":        "usd",
				"receipt_email":   "buyer@example.com",
				"description":     "Pro plan",
			},
		},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/stripe/t", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	// Wait briefly for the background fanout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "payment-alert",
		})
		if err == nil && len(runs) > 0 {
			node, err := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
			if err != nil {
				t.Fatalf("get node record: %v", err)
			}
			if node.Status != core.JobStatusSucceeded {
				t.Fatalf("trigger node status=%q want succeeded", node.Status)
			}
			if got, _ := node.Result.Output["amount_display"].Inline.(string); got != "49.99 USD" {
				t.Errorf("amount_display port = %q", got)
			}
			if got, _ := node.Result.Output["amount"].Inline.(string); got != "4999" {
				t.Errorf("amount port = %q", got)
			}
			if got, _ := node.Result.Output["currency"].Inline.(string); got != "USD" {
				t.Errorf("currency port = %q", got)
			}
			if got, _ := node.Result.Output["customer_email"].Inline.(string); got != "buyer@example.com" {
				t.Errorf("customer_email port = %q", got)
			}
			if got, _ := node.Result.Output["payment_id"].Inline.(string); got != "pi_123" {
				t.Errorf("payment_id port = %q", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no graph-record materialized within 2s")
}

func TestStripeEvents_PaymentFailedDispatches(t *testing.T) {
	h := newStripeHarness(t)
	g := core.Graph{
		ID: "decline-alert", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "stripe_on_payment_failed"}},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	event := map[string]any{
		"id":   "evt_f1",
		"type": "payment_intent.payment_failed",
		"data": map[string]any{
			"object": map[string]any{
				"id":            "pi_fail",
				"amount":        2500,
				"currency":      "eur",
				"receipt_email": "buyer@example.com",
				"last_payment_error": map[string]any{
					"message": "Your card was declined.",
				},
			},
		},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/stripe/t", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "decline-alert",
		})
		if err == nil && len(runs) > 0 {
			node, _ := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
			if node.Status != core.JobStatusSucceeded {
				t.Fatalf("status=%q", node.Status)
			}
			if got, _ := node.Result.Output["failure_message"].Inline.(string); got != "Your card was declined." {
				t.Errorf("failure_message = %q", got)
			}
			// A failed intent has amount_received=0 — amount falls back
			// to the requested amount.
			if got, _ := node.Result.Output["amount_display"].Inline.(string); got != "25.00 EUR" {
				t.Errorf("amount_display = %q", got)
			}
			if got, _ := node.Result.Output["payment_id"].Inline.(string); got != "pi_fail" {
				t.Errorf("payment_id = %q", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no graph-record materialized within 2s")
}

func TestStripeEvents_SubscriptionCanceledDispatches(t *testing.T) {
	h := newStripeHarness(t)
	g := core.Graph{
		ID: "churn-alert", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "trig", Module: "stripe_on_subscription_canceled"}},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}

	event := map[string]any{
		"id":   "evt_s1",
		"type": "customer.subscription.deleted",
		"data": map[string]any{
			"object": map[string]any{
				"id":       "sub_9",
				"customer": "cus_9",
				"ended_at": 1767225600,
				"items": map[string]any{
					"data": []any{
						map[string]any{"price": map[string]any{"id": "price_9", "nickname": "Pro monthly"}},
					},
				},
			},
		},
	}
	body, _ := json.Marshal(event)
	rw := h.post(t, "/api/v1/events/stripe/t", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rw.Code, rw.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := h.store.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
			Tenant: "t", Workspace: "ws", GraphID: "churn-alert",
		})
		if err == nil && len(runs) > 0 {
			node, _ := h.store.Get(t.Context(), NodeJobID(runs[0].ID, "trig"))
			if node.Status != core.JobStatusSucceeded {
				t.Fatalf("status=%q", node.Status)
			}
			if got, _ := node.Result.Output["subscription_id"].Inline.(string); got != "sub_9" {
				t.Errorf("subscription_id = %q", got)
			}
			if got, _ := node.Result.Output["customer"].Inline.(string); got != "cus_9" {
				t.Errorf("customer = %q", got)
			}
			if got, _ := node.Result.Output["plan"].Inline.(string); got != "Pro monthly" {
				t.Errorf("plan = %q", got)
			}
			if got, _ := node.Result.Output["ended_at"].Inline.(string); got != "2026-01-01T00:00:00Z" {
				t.Errorf("ended_at = %q", got)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no graph-record materialized within 2s")
}

func TestStripeOnPaymentFailed_StandaloneRunErrors(t *testing.T) {
	trans, ok := engine.Default.Get("stripe_on_payment_failed")
	if !ok {
		t.Fatal("stripe_on_payment_failed not registered")
	}
	res, _ := trans.Execute(t.Context(), core.Job{ID: "j"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Errorf("standalone should error with no_trigger_data, got %+v", res)
	}
}

func TestStripeOnSubscriptionCanceled_StandaloneRunErrors(t *testing.T) {
	trans, ok := engine.Default.Get("stripe_on_subscription_canceled")
	if !ok {
		t.Fatal("stripe_on_subscription_canceled not registered")
	}
	res, _ := trans.Execute(t.Context(), core.Job{ID: "j"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Errorf("standalone should error with no_trigger_data, got %+v", res)
	}
}

func TestStripeEvents_UnknownEventAcked(t *testing.T) {
	h := newStripeHarness(t)
	body := []byte(`{"id":"evt_2","type":"customer.created","data":{"object":{}}}`)
	rw := h.post(t, "/api/v1/events/stripe/t", body)
	if rw.Code != http.StatusOK {
		t.Errorf("code=%d want 200 (unknown event should ack)", rw.Code)
	}
}

func TestStripeOnPayment_StandaloneRunErrors(t *testing.T) {
	trans, ok := engine.Default.Get("stripe_on_payment")
	if !ok {
		t.Fatal("stripe_on_payment not registered")
	}
	res, _ := trans.Execute(t.Context(), core.Job{ID: "j"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
		t.Errorf("standalone stripe_on_payment should error with no_trigger_data, got %+v", res)
	}
}

func TestFormatStripeAmount(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		want     string
	}{
		{4999, "usd", "49.99 USD"},
		{500, "eur", "5.00 EUR"},
		{4, "usd", "0.04 USD"},
		{5000, "jpy", "5000 JPY"},
		{1200, "krw", "1200 KRW"},
	}
	for _, c := range cases {
		if got := formatStripeAmount(c.minor, c.currency); got != c.want {
			t.Errorf("formatStripeAmount(%d, %q) = %q want %q", c.minor, c.currency, got, c.want)
		}
	}
}
