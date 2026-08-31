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
			ID:          "stripe_on_subscription_canceled",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "On subscription canceled",
			Summary:     "Trigger that fires when a Stripe subscription ends — churn alerts, offboarding, win-back flows.",
			Description: "Starts the flow when a subscription is canceled in your Stripe account (a customer.subscription.deleted webhook event — fires when the subscription actually ends, so a scheduled at-period-end cancel triggers at the period's end). Setup is the same endpoint as the payment trigger: point a Stripe webhook at https://<your-dazyflow-host>/api/v1/events/stripe/<tenant>, subscribe it to customer.subscription.deleted, and save the endpoint's signing secret (whsec_…) as a secret named STRIPE_WEBHOOK_SECRET. Outputs the subscription and customer ids, the plan label and when it ended — connect the customer id into Search customers for the email.",
			Integration: "Stripe",
			Category:    "trigger",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "trigger", "subscription", "canceled", "churn", "webhook", "events", "billing"},
			Examples: []core.ParamsExample{
				{
					Title:  "Default — fire whenever a subscription ends",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect 'Plan' and 'Customer' into a notify step for a churn alert, or into Search customers → email for a win-back message.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_WEBHOOK_SECRET", Note: "Webhook signing secret (whsec_…)."},
			},
			ExecutionModel: core.ExecutionTrigger,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "subscription_id", Label: "Subscription ID", MIME: []string{"text/plain"}},
				{Port: "customer", Label: "Customer", MIME: []string{"text/plain"}},
				{Port: "plan", Label: "Plan", MIME: []string{"text/plain"}},
				{Port: "ended_at", Label: "Ended at", MIME: []string{"text/plain"}},
				{Port: "subscription", Label: "Subscription", MIME: []string{"application/json"}},
				{Port: "event", Label: "Full event", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{"type":"object"}`),
			Idempotent:   false,
		},
		Execute: executeStripeOnSubscriptionCanceled,
	})
}

// executeStripeOnSubscriptionCanceled is the standalone-execution path —
// only called when a graph is run manually. Mirrors stripe_on_payment.
func executeStripeOnSubscriptionCanceled(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: "This trigger only fires when a real customer.subscription.deleted webhook arrives. To test it, cancel a test-mode subscription in the Stripe dashboard; running the flow manually leaves the trigger with no event to feed the steps after it.",
			Details: "stripe_on_subscription_canceled is pre-completed by the daemon's Stripe events handler when a subscription-deleted event arrives. Standalone execution has no event payload to emit.",
		},
	}, nil
}
