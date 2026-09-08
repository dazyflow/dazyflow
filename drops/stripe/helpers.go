// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The native Stripe connectors.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// Caps the buffered response, so a large list cannot exhaust memory.
const maxResponseBytes = 16 << 20 // 16 MiB

var httpBase = apibase.New("https://api.stripe.com/v1")

func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string { return httpBase.For(job) }

// Per-tenant, so a flow carries no key.
func stripeConnectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "api_key", Label: "Secret API key", Secret: true, Required: true, Placeholder: "sk_live_… / sk_test_…"},
	}
}

// Injected from the connection, or typed per node as an override.
func resolveAPIKey(job core.Job) (string, error) {
	key, _ := params.StringOpt(job.Params, "api_key")
	if key == "" {
		return "", fmt.Errorf("Stripe is not connected: add your secret API key on the Apps page (Stripe)")
	}
	return key, nil
}

// Stripe takes form encoding, not JSON.
func stripeDo(ctx context.Context, job core.Job, method, url string, form string) (int, []byte, error) {
	return stripeDoIdem(ctx, job, method, url, form, job.IdempotencyKey())
}

// For the non-idempotent writes: a retry must not charge twice.
func stripeDoIdem(ctx context.Context, job core.Job, method, url, form, idemKey string) (int, []byte, error) {
	timeoutMS := params.TimeoutMS(job, 15000)
	apiKey, err := resolveAPIKey(job)
	if err != nil {
		return 0, nil, err
	}
	var b []byte
	if form != "" {
		b = []byte(form)
	}
	headers := map[string]string{"Authorization": "Bearer " + apiKey}
	if form != "" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	if method == http.MethodPost {
		headers["Idempotency-Key"] = idemKey
	}
	// Tenant-supplied, so net.Do guards the dial.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, b, timeoutMS, maxResponseBytes)
	return status, raw, err
}

func extractStripeError(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		if e.Error.Code != "" {
			return e.Error.Code + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
}

// A wired port overrides the param.
func numberInputOr(job core.Job, port string, fallback int) (int, bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	fromText := func(s string) (int, bool) {
		s = strings.TrimSpace(s)
		if s == "" {
			return fallback, true
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	switch v := in.Inline.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case string:
		return fromText(v)
	case []byte:
		return fromText(string(v))
	}
	return 0, false
}

func paymentTriggerOutputs() []core.Port {
	// Mirrors daemon.paymentPorts, which is what actually fills them.
	return []core.Port{
		{Port: "amount_display", Label: "Amount (display)", MIME: []string{"text/plain"}, Example: json.RawMessage(`"249.00 SEK"`)},
		{Port: "amount", Label: "Amount (cents/öre)", MIME: []string{"text/plain"}, Example: json.RawMessage(`"24900"`)},
		{Port: "currency", Label: "Currency", MIME: []string{"text/plain"}, Example: json.RawMessage(`"SEK"`)},
		{Port: "customer_email", Label: "Customer email", MIME: []string{"text/plain"}, Example: json.RawMessage(`"anna@nordkraft.se"`)},
		{Port: "description", Label: "Description", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Faktura 4471"`)},
		{Port: "payment_id", Label: "Payment ID", MIME: []string{"text/plain"}, Example: json.RawMessage(`"pi_3QxPkzFk9mNaB1cD"`)},
		{Port: "payment", Label: "Payment intent", MIME: []string{"application/json"},
			Example: json.RawMessage(`{"id":"pi_3QxPkzFk9mNaB1cD","amount":24900,"amount_received":24900,"currency":"sek","receipt_email":"anna@nordkraft.se","description":"Faktura 4471"}`)},
		{Port: "event", Label: "Full event", MIME: []string{"application/json"},
			Example: json.RawMessage(`{"id":"evt_3QxPkzFk9mNaB1cD","type":"payment_intent.succeeded","data":{"object":{"id":"pi_3QxPkzFk9mNaB1cD","amount":24900,"currency":"sek","receipt_email":"anna@nordkraft.se","description":"Faktura 4471"}}}`)},
	}
}

func noPaymentTriggerData(job core.Job, message, details string) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: message,
			Details: details,
		},
	}, nil
}

func stripeFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "stripe", "Stripe", status, body, err, extractStripeError)
}
