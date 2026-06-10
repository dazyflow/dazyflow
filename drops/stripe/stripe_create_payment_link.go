package stripe

import (
	"context"
	"encoding/json"
	"fmt"
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
			ID:          "stripe_create_payment_link",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Create payment link",
			Summary:     "Mint a shareable Stripe payment link for a price — wire the URL straight into an email or Slack message.",
			Description: "Create a payment link for one of your Stripe Prices (a price_… id from the Stripe dashboard). The hosted checkout URL comes out on the 'url' pin — the classic flow is new-order-row → payment link → email/Slack it. Quantity can be wired in from upstream; retries reuse the same Idempotency-Key so a flaky run can't mint duplicate links.",
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
				{Port: "quantity", Label: "Quantity", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "url", Label: "Payment URL", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"price":{"type":"string","title":"Price ID","description":"The price_… id from your Stripe dashboard (Products → Pricing)."},
					"quantity":{"type":"integer","title":"Quantity","default":1,"minimum":1,"description":"Units of the price. Overridden by the 'Quantity' input."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
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
	price, err := params.String(job.Params, "price")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	quantity := params.IntDefault(job.Params, "quantity", 1)
	// The Quantity input accepts a wired number (JSON) or numeric text.
	if in, ok := job.Input["quantity"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case float64:
			quantity = int(v)
		case int:
			quantity = v
		case string:
			n, convErr := strconv.Atoi(v)
			if convErr != nil {
				return params.Err(job, "bad_input", fmt.Sprintf("'Quantity' input %q is not a number", v)), nil
			}
			quantity = n
		default:
			return params.Err(job, "bad_input", "'Quantity' input must be a number"), nil
		}
	}
	if quantity < 1 {
		return params.Err(job, "bad_param", "quantity must be at least 1"), nil
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
