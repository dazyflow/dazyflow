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
			ID:          "stripe_search_customers",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Search customers",
			Summary:     "Find Stripe customers with the dashboard's search syntax — the lookup step before a refund or update.",
			Description: "Search your customers with Stripe's query syntax, e.g. email:'a@b.com' or metadata['crm_id']:'acct_42'. Wire a value into the 'Query' input for per-run lookups (a support form's email field, a sheet column). Matches come out as a JSON list on 'customers' for a For-each or a downstream Stripe step.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "customer", "search", "lookup", "billing"},
			Examples: []core.ParamsExample{
				{Title: "Customer by email", Params: json.RawMessage(`{"query":"email:'a@b.com'"}`), Notes: "Wire the email into the 'Query' input as email:'…' for per-run lookups."},
				{Title: "By your own metadata", Params: json.RawMessage(`{"query":"metadata['crm_id']:'acct_42'","limit":1}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_API_KEY", Note: "Stripe secret API key (sk_live_… / sk_test_…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "query", Label: "Query", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "customers", Label: "Customers", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"query":{"type":"string","title":"Query","description":"Stripe search syntax, e.g. email:'a@b.com'. Overridden by the 'Query' input."},
					"limit":{"type":"integer","title":"Limit","default":10,"minimum":1,"maximum":100},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["api_key","query"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSearchCustomers,
	})
}

func executeSearchCustomers(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	query, ok := textInputOr(job, "query", params.StringDefault(job.Params, "query", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Query' input must be text"), nil
	}
	if query == "" {
		return params.Err(job, "bad_param", "'query' is required — set it or wire the 'Query' input"), nil
	}
	limit := params.IntDefault(job.Params, "limit", 10)
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))

	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/customers/search?"+q.Encode(), "")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "stripe_error", "could not decode Stripe search response"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"customers": {MIME: "application/json", Inline: parsed.Data},
			"count":     {MIME: "application/json", Inline: len(parsed.Data)},
			"meta":      {MIME: "application/json", Inline: map[string]any{"count": len(parsed.Data), "query": query}},
		},
	}, nil
}
