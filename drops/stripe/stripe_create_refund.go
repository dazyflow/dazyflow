// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_create_refund",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Create refund",
			Summary:     "Refund a Stripe payment, fully or partially — pairs naturally with an approval step before it.",
			Description: "Refund a payment by its payment_intent id (pi_…). Leave Amount empty for a full refund, or set it in the smallest currency unit (cents/öre) for a partial one — the id and the amount can both be connected from an earlier step, e.g. a support form's fields. Put an Approval step before this one for the classic 'approve in Slack → refund' flow. Retries reuse the same Idempotency-Key, so a flaky run can't refund twice.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "refund", "payment", "billing", "support"},
			Examples: []core.ParamsExample{
				{Title: "Full refund", Params: json.RawMessage(`{"payment_intent":"pi_3MtwBwLkdIwHu7ix28a3tqPa"}`), Notes: "Connect the id into the 'Payment intent' input from a form or webhook instead of typing it."},
				{Title: "Partial refund, marked as requested by the customer", Params: json.RawMessage(`{"payment_intent":"pi_3MtwBwLkdIwHu7ix28a3tqPa","amount":500,"reason":"requested_by_customer"}`), Notes: "amount is in the smallest currency unit — 500 = €5.00 / $5.00."},
			},
			ConnectionFields: stripeConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "payment_intent", Label: "Payment intent", Required: true, MIME: []string{"text/plain"}},
				{Port: "amount", Label: "Amount (smallest unit)", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "refund_id", Label: "Refund ID", MIME: []string{"text/plain"}},
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"payment_intent":{"type":"string","format":"stripe-payment-intent","title":"Payment","description":"Pick the payment to refund — your account's succeeded payments, listed once the STRIPE_API_KEY secret is set. Overridden by the 'Payment intent' input when connected."},
					"amount":{"type":"integer","title":"Amount (smallest unit)","minimum":1,"description":"Leave empty to refund the whole payment. For a partial refund, enter the amount in the currency's smallest unit — e.g. 500 = 5.00 USD/EUR (cents), but 500 = 500 JPY (yen has no smaller unit). Overridden by the 'Amount' input when connected."},
					"reason":{"type":"string","title":"Reason","enum":["","duplicate","fraudulent","requested_by_customer"],"enumNames":["(none)","Duplicate","Fraudulent","Requested by customer"],"description":"Stripe's refund-reason label, shown in the dashboard."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["payment_intent"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeCreateRefund,
	})
}

func executeCreateRefund(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	pi, ok := params.TextInputOr(job, "payment_intent", params.StringDefault(job.Params, "payment_intent", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Payment intent' input must be text"), nil
	}
	if pi == "" {
		return params.Err(job, "bad_param", "'payment_intent' is required — set it or connect the 'Payment intent' input"), nil
	}

	amount, ok := numberInputOr(job, "amount", params.IntDefault(job.Params, "amount", 0))
	if !ok {
		return params.Err(job, "bad_input", "'Amount' input must be a whole number (smallest currency unit, e.g. 500 = 5.00)"), nil
	}
	if amount < 0 {
		return params.Err(job, "bad_input", "'Amount' cannot be negative"), nil
	}

	form := url.Values{}
	form.Set("payment_intent", pi)
	if amount > 0 {
		form.Set("amount", strconv.Itoa(amount))
	}
	if reason := params.StringDefault(job.Params, "reason", ""); reason != "" {
		form.Set("reason", reason)
	}

	status, body, err := stripeDo(ctx, job, http.MethodPost, baseURL(job)+"/refunds", form.Encode())
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Amount int64  `json:"amount"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.ID == "" {
		return params.Err(job, "stripe_error", "Stripe response had no refund id"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"refund_id": {MIME: "text/plain", Inline: parsed.ID},
			"status":    {MIME: "text/plain", Inline: parsed.Status},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": parsed.ID, "status": parsed.Status, "amount": parsed.Amount, "payment_intent": pi,
			}},
		},
	}, nil
}
