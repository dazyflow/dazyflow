package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_list_subscriptions",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "List subscriptions",
			Summary:     "List a customer's Stripe subscriptions (or sweep all of them by status) — the lookup step before a cancel.",
			Description: "List subscriptions, scoped to one customer (wire a cus_… id in from Search customers) or across the whole account when Customer is empty — e.g. a daily past_due sweep. The matches come out as a JSON list on 'subscriptions'; 'first_id' carries the first match's sub_… id so the common one-subscription case wires straight into Cancel subscription without a For-each.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "subscription", "billing", "lookup"},
			Examples: []core.ParamsExample{
				{Title: "A customer's active subscriptions", Params: json.RawMessage(`{"customer":"cus_NffrFeUfNV2Hib"}`), Notes: "Wire the customer id in from Search customers; wire 'first_id' into Cancel subscription."},
				{Title: "Dunning sweep — every past-due subscription", Params: json.RawMessage(`{"status":"past_due","limit":100}`), Notes: "Schedule-trigger this and For-each the 'subscriptions' list into a notify step."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_API_KEY", Note: "Stripe secret API key (sk_live_… / sk_test_…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "customer", Label: "Customer", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "subscriptions", Label: "Subscriptions", MIME: []string{"application/json"}},
				{Port: "first_id", Label: "First ID", MIME: []string{"text/plain"}},
				{Port: "count", Label: "Count", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"customer":{"type":"string","format":"stripe-customer","title":"Customer","description":"Pick a customer to list only their subscriptions, or leave empty for the whole account (e.g. a past-due sweep). Listed once the STRIPE_API_KEY secret is set. Overridden by the 'Customer' input."},
					"status":{"type":"string","title":"Status","default":"active","enum":["active","trialing","past_due","unpaid","paused","canceled","all"],"enumNames":["Active","On trial","Past due","Unpaid","Paused","Canceled","All states"],"description":"Which subscription states to list."},
					"limit":{"type":"integer","title":"Limit","default":25,"minimum":1,"maximum":100},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["api_key"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeListSubscriptions,
	})
}

func executeListSubscriptions(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	customer, ok := textInputOr(job, "customer", params.StringDefault(job.Params, "customer", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Customer' input must be text (a cus_… id)"), nil
	}
	limit := params.IntDefault(job.Params, "limit", 25)
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("status", params.StringDefault(job.Params, "status", "active"))
	if customer != "" {
		q.Set("customer", customer)
	}

	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/subscriptions?"+q.Encode(), "")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "stripe_error", "could not decode Stripe subscriptions response"), nil
	}
	firstID := ""
	if len(parsed.Data) > 0 {
		firstID, _ = parsed.Data[0]["id"].(string)
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"subscriptions": {MIME: "application/json", Inline: parsed.Data},
			"first_id":      {MIME: "text/plain", Inline: firstID},
			"count":         {MIME: "application/json", Inline: len(parsed.Data)},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"count": len(parsed.Data), "customer": customer,
			}},
		},
	}, nil
}
