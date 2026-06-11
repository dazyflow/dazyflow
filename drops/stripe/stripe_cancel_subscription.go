package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_cancel_subscription",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Cancel subscription",
			Summary:     "Cancel a Stripe subscription — at period end by default, so the customer keeps what they paid for.",
			Description: "Cancel a subscription by its sub_… id. By default it stays active until the current billing period ends (the customer keeps what they paid for); switch 'At period end' off to cancel immediately. Wire the id in from List subscriptions ('first_id') or a support form, and put an Approval step before this one for the classic 'approve in Slack → cancel' flow. 'Ends at' comes out as a date for the confirmation message.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "subscription", "cancel", "billing", "support"},
			Examples: []core.ParamsExample{
				{Title: "Cancel at period end (default)", Params: json.RawMessage(`{"subscription":"sub_1MowQVLkdIwHu7ixeRlqHVzs"}`), Notes: "Wire the id into the 'Subscription' input from List subscriptions instead of typing it."},
				{Title: "Cancel immediately", Params: json.RawMessage(`{"subscription":"sub_1MowQVLkdIwHu7ixeRlqHVzs","at_period_end":false}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_API_KEY", Note: "Stripe secret API key (sk_live_… / sk_test_…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "subscription", Label: "Subscription", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
				{Port: "ends_at", Label: "Ends at", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"subscription":{"type":"string","format":"stripe-subscription","title":"Subscription","description":"Pick the subscription to cancel — listed from your account once the STRIPE_API_KEY secret is set. Overridden by the 'Subscription' input when connected."},
					"at_period_end":{"type":"boolean","title":"At period end","default":true,"description":"ON: the subscription runs until the period the customer already paid for ends. OFF: cancel right now."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["api_key","subscription"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCancelSubscription,
	})
}

func executeCancelSubscription(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	sub, ok := textInputOr(job, "subscription", params.StringDefault(job.Params, "subscription", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Subscription' input must be text (a sub_… id)"), nil
	}
	if sub == "" {
		return params.Err(job, "bad_param", "'subscription' is required — set it or wire the 'Subscription' input"), nil
	}

	// Two API shapes for one intent: at-period-end is an UPDATE
	// (cancel_at_period_end=true, POST so the Idempotency-Key makes
	// retries safe); immediate is a DELETE. Stripe doesn't honor
	// idempotency keys on DELETE, but a cancel is idempotent in effect —
	// the rare retry-after-success surfaces Stripe's "already canceled"
	// error instead of doing harm.
	var status int
	var body []byte
	var err error
	if params.BoolDefault(job.Params, "at_period_end", true) {
		status, body, err = stripeDo(ctx, job, http.MethodPost,
			baseURL(job)+"/subscriptions/"+url.PathEscape(sub), "cancel_at_period_end=true")
	} else {
		status, body, err = stripeDo(ctx, job, http.MethodDelete,
			baseURL(job)+"/subscriptions/"+url.PathEscape(sub), "")
	}
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	var parsed struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
		CurrentPeriodEnd  int64  `json:"current_period_end"`
		CanceledAt        int64  `json:"canceled_at"`
		EndedAt           int64  `json:"ended_at"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.ID == "" {
		return params.Err(job, "stripe_error", "Stripe response had no subscription id"), nil
	}
	// ends_at is when access actually stops: the paid-for period's end
	// for a scheduled cancel, the cancellation moment for an immediate
	// one. RFC3339 so it drops into a notification readably.
	endsUnix := parsed.CurrentPeriodEnd
	if !parsed.CancelAtPeriodEnd {
		if parsed.EndedAt != 0 {
			endsUnix = parsed.EndedAt
		} else if parsed.CanceledAt != 0 {
			endsUnix = parsed.CanceledAt
		}
	}
	endsAt := ""
	if endsUnix != 0 {
		endsAt = time.Unix(endsUnix, 0).UTC().Format(time.RFC3339)
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"status":  {MIME: "text/plain", Inline: parsed.Status},
			"ends_at": {MIME: "text/plain", Inline: endsAt},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": parsed.ID, "status": parsed.Status,
				"cancel_at_period_end": parsed.CancelAtPeriodEnd, "ends_at": endsAt,
			}},
		},
	}, nil
}
