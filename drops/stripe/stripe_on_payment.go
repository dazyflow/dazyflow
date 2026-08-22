// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_on_payment",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "On payment",
			Summary:     "Trigger that fires the moment a Stripe payment succeeds, with amount, currency and payer email.",
			Description: "Starts the flow when a payment succeeds in your Stripe account (a payment_intent.succeeded webhook event). Setup: in the Stripe dashboard add a webhook endpoint pointing at https://<your-dazyflow-host>/api/v1/events/stripe/<tenant>, subscribe it to payment_intent.succeeded, then save that endpoint's signing secret (whsec_…) as a secret named STRIPE_WEBHOOK_SECRET — each delivery's Stripe-Signature is verified against it. Outputs the amount (minor units and a display form like '49.99 USD'), currency, payer email, description, and the raw event. For polling instead of webhooks, compose Schedule → 'List events' instead.",
			Integration: "Stripe",
			Category:    "trigger",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "trigger", "payment", "webhook", "events", "billing"},
			Examples: []core.ParamsExample{
				{
					Title:  "Default — fire on every successful payment",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect 'Amount (display)' and 'Customer email' into a notify step (ntfy push / email / Slack) for a payment alert.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_WEBHOOK_SECRET", Note: "Webhook signing secret (whsec_…)."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs:        paymentTriggerOutputs(),
			ParamsSchema:   json.RawMessage(`{"type":"object"}`),
			Idempotent:     false,
		},
		Execute: executeStripeOnPayment,
	})
}

// executeStripeOnPayment is the standalone-execution path — only called
// when a graph is run manually (no webhook event seeded the node).
// Mirrors github_on_push / webhook_input.
func executeStripeOnPayment(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return noPaymentTriggerData(job,
		"This Stripe payment trigger only fires when a real payment_intent.succeeded webhook arrives. To test it, send a test event from the Stripe dashboard's webhook page (or make a test-mode payment); running the flow manually leaves the trigger with no event to feed the steps after it.",
		"stripe_on_payment is pre-completed by the daemon's Stripe events handler when a payment event arrives. Standalone execution has no event payload to emit.",
	)
}
