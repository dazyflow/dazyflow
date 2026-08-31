// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_on_payment_failed",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "On payment failed",
			Summary:     "Trigger that fires when a Stripe payment attempt fails, with the decline reason ready for an alert.",
			Description: "Starts the flow when a payment attempt fails in your Stripe account (a payment_intent.payment_failed webhook event). Setup is the same endpoint as the payment trigger: point a Stripe webhook at https://<your-dazyflow-host>/api/v1/events/stripe/<tenant>, subscribe it to payment_intent.payment_failed, and save the endpoint's signing secret (whsec_…) as a secret named STRIPE_WEBHOOK_SECRET. The 'Failure reason' output carries Stripe's decline message ('Your card was declined.') — connect it with the amount and payer email into a notify step.",
			Integration: "Stripe",
			Category:    "trigger",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "trigger", "payment", "failed", "decline", "webhook", "events", "billing"},
			Examples: []core.ParamsExample{
				{
					Title:  "Default — fire on every failed payment attempt",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect 'Failure reason', 'Amount (display)' and 'Customer email' into a notify step for a payment-trouble alert.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_WEBHOOK_SECRET", Note: "Webhook signing secret (whsec_…)."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			// Same payment-event outputs as stripe_on_payment, with a
			// 'Failure reason' pin prepended (Stripe's decline message).
			Outputs: append([]core.Port{
				{Port: "failure_message", Label: "Failure reason", MIME: []string{"text/plain"}},
			}, paymentTriggerOutputs()...),
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			Idempotent:   false,
		},
		Execute: executeStripeOnPaymentFailed,
	})
}

// executeStripeOnPaymentFailed is the standalone-execution path — only
// called when a graph is run manually. Mirrors stripe_on_payment.
func executeStripeOnPaymentFailed(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return noPaymentTriggerData(job,
		"This trigger only fires when a real payment_intent.payment_failed webhook arrives. To test it, use a Stripe test card that declines (e.g. 4000 0000 0000 0002); running the flow manually leaves the trigger with no event to feed the steps after it.",
		"stripe_on_payment_failed is pre-completed by the daemon's Stripe events handler when a failed-payment event arrives. Standalone execution has no event payload to emit.",
	)
}
