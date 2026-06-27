// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Minimal Stripe client — just the two calls the billing flow needs
// (Checkout session, billing-portal session) plus webhook signature
// verification. Hand-rolled over Stripe's form-encoded HTTP API instead
// of pulling in stripe-go: same trade the /metrics endpoint made — two
// endpoints don't justify a dependency with hundreds of types.
//
// The secret key is operator config (env), never tenant input, so these
// calls don't route through the SSRF guard the tenant-supplied-URL drops
// use.

// maxStripeResponseBytes caps how much of a Stripe response we buffer.
const maxStripeResponseBytes = 1 << 20 // 1 MiB

type StripeClient struct {
	// SecretKey is the sk_live_/sk_test_ API key.
	SecretKey string
	// PriceID is the recurring price the pro plan subscribes to.
	PriceID string
	// BaseURL overrides the API host (tests). Empty = api.stripe.com.
	BaseURL string

	httpc *http.Client
}

func NewStripeClient(secretKey, priceID string) *StripeClient {
	return &StripeClient{
		SecretKey: secretKey,
		PriceID:   priceID,
		httpc:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *StripeClient) base() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return "https://api.stripe.com"
}

// post runs one form-encoded Stripe API call and decodes the JSON
// response, surfacing Stripe's error.message on non-2xx.
func (c *StripeClient) post(ctx context.Context, path string, form url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.SecretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stripe %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxStripeResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("stripe %s: read response: %w", path, err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("stripe %s: decode response: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := resp.Status
		if e, ok := body["error"].(map[string]any); ok {
			if m, ok := e["message"].(string); ok && m != "" {
				msg = m
			}
		}
		return nil, fmt.Errorf("stripe %s: %s", path, msg)
	}
	return body, nil
}

// CreateCheckoutSession mints a subscription Checkout session for the
// tenant and returns its hosted URL. The tenant rides along twice: as
// client_reference_id on the session (read by checkout.session.completed)
// and as subscription metadata (read by customer.subscription.* events),
// so every webhook event maps back to the tenant without a reverse index.
//
// customerID, when non-empty, pins the session to an existing Stripe
// customer so a re-subscribe (after a real lapse) reuses the same customer
// record instead of spawning a duplicate — keeps the portal and history
// complete. Empty lets Stripe create the customer (first-time upgrade).
func (c *StripeClient) CreateCheckoutSession(ctx context.Context, tenant, customerID, successURL, cancelURL string) (string, error) {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("line_items[0][price]", c.PriceID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", tenant)
	form.Set("subscription_data[metadata][tenant]", tenant)
	if customerID != "" {
		form.Set("customer", customerID)
	}
	body, err := c.post(ctx, "/v1/checkout/sessions", form)
	if err != nil {
		return "", err
	}
	u, _ := body["url"].(string)
	if u == "" {
		return "", fmt.Errorf("stripe checkout session has no url")
	}
	return u, nil
}

// CreatePortalSession mints a billing-portal session (manage / cancel
// the subscription) for an existing Stripe customer.
func (c *StripeClient) CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	form := url.Values{}
	form.Set("customer", customerID)
	form.Set("return_url", returnURL)
	body, err := c.post(ctx, "/v1/billing_portal/sessions", form)
	if err != nil {
		return "", err
	}
	u, _ := body["url"].(string)
	if u == "" {
		return "", fmt.Errorf("stripe portal session has no url")
	}
	return u, nil
}

// stripeSignatureTolerance bounds how stale a webhook timestamp may be —
// Stripe's own SDKs default to 5 minutes.
const stripeSignatureTolerance = 5 * time.Minute

// VerifyStripeSignature checks a Stripe-Signature header against the raw
// request body. Scheme (https://docs.stripe.com/webhooks#verify-manually):
// the header carries `t=<unix>,v1=<hex>,...`; the signed payload is
// `<t>.<body>`; v1 is HMAC-SHA256 under the endpoint's signing secret.
// Multiple v1 entries (secret rotation) are each tried; the timestamp
// must be within tolerance of now to stop replay.
func VerifyStripeSignature(header string, body []byte, secret string, now time.Time) error {
	var ts int64
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts, _ = strconv.ParseInt(v, 10, 64)
		case "v1":
			if sig, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, sig)
			}
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return fmt.Errorf("malformed Stripe-Signature header")
	}
	age := now.Sub(time.Unix(ts, 0))
	if age > stripeSignatureTolerance || age < -stripeSignatureTolerance {
		return fmt.Errorf("webhook timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, sig := range sigs {
		if hmac.Equal(sig, want) {
			return nil
		}
	}
	return fmt.Errorf("signature mismatch")
}
