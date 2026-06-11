package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_send_invoice",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Send invoice",
			Summary:     "Bill a Stripe customer a one-off amount and email them the invoice — draft, line item, finalize and send in one step.",
			Description: "Create a one-line invoice for a customer and have Stripe email it (a hosted page where they can pay by card or transfer). One step covers the whole API sequence — draft invoice, line item, finalize, send — and a retried run replays already-completed steps instead of double-billing. Amount is in the smallest currency unit (12000 = 120.00); customer, amount and description can all be wired in from upstream, e.g. a new row in an orders sheet. The classic flow: new-order-row → Search customers (or Create customer) → Send invoice → notify. Note: Stripe only delivers invoice emails in live mode; in test mode check the dashboard.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "invoice", "billing", "email", "payments"},
			Examples: []core.ParamsExample{
				{Title: "Invoice 120.00 USD for consulting", Params: json.RawMessage(`{"customer":"cus_NffrFeUfNV2Hib","amount":12000,"currency":"usd","description":"Consulting — May"}`), Notes: "Wire the customer id in from Search customers and the amount from a sheet row instead of typing them."},
				{Title: "Net-14 payment terms", Params: json.RawMessage(`{"customer":"cus_NffrFeUfNV2Hib","amount":50000,"currency":"sek","description":"Workshop","days_until_due":14}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_API_KEY", Note: "Stripe secret API key (sk_live_… / sk_test_…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "customer", Label: "Customer", Required: true, MIME: []string{"text/plain"}},
				{Port: "amount", Label: "Amount", MIME: []string{"text/plain", "application/json"}},
				{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "invoice_id", Label: "Invoice ID", MIME: []string{"text/plain"}},
				{Port: "hosted_invoice_url", Label: "Invoice URL", MIME: []string{"text/plain"}},
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"customer":{"type":"string","title":"Customer","description":"The cus_… id to bill. Overridden by the 'Customer' input."},
					"amount":{"type":"integer","title":"Amount","minimum":1,"description":"In the smallest currency unit (12000 = 120.00). Overridden by the 'Amount' input."},
					"currency":{"type":"string","title":"Currency","default":"usd","description":"Three-letter ISO code (usd, eur, sek, …)."},
					"description":{"type":"string","title":"Description","description":"The invoice's single line item, shown to the customer. Overridden by the 'Description' input."},
					"days_until_due":{"type":"integer","title":"Days until due","default":30,"minimum":1,"description":"Payment terms — when the invoice falls due."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["api_key","customer","amount"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeSendInvoice,
	})
}

// executeSendInvoice runs Stripe's four-call invoicing sequence as one
// user intent. Order matters: the draft invoice is created FIRST (with
// pending_invoice_items_behavior=exclude) and the line item is attached
// to it explicitly — creating the item first would leave it "pending",
// where an invoice created concurrently elsewhere could sweep it up.
// Every POST carries a per-step Idempotency-Key derived from the job's,
// so a retry after a partial failure (say, finalize timed out) replays
// steps 1–2 as Stripe-side no-ops and resumes where it failed. The one
// non-atomic leftover: if the run dies for good mid-sequence, a draft
// invoice may linger in the dashboard — visible and deletable, never
// sent.
func executeSendInvoice(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	customer, ok := textInputOr(job, "customer", params.StringDefault(job.Params, "customer", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Customer' input must be text (a cus_… id)"), nil
	}
	if customer == "" {
		return params.Err(job, "bad_param", "'customer' is required — set it or wire the 'Customer' input"), nil
	}
	amount, ok := numberInputOr(job, "amount", params.IntDefault(job.Params, "amount", 0))
	if !ok {
		return params.Err(job, "bad_input", "'Amount' input must be a whole number (smallest currency unit, e.g. 12000 = 120.00)"), nil
	}
	if amount < 1 {
		return params.Err(job, "bad_param", "'amount' is required — set it or wire the 'Amount' input (smallest currency unit)"), nil
	}
	description, ok := textInputOr(job, "description", params.StringDefault(job.Params, "description", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Description' input must be text"), nil
	}
	currency := strings.ToLower(params.StringDefault(job.Params, "currency", "usd"))
	days := params.IntDefault(job.Params, "days_until_due", 30)
	if days < 1 {
		days = 1
	}

	base := baseURL(job)
	idem := job.IdempotencyKey()

	// 1. Draft invoice. auto_advance off — WE finalize and send below;
	// Stripe's automation must not race us.
	form := url.Values{}
	form.Set("customer", customer)
	form.Set("collection_method", "send_invoice")
	form.Set("days_until_due", strconv.Itoa(days))
	form.Set("auto_advance", "false")
	form.Set("pending_invoice_items_behavior", "exclude")
	status, body, err := stripeDoIdem(ctx, job, http.MethodPost, base+"/invoices", form.Encode(), idem+":invoice")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var inv struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &inv); err != nil || inv.ID == "" {
		return params.Err(job, "stripe_error", "Stripe response had no invoice id"), nil
	}

	// 2. The line item, attached to that invoice explicitly.
	form = url.Values{}
	form.Set("customer", customer)
	form.Set("invoice", inv.ID)
	form.Set("amount", strconv.Itoa(amount))
	form.Set("currency", currency)
	if description != "" {
		form.Set("description", description)
	}
	status, body, err = stripeDoIdem(ctx, job, http.MethodPost, base+"/invoiceitems", form.Encode(), idem+":item")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	// 3. Finalize — the draft becomes a numbered, immutable invoice.
	invPath := base + "/invoices/" + url.PathEscape(inv.ID)
	status, body, err = stripeDoIdem(ctx, job, http.MethodPost, invPath+"/finalize", "", idem+":finalize")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}

	// 4. Send — Stripe emails the hosted invoice page to the customer.
	status, body, err = stripeDoIdem(ctx, job, http.MethodPost, invPath+"/send", "", idem+":send")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var sent struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		HostedInvoiceURL string `json:"hosted_invoice_url"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || sent.ID == "" {
		return params.Err(job, "stripe_error", "Stripe response had no invoice id after send"), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"invoice_id":         {MIME: "text/plain", Inline: sent.ID},
			"hosted_invoice_url": {MIME: "text/plain", Inline: sent.HostedInvoiceURL},
			"status":             {MIME: "text/plain", Inline: sent.Status},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"id": sent.ID, "status": sent.Status, "url": sent.HostedInvoiceURL,
				"customer": customer, "amount": amount, "currency": currency,
				"amount_display": formatPriceAmount(int64(amount), currency),
			}},
		},
	}, nil
}
