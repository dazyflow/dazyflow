package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_create_payment_link",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Create payment link",
			Summary:     "Mint a shareable Stripe payment link for a price — wire the URL straight into an email or Slack message.",
			Description: "Create a payment link for one of your Stripe Prices. Pick the price on the step (listed from your account) or wire a price_… id in from upstream — the input overrides the param, e.g. a per-row price from a sheet. The hosted checkout URL comes out on the 'url' pin — the classic flow is new-order-row → payment link → email/Slack it. Quantity can be wired in too; retries reuse the same Idempotency-Key so a flaky run can't mint duplicate links.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "payment", "link", "checkout", "billing"},
			Examples: []core.ParamsExample{
				{Title: "Link for one unit of a price", Params: json.RawMessage(`{"price":"price_1MoC3TLkdIwHu7ixcIbKelAC"}`), Notes: "Wire the 'url' output into Gmail send or Slack message."},
				{Title: "Quantity from upstream", Params: json.RawMessage(`{"price":"price_1MoC3TLkdIwHu7ixcIbKelAC"}`), Notes: "Wire a number into the 'Quantity' input — e.g. the ordered amount from a sheet row."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_API_KEY", Note: "Stripe secret API key (sk_live_… / sk_test_…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "price", Label: "Price", MIME: []string{"text/plain"}},
				{Port: "quantity", Label: "Quantity", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "url", Label: "Payment URL", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"price":{"type":"string","format":"stripe-price","title":"Price","description":"One of your Stripe Prices, listed from your account once the STRIPE_API_KEY secret is set (Products → Pricing in the dashboard). Overridden by the 'Price' input when connected."},
					"quantity":{"type":"integer","title":"Quantity","default":1,"minimum":1,"maximum":999999,"description":"Units of the price (1–999999, Stripe's per-line-item limit). Overridden by the 'Quantity' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["api_key","price"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCreatePaymentLink,
	})
}

func executeCreatePaymentLink(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	price, ok := textInputOr(job, "price", params.StringDefault(job.Params, "price", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Price' input must be text (a price_… id)"), nil
	}
	if price == "" {
		return params.Err(job, "bad_param", "'price' is required — pick one on the step or wire the 'Price' input"), nil
	}
	quantity, ok := numberInputOr(job, "quantity", params.IntDefault(job.Params, "quantity", 1))
	if !ok {
		return params.Err(job, "bad_input", "'Quantity' input must be a whole number"), nil
	}
	// Bound both ends: the UI clamps, but a wired 'Quantity' input bypasses
	// the form, so the run path enforces Stripe's 1–999999 line-item range.
	if quantity < 1 || quantity > 999999 {
		return params.Err(job, "bad_param", "quantity must be between 1 and 999999"), nil
	}

	form := url.Values{}
	form.Set("line_items[0][price]", price)
	form.Set("line_items[0][quantity]", strconv.Itoa(quantity))

	status, body, err := stripeDo(ctx, job, http.MethodPost, baseURL(job)+"/payment_links", form.Encode())
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.URL == "" {
		return params.Err(job, "stripe_error", "Stripe response had no payment link url"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"url": {MIME: "text/plain", Inline: parsed.URL},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": parsed.ID, "url": parsed.URL, "price": price, "quantity": quantity,
			}},
		},
	}, nil
}
