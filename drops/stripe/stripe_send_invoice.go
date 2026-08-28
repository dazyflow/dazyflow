// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// stripeCurrencies is Stripe's supported presentment-currency set (ISO 4217
// code → display name), majors first then alphabetical. It populates the
// Send invoice currency combobox (format:"suggest"); codes are stored/sent
// lowercase, the name is display only. The field is a free-text combobox, so
// a code not listed here (or a ${item.currency} reference) still works — this
// is the suggestion list, not a whitelist.
var stripeCurrencies = []struct{ code, name string }{
	{"usd", "US Dollar"}, {"eur", "Euro"}, {"gbp", "British Pound"},
	{"sek", "Swedish Krona"}, {"nok", "Norwegian Krone"}, {"dkk", "Danish Krone"},
	{"chf", "Swiss Franc"}, {"cad", "Canadian Dollar"}, {"aud", "Australian Dollar"},
	{"nzd", "New Zealand Dollar"}, {"jpy", "Japanese Yen"}, {"cny", "Chinese Yuan"},
	{"hkd", "Hong Kong Dollar"}, {"sgd", "Singapore Dollar"}, {"inr", "Indian Rupee"},
	{"aed", "UAE Dirham"}, {"afn", "Afghan Afghani"}, {"all", "Albanian Lek"},
	{"amd", "Armenian Dram"}, {"ang", "Netherlands Antillean Guilder"},
	{"aoa", "Angolan Kwanza"}, {"ars", "Argentine Peso"}, {"awg", "Aruban Florin"},
	{"azn", "Azerbaijani Manat"}, {"bam", "Bosnia-Herzegovina Convertible Mark"},
	{"bbd", "Barbadian Dollar"}, {"bdt", "Bangladeshi Taka"}, {"bgn", "Bulgarian Lev"},
	{"bif", "Burundian Franc"}, {"bmd", "Bermudian Dollar"}, {"bnd", "Brunei Dollar"},
	{"bob", "Bolivian Boliviano"}, {"brl", "Brazilian Real"}, {"bsd", "Bahamian Dollar"},
	{"bwp", "Botswanan Pula"}, {"byn", "Belarusian Ruble"}, {"bzd", "Belize Dollar"},
	{"cdf", "Congolese Franc"}, {"clp", "Chilean Peso"}, {"cop", "Colombian Peso"},
	{"crc", "Costa Rican Colón"}, {"cve", "Cape Verdean Escudo"}, {"czk", "Czech Koruna"},
	{"djf", "Djiboutian Franc"}, {"dop", "Dominican Peso"}, {"dzd", "Algerian Dinar"},
	{"egp", "Egyptian Pound"}, {"etb", "Ethiopian Birr"}, {"fjd", "Fijian Dollar"},
	{"fkp", "Falkland Islands Pound"}, {"gel", "Georgian Lari"}, {"gip", "Gibraltar Pound"},
	{"gmd", "Gambian Dalasi"}, {"gnf", "Guinean Franc"}, {"gtq", "Guatemalan Quetzal"},
	{"gyd", "Guyanaese Dollar"}, {"hnl", "Honduran Lempira"}, {"htg", "Haitian Gourde"},
	{"huf", "Hungarian Forint"}, {"idr", "Indonesian Rupiah"}, {"ils", "Israeli New Shekel"},
	{"isk", "Icelandic Króna"}, {"jmd", "Jamaican Dollar"}, {"kes", "Kenyan Shilling"},
	{"kgs", "Kyrgystani Som"}, {"khr", "Cambodian Riel"}, {"kmf", "Comorian Franc"},
	{"krw", "South Korean Won"}, {"kyd", "Cayman Islands Dollar"}, {"kzt", "Kazakhstani Tenge"},
	{"lak", "Laotian Kip"}, {"lbp", "Lebanese Pound"}, {"lkr", "Sri Lankan Rupee"},
	{"lrd", "Liberian Dollar"}, {"lsl", "Lesotho Loti"}, {"mad", "Moroccan Dirham"},
	{"mdl", "Moldovan Leu"}, {"mga", "Malagasy Ariary"}, {"mkd", "Macedonian Denar"},
	{"mmk", "Myanmar Kyat"}, {"mnt", "Mongolian Tugrik"}, {"mop", "Macanese Pataca"},
	{"mur", "Mauritian Rupee"}, {"mvr", "Maldivian Rufiyaa"}, {"mwk", "Malawian Kwacha"},
	{"mxn", "Mexican Peso"}, {"myr", "Malaysian Ringgit"}, {"mzn", "Mozambican Metical"},
	{"nad", "Namibian Dollar"}, {"ngn", "Nigerian Naira"}, {"nio", "Nicaraguan Córdoba"},
	{"npr", "Nepalese Rupee"}, {"pab", "Panamanian Balboa"}, {"pen", "Peruvian Sol"},
	{"pgk", "Papua New Guinean Kina"}, {"php", "Philippine Peso"}, {"pkr", "Pakistani Rupee"},
	{"pln", "Polish Złoty"}, {"pyg", "Paraguayan Guarani"}, {"qar", "Qatari Riyal"},
	{"ron", "Romanian Leu"}, {"rsd", "Serbian Dinar"}, {"rwf", "Rwandan Franc"},
	{"sar", "Saudi Riyal"}, {"sbd", "Solomon Islands Dollar"}, {"scr", "Seychellois Rupee"},
	{"shp", "Saint Helena Pound"}, {"sle", "Sierra Leonean Leone"}, {"sos", "Somali Shilling"},
	{"srd", "Surinamese Dollar"}, {"std", "São Tomé & Príncipe Dobra"}, {"szl", "Swazi Lilangeni"},
	{"thb", "Thai Baht"}, {"tjs", "Tajikistani Somoni"}, {"top", "Tongan Paʻanga"},
	{"try", "Turkish Lira"}, {"ttd", "Trinidad & Tobago Dollar"}, {"twd", "New Taiwan Dollar"},
	{"tzs", "Tanzanian Shilling"}, {"uah", "Ukrainian Hryvnia"}, {"ugx", "Ugandan Shilling"},
	{"uyu", "Uruguayan Peso"}, {"uzs", "Uzbekistani Som"}, {"vnd", "Vietnamese Dong"},
	{"vuv", "Vanuatu Vatu"}, {"wst", "Samoan Tala"}, {"xaf", "Central African CFA Franc"},
	{"xcd", "East Caribbean Dollar"}, {"xof", "West African CFA Franc"}, {"xpf", "CFP Franc"},
	{"yer", "Yemeni Rial"}, {"zar", "South African Rand"}, {"zmw", "Zambian Kwacha"},
}

