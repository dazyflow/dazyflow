// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStripeClient_CreateCheckoutSession(t *testing.T) {
	var gotPath, gotAuth string
	var gotForm map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = r.ParseForm()
		gotForm = r.PostForm
		fmt.Fprint(rw, `{"id":"cs_123","url":"https://checkout.stripe.com/c/pay/cs_123"}`)
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_abc", "price_pro")
	c.BaseURL = srv.URL
	u, err := c.CreateCheckoutSession(t.Context(), "acme", "",
		"https://app.example/usage?upgraded=1", "https://app.example/usage")
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if u != "https://checkout.stripe.com/c/pay/cs_123" {
		t.Errorf("url = %q", u)
	}
	if gotPath != "/v1/checkout/sessions" || gotAuth != "Bearer sk_test_abc" {
		t.Errorf("path/auth = %q / %q", gotPath, gotAuth)
	}
	// The tenant must ride on BOTH the session and the subscription
	// metadata, so checkout AND subscription lifecycle events map back.
	for key, want := range map[string]string{
		"mode":                                "subscription",
		"line_items[0][price]":                "price_pro",
		"client_reference_id":                 "acme",
		"subscription_data[metadata][tenant]": "acme",
	} {
		if got := gotForm[key]; len(got) != 1 || got[0] != want {
			t.Errorf("form[%q] = %v, want %q", key, got, want)
		}
	}
}

func TestStripeClient_ErrorSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(400)
		fmt.Fprint(rw, `{"error":{"message":"No such price: price_pro"}}`)
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_abc", "price_pro")
	c.BaseURL = srv.URL
	_, err := c.CreateCheckoutSession(t.Context(), "acme", "", "https://x/ok", "https://x/no")
	if err == nil || !strings.Contains(err.Error(), "No such price") {
		t.Fatalf("err = %v, want Stripe's message surfaced", err)
	}
}

func TestStripeClient_CreatePortalSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path != "/v1/billing_portal/sessions" || r.PostForm.Get("customer") != "cus_9" {
			rw.WriteHeader(400)
			fmt.Fprint(rw, `{"error":{"message":"bad request"}}`)
			return
		}
		fmt.Fprint(rw, `{"url":"https://billing.stripe.com/p/session_9"}`)
	}))
	defer srv.Close()

	c := NewStripeClient("sk_test_abc", "price_pro")
	c.BaseURL = srv.URL
	u, err := c.CreatePortalSession(t.Context(), "cus_9", "https://app.example/usage")
	if err != nil || u != "https://billing.stripe.com/p/session_9" {
		t.Fatalf("got %q / %v", u, err)
	}
}

// signStripe builds a valid Stripe-Signature header for body at ts.
func signStripe(t *testing.T, secret string, ts time.Time, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts.Unix())
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifyStripeSignature(t *testing.T) {
	secret := "whsec_test"
	body := []byte(`{"type":"checkout.session.completed"}`)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		header string
		wantOK bool
	}{
		{"valid", signStripe(t, secret, now, body), true},
		{"valid with extra schemes", signStripe(t, secret, now, body) + ",v0=deadbeef", true},
		{"wrong secret", signStripe(t, "whsec_other", now, body), false},
		{"stale timestamp", signStripe(t, secret, now.Add(-6*time.Minute), body), false},
		{"future timestamp", signStripe(t, secret, now.Add(6*time.Minute), body), false},
		{"malformed", "not-a-header", false},
		{"missing v1", fmt.Sprintf("t=%d", now.Unix()), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyStripeSignature(c.header, body, secret, now)
			if (err == nil) != c.wantOK {
				t.Errorf("err = %v, wantOK = %v", err, c.wantOK)
			}
		})
	}

	// Tampered body fails even with a fresh, well-formed header.
	if err := VerifyStripeSignature(signStripe(t, secret, now, body),
		[]byte(`{"type":"tampered"}`), secret, now); err == nil {
		t.Error("tampered body verified")
	}
	// Rotation: an old-secret v1 alongside the current one still verifies.
	rotated := signStripe(t, "whsec_old", now, body) + "," +
		strings.TrimPrefix(signStripe(t, secret, now, body), fmt.Sprintf("t=%d,", now.Unix()))
	if err := VerifyStripeSignature(rotated, body, secret, now); err != nil {
		t.Errorf("rotated header should verify: %v", err)
	}
}