// currencyEnumJSON returns the JSON arrays for the currency field's `enum`
// (lowercase codes) and `enumNames` ("USD — US Dollar"), built from
// stripeCurrencies so the 130-odd entries live in a reviewable Go table
// instead of a giant inline schema literal.
func stripeCurrencyLists() (codes, labels []string) {
	codes = make([]string, len(stripeCurrencies))
	labels = make([]string, len(stripeCurrencies))
	for i, c := range stripeCurrencies {
		codes[i] = c.code
		labels[i] = strings.ToUpper(c.code) + " — " + c.name
	}
	return codes, labels
}

// sendInvoiceParamsSchema is the drop's ParamsSchema with the currency
// enum/enumNames filled in from stripeCurrencies. Built once at package load
// (before init registers the drop).
var sendInvoiceParamsSchema = buildSendInvoiceParamsSchema()

func buildSendInvoiceParamsSchema() json.RawMessage {
	// Marshalled from a map rather than spliced into a format string. The
	// values are static today, so the Sprintf form was safe — but it was the
	// one schema in the catalog built by string interpolation, and a schema
	// that can be malformed by its own inputs is a footgun waiting for the
	// day a currency label contains a quote. Marshalling makes that
	// impossible by construction.
	currencies, currencyNames := stripeCurrencyLists()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"customer": map[string]any{
				"type": "string", "format": "stripe-customer", "title": "Customer",
				"description": "Pick the customer to bill — listed from your account once the STRIPE_API_KEY secret is set. Overridden by the 'Customer' input when connected.",
			},
			"amount": map[string]any{
				"type": "integer", "title": "Amount", "minimum": 1,
				"description": "In the smallest currency unit (12000 = 120.00). Overridden by the 'Amount' input.",
			},
			"currency": map[string]any{
				"type": "string", "title": "Currency", "format": "suggest", "default": "usd",
				"enum": currencies, "enumNames": currencyNames,
				"description": "Three-letter ISO code (stored lowercase). Pick a common one, type any Stripe-supported code, or use a reference like ${item.currency} for a per-row currency.",
			},
			"description": map[string]any{
				"type": "string", "title": "Description",
				"description": "The invoice's single line item, shown to the customer. Overridden by the 'Description' input.",
			},
			"days_until_due": map[string]any{
				"type": "integer", "title": "Days until due", "default": 30, "minimum": 1,
				"description": "Payment terms — when the invoice falls due.",
			},
			"base_url": map[string]any{
				"type": "string", "description": "Override the API host (testing).",
			},
			"timeout_ms": map[string]any{
				"type": "integer", "default": 15000, "minimum": 1,
				"description": "Hard deadline for the request, in milliseconds.",
			},
		},
		"required": []string{"customer", "amount"},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		// Unreachable: the map holds only JSON-native values. Panicking at
		// package load beats registering a drop with a broken schema.
		panic("stripe_send_invoice: build params schema: " + err.Error())
	}
	return raw
}

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_send_invoice",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "Send invoice",
			Summary:     "Bill a Stripe customer a one-off amount and email them the invoice — draft, line item, finalize and send in one step.",
			Description: "Create a one-line invoice for a customer and have Stripe email it (a hosted page where they can pay by card or transfer). One step covers the whole API sequence — draft invoice, line item, finalize, send — and a retried run replays already-completed steps instead of double-billing. Amount is in the smallest currency unit (12000 = 120.00); customer, amount and description can all be connected from an earlier step, e.g. a new row in an orders sheet. The classic flow: new-order-row → Search customers (or Create customer) → Send invoice → notify. Note: Stripe only delivers invoice emails in live mode; in test mode check the dashboard.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "invoice", "billing", "email", "payments"},
			Examples: []core.ParamsExample{
				{Title: "Invoice 120.00 USD for consulting", Params: json.RawMessage(`{"customer":"cus_NffrFeUfNV2Hib","amount":12000,"currency":"usd","description":"Consulting — May"}`), Notes: "Connect the customer id in from Search customers and the amount from a sheet row instead of typing them."},
				{Title: "Net-14 payment terms", Params: json.RawMessage(`{"customer":"cus_NffrFeUfNV2Hib","amount":50000,"currency":"sek","description":"Workshop","days_until_due":14}`)},
			},
			ConnectionFields: stripeConnectionFields(),
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "customer", Label: "Customer", Required: true, MIME: []string{"text/plain"}},
				{Port: "amount", Label: "Amount (smallest unit)", MIME: []string{"text/plain", "application/json"}},
				{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "invoice_id", Label: "Invoice ID", MIME: []string{"text/plain"}},
				{Port: "hosted_invoice_url", Label: "Invoice URL", MIME: []string{"text/plain"}},
				{Port: "status", Label: "Status", MIME: []string{"text/plain"}},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: sendInvoiceParamsSchema,
			Idempotent:   false,
			RetryPolicy:  core.RetryExponentialBackoff,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
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
	customer, ok := params.TextInputOr(job, "customer", params.StringDefault(job.Params, "customer", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Customer' input must be text (a cus_… id)"), nil
	}
	if customer == "" {
		return params.Err(job, "bad_param", "'customer' is required — set it or connect the 'Customer' input"), nil
	}
	amount, ok := numberInputOr(job, "amount", params.IntDefault(job.Params, "amount", 0))
	if !ok {
		return params.Err(job, "bad_input", "'Amount' input must be a whole number (smallest currency unit, e.g. 12000 = 120.00)"), nil
	}
	if amount < 1 {
		return params.Err(job, "bad_param", "'amount' is required — set it or connect the 'Amount' input (smallest currency unit)"), nil
	}
	description, ok := params.TextInputOr(job, "description", params.StringDefault(job.Params, "description", ""))
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
