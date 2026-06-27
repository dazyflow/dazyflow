// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// fakeStripe stands in for api.stripe.com: it checks auth + idempotency
// headers and serves the five endpoints the drops call. Each handler
// records the form/query it received for assertions.
type fakeStripe struct {
	srv      *httptest.Server
	lastForm url.Values
	lastIdem string
}

func newFakeStripe(t *testing.T) *fakeStripe {
	t.Helper()
	f := &fakeStripe{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_good" {
			rw.WriteHeader(401)
			fmt.Fprint(rw, `{"error":{"message":"Invalid API Key provided","code":"invalid_api_key"}}`)
			return
		}
		f.lastIdem = r.Header.Get("Idempotency-Key")
		_ = r.ParseForm()
		f.lastForm = r.Form
		switch {
		case r.Method == "POST" && r.URL.Path == "/customers":
			if r.PostForm.Get("email") == "reject@stripe.test" {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":{"message":"Invalid email address: reject@stripe.test","code":"email_invalid","type":"invalid_request_error"}}`)
				return
			}
			fmt.Fprintf(rw, `{"id":"cus_1","email":%q,"name":%q}`, r.PostForm.Get("email"), r.PostForm.Get("name"))
		case r.Method == "POST" && r.URL.Path == "/payment_links":
			if r.PostForm.Get("line_items[0][price]") == "price_bad" {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":{"message":"No such price: price_bad","code":"resource_missing"}}`)
				return
			}
			fmt.Fprint(rw, `{"id":"plink_1","url":"https://buy.stripe.com/test_abc"}`)
		case r.Method == "POST" && r.URL.Path == "/refunds":
			if r.PostForm.Get("payment_intent") == "pi_missing" {
				rw.WriteHeader(404)
				fmt.Fprint(rw, `{"error":{"message":"No such payment_intent: pi_missing","code":"resource_missing"}}`)
				return
			}
			fmt.Fprint(rw, `{"id":"re_1","status":"succeeded","amount":500}`)
		case r.Method == "GET" && r.URL.Path == "/events":
			if r.Form.Get("ending_before") == "evt_2" {
				fmt.Fprint(rw, `{"data":[{"id":"evt_3","type":"payment_intent.succeeded"}]}`)
				return
			}
			fmt.Fprint(rw, `{"data":[{"id":"evt_2","type":"payment_intent.succeeded"},{"id":"evt_1","type":"payment_intent.succeeded"}]}`)
		case r.Method == "GET" && r.URL.Path == "/subscriptions":
			if r.Form.Get("customer") == "cus_none" {
				fmt.Fprint(rw, `{"data":[]}`)
				return
			}
			fmt.Fprint(rw, `{"data":[{"id":"sub_1","status":"active","current_period_end":1767225600},{"id":"sub_2","status":"active"}]}`)
		case r.Method == "POST" && r.URL.Path == "/subscriptions/sub_1":
			fmt.Fprint(rw, `{"id":"sub_1","status":"active","cancel_at_period_end":true,"current_period_end":1767225600}`)
		case r.Method == "DELETE" && r.URL.Path == "/subscriptions/sub_1":
			fmt.Fprint(rw, `{"id":"sub_1","status":"canceled","cancel_at_period_end":false,"canceled_at":1750000000,"ended_at":1750000000}`)
		case r.URL.Path == "/subscriptions/sub_missing":
			rw.WriteHeader(404)
			fmt.Fprint(rw, `{"error":{"message":"No such subscription: sub_missing","code":"resource_missing"}}`)
		case r.Method == "POST" && r.URL.Path == "/invoices":
			fmt.Fprint(rw, `{"id":"in_1","status":"draft"}`)
		case r.Method == "POST" && r.URL.Path == "/invoiceitems":
			if r.PostForm.Get("invoice") != "in_1" {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":{"message":"invoice item not attached to the draft"}}`)
				return
			}
			fmt.Fprint(rw, `{"id":"ii_1"}`)
		case r.Method == "POST" && r.URL.Path == "/invoices/in_1/finalize":
			fmt.Fprint(rw, `{"id":"in_1","status":"open","hosted_invoice_url":"https://invoice.stripe.com/i/test_in1"}`)
		case r.Method == "POST" && r.URL.Path == "/invoices/in_1/send":
			fmt.Fprint(rw, `{"id":"in_1","status":"open","hosted_invoice_url":"https://invoice.stripe.com/i/test_in1"}`)
		case r.Method == "GET" && r.URL.Path == "/customers/search":
			if strings.Contains(r.Form.Get("query"), "nobody") {
				fmt.Fprint(rw, `{"data":[]}`)
				return
			}
			fmt.Fprint(rw, `{"data":[{"id":"cus_7","email":"a@b.com"}]}`)
		default:
			rw.WriteHeader(404)
			fmt.Fprint(rw, `{"error":{"message":"unknown path"}}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// run executes a registered drop against the fake with base params merged in.
func run(t *testing.T, f *fakeStripe, moduleID string, p map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	drop, ok := engine.Default.Get(moduleID)
	if !ok {
		t.Fatalf("module %s not registered", moduleID)
	}
	if p == nil {
		p = map[string]any{}
	}
	if _, ok := p["api_key"]; !ok {
		p["api_key"] = "sk_test_good"
	}
	p["base_url"] = f.srv.URL
	res, err := drop.Execute(context.Background(), core.Job{ID: "job-1", NodeID: "n", Params: p, Input: input}, nil)
	if err != nil {
		t.Fatalf("%s execute: %v", moduleID, err)
	}
	return res
}

func TestCreateCustomer(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_customer",
		map[string]any{"email": "a@b.com", "name": "Ada", "metadata": map[string]any{"crm_id": "x1"}}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if got := res.Output["customer_id"].Inline; got != "cus_1" {
		t.Errorf("customer_id = %v", got)
	}
	if f.lastForm.Get("metadata[crm_id]") != "x1" {
		t.Errorf("metadata not form-encoded: %v", f.lastForm)
	}
	if wantIdem := (core.Job{ID: "job-1"}).IdempotencyKey(); f.lastIdem != wantIdem {
		t.Errorf("Idempotency-Key = %q, want %q (the job's stable key)", f.lastIdem, wantIdem)
	}

	// Input port overrides the param.
	res = run(t, f, "stripe_create_customer", map[string]any{"email": "typed@x.com"},
		map[string]core.Ref{"email": {Inline: "wired@x.com"}})
	if f.lastForm.Get("email") != "wired@x.com" {
		t.Errorf("wired email lost: %v", f.lastForm.Get("email"))
	}
	_ = res

	// Missing email is a friendly param error.
	res = run(t, f, "stripe_create_customer", map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("missing email res = %+v", res)
	}

	// A Stripe API error response (400) surfaces Stripe's own message rather
	// than a generic failure — the per-action error path for create_customer.
	res = run(t, f, "stripe_create_customer", map[string]any{"email": "reject@stripe.test"}, nil)
	if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "Invalid email address") {
		t.Errorf("API-error res = %+v", res)
	}
}

func TestCreatePaymentLink(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_ok"}, nil)
	if res.Status != core.StatusOK || res.Output["url"].Inline != "https://buy.stripe.com/test_abc" {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("line_items[0][quantity]") != "1" {
		t.Errorf("default quantity = %v", f.lastForm.Get("line_items[0][quantity]"))
	}

	// Wired quantity (numbers arrive as float64 over JSON).
	run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_ok"},
		map[string]core.Ref{"quantity": {Inline: float64(3)}})
	if f.lastForm.Get("line_items[0][quantity]") != "3" {
		t.Errorf("wired quantity = %v", f.lastForm.Get("line_items[0][quantity]"))
	}

	// A fractional wired quantity is a wiring mistake, not something to
	// silently truncate.
	res = run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_ok"},
		map[string]core.Ref{"quantity": {Inline: float64(5.9)}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("fractional quantity res = %+v", res)
	}

	// The wired price overrides the param — per-row prices from upstream.
	run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_param"},
		map[string]core.Ref{"price": {Inline: "price_wired"}})
	if f.lastForm.Get("line_items[0][price]") != "price_wired" {
		t.Errorf("wired price = %v", f.lastForm.Get("line_items[0][price]"))
	}

	// No price anywhere → param error before any HTTP.
	res = run(t, f, "stripe_create_payment_link", map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("no price res = %+v", res)
	}

	// Stripe's error message reaches the user.
	res = run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_bad"}, nil)
	if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "No such price") {
		t.Errorf("bad price res = %+v", res)
	}
}

func TestCreateRefund(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_refund",
		map[string]any{"payment_intent": "pi_1", "amount": 500, "reason": "requested_by_customer"}, nil)
	if res.Status != core.StatusOK || res.Output["refund_id"].Inline != "re_1" ||
		res.Output["status"].Inline != "succeeded" {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("amount") != "500" || f.lastForm.Get("reason") != "requested_by_customer" {
		t.Errorf("form = %v", f.lastForm)
	}

	// The wired amount overrides the param — e.g. a computed partial
	// refund from a support form. Numeric text is fine (form fields).
	run(t, f, "stripe_create_refund",
		map[string]any{"payment_intent": "pi_1", "amount": 500},
		map[string]core.Ref{"amount": {Inline: "250"}})
	if f.lastForm.Get("amount") != "250" {
		t.Errorf("wired amount = %v", f.lastForm.Get("amount"))
	}

	// A non-numeric or fractional wired amount is rejected, not coerced.
	res = run(t, f, "stripe_create_refund",
		map[string]any{"payment_intent": "pi_1"},
		map[string]core.Ref{"amount": {Inline: "lots"}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("junk amount res = %+v", res)
	}
	res = run(t, f, "stripe_create_refund",
		map[string]any{"payment_intent": "pi_1"},
		map[string]core.Ref{"amount": {Inline: float64(5.5)}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("fractional amount res = %+v", res)
	}

	res = run(t, f, "stripe_create_refund", map[string]any{"payment_intent": "pi_missing"}, nil)
	if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "No such payment_intent") {
		t.Errorf("missing pi res = %+v", res)
	}
	// No payment intent at all → param error before any HTTP.
	res = run(t, f, "stripe_create_refund", map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("no pi res = %+v", res)
	}
}

func TestListSubscriptions(t *testing.T) {
	f := newFakeStripe(t)
	// Wired customer + default status; first_id carries the first match.
	res := run(t, f, "stripe_list_subscriptions", nil,
		map[string]core.Ref{"customer": {Inline: "cus_7"}})
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("customer") != "cus_7" || f.lastForm.Get("status") != "active" {
		t.Errorf("query = %v", f.lastForm)
	}
	if res.Output["first_id"].Inline != "sub_1" {
		t.Errorf("first_id = %v", res.Output["first_id"].Inline)
	}
	if res.Output["count"].Inline != 2 {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}

	// No matches: empty list, empty first_id — not an error.
	res = run(t, f, "stripe_list_subscriptions", map[string]any{"customer": "cus_none"}, nil)
	if res.Status != core.StatusOK || res.Output["first_id"].Inline != "" {
		t.Errorf("no-match res = %+v", res)
	}

	// No customer: account-wide sweep with an explicit status.
	run(t, f, "stripe_list_subscriptions", map[string]any{"status": "past_due"}, nil)
	if f.lastForm.Get("status") != "past_due" || f.lastForm.Has("customer") {
		t.Errorf("sweep query = %v", f.lastForm)
	}
}

func TestCancelSubscription(t *testing.T) {
	f := newFakeStripe(t)
	// Default: at period end — an UPDATE, carrying the idempotency key.
	res := run(t, f, "stripe_cancel_subscription", nil,
		map[string]core.Ref{"subscription": {Inline: "sub_1"}})
	if res.Status != core.StatusOK || res.Output["status"].Inline != "active" {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("cancel_at_period_end") != "true" {
		t.Errorf("form = %v", f.lastForm)
	}
	if wantIdem := (core.Job{ID: "job-1"}).IdempotencyKey(); f.lastIdem != wantIdem {
		t.Errorf("idempotency key = %q want %q", f.lastIdem, wantIdem)
	}
	if res.Output["ends_at"].Inline != "2026-01-01T00:00:00Z" {
		t.Errorf("ends_at = %v", res.Output["ends_at"].Inline)
	}

	// Immediate via the cancel_timing enum: a DELETE; ends_at is the
	// cancellation moment.
	res = run(t, f, "stripe_cancel_subscription",
		map[string]any{"subscription": "sub_1", "cancel_timing": "immediately"}, nil)
	if res.Status != core.StatusOK || res.Output["status"].Inline != "canceled" {
		t.Fatalf("immediate res = %+v", res)
	}
	if res.Output["ends_at"].Inline != "2025-06-15T15:06:40Z" {
		t.Errorf("immediate ends_at = %v", res.Output["ends_at"].Inline)
	}

	// Legacy boolean at_period_end:false from a pre-enum saved graph still
	// cancels immediately (backward compat with the old param).
	res = run(t, f, "stripe_cancel_subscription",
		map[string]any{"subscription": "sub_1", "at_period_end": false}, nil)
	if res.Status != core.StatusOK || res.Output["status"].Inline != "canceled" {
		t.Fatalf("legacy immediate res = %+v", res)
	}

	// Stripe's error message reaches the user.
	res = run(t, f, "stripe_cancel_subscription", map[string]any{"subscription": "sub_missing"}, nil)
	if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "No such subscription") {
		t.Errorf("missing sub res = %+v", res)
	}
	// No id at all → param error before any HTTP.
	res = run(t, f, "stripe_cancel_subscription", map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("no sub res = %+v", res)
	}
}

func TestSendInvoice(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_send_invoice",
		map[string]any{"customer": "cus_7", "amount": 12000, "currency": "USD", "description": "Consulting"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if res.Output["invoice_id"].Inline != "in_1" ||
		res.Output["hosted_invoice_url"].Inline != "https://invoice.stripe.com/i/test_in1" ||
		res.Output["status"].Inline != "open" {
		t.Errorf("outputs = %+v", res.Output)
	}

	// Wired customer + amount-as-text (the sheet-row case).
	res = run(t, f, "stripe_send_invoice", map[string]any{"currency": "sek"},
		map[string]core.Ref{"customer": {Inline: "cus_7"}, "amount": {Inline: "50000"}})
	if res.Status != core.StatusOK {
		t.Fatalf("wired res = %+v", res)
	}

	// Missing pieces → param errors before any HTTP.
	res = run(t, f, "stripe_send_invoice", map[string]any{"amount": 100}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("no customer res = %+v", res)
	}
	res = run(t, f, "stripe_send_invoice", map[string]any{"customer": "cus_7"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("no amount res = %+v", res)
	}
}

// TestSendInvoice_RetryReplaysSteps — the multi-call sequence's retry
// contract: when a run dies mid-sequence (finalize 500s) and the engine
// retries, every step must be re-sent with the SAME per-step
// Idempotency-Key as the first attempt, so Stripe replays the completed
// steps instead of creating a second invoice.
func TestSendInvoice_RetryReplaysSteps(t *testing.T) {
	type call struct {
		path string
		idem string
		form url.Values
	}
	var calls []call
	failFinalize := true
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		calls = append(calls, call{path: r.URL.Path, idem: r.Header.Get("Idempotency-Key"), form: r.PostForm})
		switch r.URL.Path {
		case "/invoices":
			fmt.Fprint(rw, `{"id":"in_9","status":"draft"}`)
		case "/invoiceitems":
			fmt.Fprint(rw, `{"id":"ii_9"}`)
		case "/invoices/in_9/finalize":
			if failFinalize {
				failFinalize = false
				rw.WriteHeader(500)
				fmt.Fprint(rw, `{"error":{"message":"server hiccup"}}`)
				return
			}
			fmt.Fprint(rw, `{"id":"in_9","status":"open","hosted_invoice_url":"https://invoice.stripe.com/i/test_in9"}`)
		case "/invoices/in_9/send":
			fmt.Fprint(rw, `{"id":"in_9","status":"open","hosted_invoice_url":"https://invoice.stripe.com/i/test_in9"}`)
		default:
			rw.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	drop, _ := engine.Default.Get("stripe_send_invoice")
	job := core.Job{ID: "job-retry", NodeID: "n", Params: map[string]any{
		"api_key": "sk_test_good", "base_url": srv.URL,
		"customer": "cus_9", "amount": 100,
	}}
	// First attempt dies at finalize.
	res, err := drop.Execute(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "server hiccup") {
		t.Fatalf("first attempt res = %+v", res)
	}
	firstCalls := len(calls)
	// The engine's retry: same job, same params.
	res, err = drop.Execute(context.Background(), job, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("retry res = %+v err=%v", res, err)
	}

	keysByPath := func(from, to int) map[string]string {
		m := map[string]string{}
		for _, c := range calls[from:to] {
			m[c.path] = c.idem
		}
		return m
	}
	first, second := keysByPath(0, firstCalls), keysByPath(firstCalls, len(calls))
	for _, path := range []string{"/invoices", "/invoiceitems", "/invoices/in_9/finalize"} {
		if first[path] == "" || first[path] != second[path] {
			t.Errorf("idempotency key for %s changed across retry: %q → %q", path, first[path], second[path])
		}
	}
	// And the steps must not share one key — that would make Stripe
	// replay step 1's response for step 2.
	seen := map[string]bool{}
	for path, k := range second {
		if seen[k] {
			t.Errorf("idempotency key %q reused across steps (%s)", k, path)
		}
		seen[k] = true
	}
}

func TestListEvents_CursorSemantics(t *testing.T) {
	f := newFakeStripe(t)

	// First poll, no cursor: newest events; last_id = the newest (evt_2).
	res := run(t, f, "stripe_list_events", map[string]any{"types": []any{"payment_intent.succeeded"}}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if res.Output["last_id"].Inline != "evt_2" {
		t.Errorf("last_id = %v, want evt_2 (newest)", res.Output["last_id"].Inline)
	}
	if f.lastForm.Get("types[0]") != "payment_intent.succeeded" {
		t.Errorf("types filter lost: %v", f.lastForm)
	}

	// Second poll with the cursor wired in: only newer events; cursor advances.
	res = run(t, f, "stripe_list_events", nil, map[string]core.Ref{"after_id": {Inline: "evt_2"}})
	events, _ := res.Output["events"].Inline.([]map[string]any)
	if len(events) != 1 || events[0]["id"] != "evt_3" {
		t.Errorf("events after cursor = %+v", res.Output["events"].Inline)
	}
	if res.Output["last_id"].Inline != "evt_3" {
		t.Errorf("advanced last_id = %v", res.Output["last_id"].Inline)
	}
	if f.lastForm.Get("ending_before") != "evt_2" {
		t.Errorf("cursor not sent as ending_before: %v", f.lastForm)
	}
}

func TestListEvents_EmptyEchoesCursor(t *testing.T) {
	// A quiet account must not clobber the saved cursor with "".
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, `{"data":[]}`)
	}))
	defer srv.Close()
	drop, _ := engine.Default.Get("stripe_list_events")
	res, err := drop.Execute(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"api_key": "sk_test_good", "base_url": srv.URL},
		Input: map[string]core.Ref{"after_id": {Inline: "evt_keep"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v / %v", res, err)
	}
	if res.Output["last_id"].Inline != "evt_keep" {
		t.Errorf("last_id = %v, want the incoming cursor echoed", res.Output["last_id"].Inline)
	}
}

func TestSearchCustomers(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_search_customers", nil,
		map[string]core.Ref{"query": {Inline: "email:'a@b.com'"}})
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	customers, _ := res.Output["customers"].Inline.([]map[string]any)
	if len(customers) != 1 || customers[0]["id"] != "cus_7" {
		t.Errorf("customers = %+v", res.Output["customers"].Inline)
	}
	if res.Output["count"].Inline != 1 {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
	// first_id/first_email carry the first match for the single-match wire.
	if res.Output["first_id"].Inline != "cus_7" {
		t.Errorf("first_id = %v", res.Output["first_id"].Inline)
	}
	if res.Output["first_email"].Inline != "a@b.com" {
		t.Errorf("first_email = %v", res.Output["first_email"].Inline)
	}
	if f.lastForm.Get("query") != "email:'a@b.com'" {
		t.Errorf("query = %v", f.lastForm.Get("query"))
	}

	res = run(t, f, "stripe_search_customers", map[string]any{"query": "email:'nobody@x.com'"}, nil)
	if res.Output["count"].Inline != 0 {
		t.Errorf("empty search count = %v", res.Output["count"].Inline)
	}
	// No matches → empty scalars, not an error.
	if res.Output["first_id"].Inline != "" || res.Output["first_email"].Inline != "" {
		t.Errorf("empty search first_id/email = %v / %v", res.Output["first_id"].Inline, res.Output["first_email"].Inline)
	}
}

func TestAuthErrors(t *testing.T) {
	f := newFakeStripe(t)

	// Wrong key: Stripe's message surfaces.
	res := run(t, f, "stripe_search_customers", map[string]any{"api_key": "sk_wrong", "query": "email:'a@b.com'"}, nil)
	if res.Status != core.StatusError || !strings.Contains(res.Error.Message, "Invalid API Key") {
		t.Errorf("wrong key res = %+v", res)
	}

	// Empty key (secret not set): clear setup pointer, no HTTP call.
	drop, _ := engine.Default.Get("stripe_create_customer")
	r2, err := drop.Execute(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"api_key": "", "email": "a@b.com"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if r2.Status != core.StatusError || !strings.Contains(r2.Error.Message, "STRIPE_API_KEY") {
		t.Errorf("empty key res = %+v", r2)
	}
}

func TestManifests(t *testing.T) {
	// All five registered, all carrying the Stripe branding + secret
	// requirement, so the catalog groups them and the connection gate fires.
	ids := []string{
		"stripe_create_customer", "stripe_create_payment_link",
		"stripe_create_refund", "stripe_list_events", "stripe_search_customers",
	}
	for _, id := range ids {
		mf, ok := engine.Default.Manifests()[id]
		if !ok {
			t.Errorf("%s not registered", id)
			continue
		}
		if mf.Integration != "Stripe" || mf.BrandLogo != "/brands/stripe.svg" {
			t.Errorf("%s: integration/brand = %q/%q", id, mf.Integration, mf.BrandLogo)
		}
		if len(mf.RequiresConnections) != 1 || mf.RequiresConnections[0].Name != "STRIPE_API_KEY" {
			t.Errorf("%s: RequiresConnections = %+v", id, mf.RequiresConnections)
		}
		var schema map[string]any
		if err := json.Unmarshal(mf.ParamsSchema, &schema); err != nil {
			t.Errorf("%s: params schema does not parse: %v", id, err)
		}
	}
}

// A placeholder cursor (the first-run value of the cursor secret, before
// any real event id has been saved) is ignored rather than sent to
// Stripe, and round-trips on idle polls.
func TestListEvents_PlaceholderCursorIgnored(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_list_events", nil, map[string]core.Ref{"after_id": {Inline: "-"}})
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("ending_before") != "" {
		t.Errorf("placeholder was sent to Stripe: %v", f.lastForm)
	}
	// Events existed, so the cursor advances past the placeholder.
	if res.Output["last_id"].Inline != "evt_2" {
		t.Errorf("last_id = %v", res.Output["last_id"].Inline)
	}
}

// resourceJob builds a job pointed at srv with a good key for the resource
// lister functions (ListSubscriptions / ListPaymentIntents / ListCustomers).
func resourceJob(url string) core.Job {
	return core.Job{Params: map[string]any{"api_key": "sk_test_good", "base_url": url}}
}

// --- pure helpers ----------------------------------------------------------

func TestSetHTTPBase_RoundTrip(t *testing.T) {
	orig := httpBase.Get()
	SetHTTPBase("https://example.test/v1")
	if httpBase.Get() != "https://example.test/v1" {
		t.Errorf("base = %q", httpBase.Get())
	}
	SetHTTPBase(orig)
}

func TestExtractStripeError_Forms(t *testing.T) {
	// message + code.
	if got := extractStripeError([]byte(`{"error":{"message":"No such customer","code":"resource_missing"}}`)); got != "resource_missing: No such customer" {
		t.Errorf("code+msg = %q", got)
	}
	// message only.
	if got := extractStripeError([]byte(`{"error":{"message":"boom"}}`)); got != "boom" {
		t.Errorf("msg only = %q", got)
	}
	// non-JSON body short.
	if got := extractStripeError([]byte("plain text")); got != "plain text" {
		t.Errorf("plain = %q", got)
	}
	// long non-JSON body is truncated to 200 bytes.
	long := strings.Repeat("x", 300)
	if got := extractStripeError([]byte(long)); len(got) != 200 {
		t.Errorf("truncated len = %d", len(got))
	}
}

func TestNumberInputOr_Edges(t *testing.T) {
	// Unwired → fallback.
	if n, ok := numberInputOr(core.Job{}, "amount", 5); !ok || n != 5 {
		t.Errorf("unwired = %d %v", n, ok)
	}
	mk := func(v any) core.Job {
		return core.Job{Input: map[string]core.Ref{"amount": {Inline: v}}}
	}
	// nil inline → fallback.
	if n, ok := numberInputOr(mk(nil), "amount", 9); !ok || n != 9 {
		t.Errorf("nil inline = %d %v", n, ok)
	}
	// whole float64.
	if n, ok := numberInputOr(mk(float64(12)), "amount", 0); !ok || n != 12 {
		t.Errorf("float = %d %v", n, ok)
	}
	// fractional float64 → not ok.
	if _, ok := numberInputOr(mk(float64(1.5)), "amount", 0); ok {
		t.Error("fractional should be !ok")
	}
	// int.
	if n, ok := numberInputOr(mk(7), "amount", 0); !ok || n != 7 {
		t.Errorf("int = %d %v", n, ok)
	}
	// numeric string.
	if n, ok := numberInputOr(mk("42"), "amount", 0); !ok || n != 42 {
		t.Errorf("string = %d %v", n, ok)
	}
	// blank string → fallback.
	if n, ok := numberInputOr(mk("  "), "amount", 3); !ok || n != 3 {
		t.Errorf("blank string = %d %v", n, ok)
	}
	// junk string → not ok.
	if _, ok := numberInputOr(mk("nope"), "amount", 0); ok {
		t.Error("junk string should be !ok")
	}
	// []byte numeric.
	if n, ok := numberInputOr(mk([]byte("8")), "amount", 0); !ok || n != 8 {
		t.Errorf("[]byte = %d %v", n, ok)
	}
	// unsupported type → not ok.
	if _, ok := numberInputOr(mk(true), "amount", 0); ok {
		t.Error("bool should be !ok")
	}
}

func TestFormatPriceAmount(t *testing.T) {
	if got := formatPriceAmount(4999, "usd"); got != "49.99 USD" {
		t.Errorf("usd = %q", got)
	}
	if got := formatPriceAmount(5000, "jpy"); got != "5000 JPY" {
		t.Errorf("jpy (zero-decimal) = %q", got)
	}
	if got := formatPriceAmount(0, "usd"); got != "" {
		t.Errorf("zero amount = %q", got)
	}
	if got := formatPriceAmount(100, ""); got != "" {
		t.Errorf("no currency = %q", got)
	}
}

func TestCurrencyEnumJSON(t *testing.T) {
	enum, names := currencyEnumJSON()
	var codes, labels []string
	if err := json.Unmarshal([]byte(enum), &codes); err != nil {
		t.Fatalf("enum json: %v", err)
	}
	if err := json.Unmarshal([]byte(names), &labels); err != nil {
		t.Fatalf("names json: %v", err)
	}
	if len(codes) != len(labels) || len(codes) == 0 {
		t.Fatalf("codes/labels len = %d/%d", len(codes), len(labels))
	}
	if codes[0] != "usd" || !strings.HasPrefix(labels[0], "USD — ") {
		t.Errorf("first = %q / %q", codes[0], labels[0])
	}
}

// --- triggers (standalone-run paths) ---------------------------------------

func TestTriggers_StandaloneRunHaveNoData(t *testing.T) {
	for _, id := range []string{
		"stripe_on_payment", "stripe_on_payment_failed", "stripe_on_subscription_canceled",
	} {
		drop, ok := engine.Default.Get(id)
		if !ok {
			t.Fatalf("%s not registered", id)
		}
		res, err := drop.Execute(context.Background(), core.Job{ID: "j"}, nil)
		if err != nil {
			t.Fatalf("%s execute: %v", id, err)
		}
		if res.Status != core.StatusError || res.Error.Code != "no_trigger_data" {
			t.Errorf("%s res = %+v", id, res)
		}
	}
}

func TestTriggerManifests_HaveWebhookSecret(t *testing.T) {
	for _, id := range []string{
		"stripe_on_payment", "stripe_on_payment_failed", "stripe_on_subscription_canceled",
	} {
		mf := engine.Default.Manifests()[id]
		if len(mf.RequiresConnections) != 1 || mf.RequiresConnections[0].Name != "STRIPE_WEBHOOK_SECRET" {
			t.Errorf("%s connections = %+v", id, mf.RequiresConnections)
		}
		if mf.Category != "trigger" {
			t.Errorf("%s category = %q", id, mf.Category)
		}
	}
	// The failed trigger prepends a 'failure_message' pin onto the shared set.
	failed := engine.Default.Manifests()["stripe_on_payment_failed"]
	if len(failed.Outputs) == 0 || failed.Outputs[0].Port != "failure_message" {
		t.Errorf("failed trigger outputs = %+v", failed.Outputs)
	}
}

// --- resource listers ------------------------------------------------------

func TestListSubscriptions_Resource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subscriptions" {
			rw.WriteHeader(404)
			return
		}
		fmt.Fprint(rw, `{"data":[
			{"id":"sub_1","status":"active","customer":{"name":"Ada","email":"a@x"},
			 "items":{"data":[{"price":{"unit_amount":4999,"currency":"usd","recurring":{"interval":"month"}}}]}},
			{"id":"sub_2","status":"trialing","customer":{"email":"b@y"},
			 "items":{"data":[{"price":{"unit_amount":0,"currency":"usd"}}]}},
			{"id":"sub_3"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	got, err := ListSubscriptions(context.Background(), resourceJob(srv.URL))
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	if got[0].ID != "sub_1" || got[0].Name != "Ada — 49.99 USD/month (active)" {
		t.Errorf("sub_1 = %+v", got[0])
	}
	if got[1].Name != "b@y (trialing)" {
		t.Errorf("sub_2 = %+v", got[1])
	}
	// No customer/price/status → falls back to the raw id.
	if got[2].Name != "sub_3" {
		t.Errorf("sub_3 fallback = %+v", got[2])
	}
}

func TestListSubscriptions_Resource_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(401)
		fmt.Fprint(rw, `{"error":{"message":"Invalid API Key"}}`)
	}))
	t.Cleanup(srv.Close)
	if _, err := ListSubscriptions(context.Background(), resourceJob(srv.URL)); err == nil {
		t.Error("401 should error")
	}
}

func TestListPaymentIntents_Resource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payment_intents" {
			rw.WriteHeader(404)
			return
		}
		fmt.Fprint(rw, `{"data":[
			{"id":"pi_1","status":"succeeded","amount":4999,"currency":"usd","description":"Order 1"},
			{"id":"pi_2","status":"succeeded","amount":2500,"currency":"usd","customer":{"email":"c@x"}},
			{"id":"pi_3","status":"succeeded","amount":1000,"currency":"usd","receipt_email":"r@x"},
			{"id":"pi_4","status":"requires_payment_method","amount":500,"currency":"usd"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	got, err := ListPaymentIntents(context.Background(), resourceJob(srv.URL))
	if err != nil {
		t.Fatalf("ListPaymentIntents: %v", err)
	}
	// pi_4 (not succeeded) is filtered out.
	if len(got) != 3 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	if got[0].Name != "49.99 USD — Order 1" {
		t.Errorf("pi_1 = %+v", got[0])
	}
	if got[1].Name != "25.00 USD — c@x" {
		t.Errorf("pi_2 = %+v", got[1])
	}
	if got[2].Name != "10.00 USD — r@x" {
		t.Errorf("pi_3 = %+v", got[2])
	}
}

func TestListPaymentIntents_Resource_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(500)
		fmt.Fprint(rw, `{"error":{"message":"server error"}}`)
	}))
	t.Cleanup(srv.Close)
	if _, err := ListPaymentIntents(context.Background(), resourceJob(srv.URL)); err == nil {
		t.Error("500 should error")
	}
}

func TestListCustomers_Resource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/customers" {
			rw.WriteHeader(404)
			return
		}
		fmt.Fprint(rw, `{"data":[
			{"id":"cus_1","name":"Jane Doe","email":"jane@x"},
			{"id":"cus_2","email":"only@x"},
			{"id":"cus_3","name":"NoEmail"},
			{"id":"cus_4"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	got, err := ListCustomers(context.Background(), resourceJob(srv.URL))
	if err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	want := map[string]string{
		"cus_1": "Jane Doe — jane@x",
		"cus_2": "only@x",
		"cus_3": "NoEmail",
		"cus_4": "cus_4",
	}
	if len(got) != 4 {
		t.Fatalf("got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if want[r.ID] != r.Name {
			t.Errorf("%s name = %q want %q", r.ID, r.Name, want[r.ID])
		}
	}
}

func TestListCustomers_Resource_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(403)
		fmt.Fprint(rw, `{"error":{"message":"forbidden"}}`)
	}))
	t.Cleanup(srv.Close)
	if _, err := ListCustomers(context.Background(), resourceJob(srv.URL)); err == nil {
		t.Error("403 should error")
	}
}

// ListPrices with a bare-string product (expansion unavailable) and a
// metered (unit_amount 0) price — exercises the product fallback + no-amount
// label paths the existing TestListPrices doesn't cover.
func TestListPrices_BareProductAndNoNickname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, `{"data":[
			{"id":"price_x","unit_amount":1000,"currency":"usd","product":"prod_bare"},
			{"id":"price_y","nickname":"Just nick","unit_amount":0,"currency":"usd"}
		]}`)
	}))
	t.Cleanup(srv.Close)

	got, err := ListPrices(context.Background(), resourceJob(srv.URL))
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	// product is a bare string → name falls back to the price id, then amount.
	if got[0].Name != "price_x — 10.00 USD" {
		t.Errorf("price_x = %+v", got[0])
	}
	// nickname only, zero amount → just the nickname.
	if got[1].Name != "Just nick" {
		t.Errorf("price_y = %+v", got[1])
	}
}

// --- execute-path error/decode branches ------------------------------------

func TestCreateCustomer_BadJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, `{"no":"id here"}`)
	}))
	t.Cleanup(srv.Close)
	drop, _ := engine.Default.Get("stripe_create_customer")
	res, _ := drop.Execute(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"api_key": "sk_test_good", "base_url": srv.URL, "email": "a@x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "stripe_error" {
		t.Errorf("res = %+v", res)
	}
}

func TestCreateCustomer_BadEmailInput(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_customer", map[string]any{},
		map[string]core.Ref{"email": {Inline: 42}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestCreateCustomer_WithDescriptionAndName(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_customer",
		map[string]any{"email": "a@x", "name": "Ada", "description": "VIP"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("name") != "Ada" || f.lastForm.Get("description") != "VIP" {
		t.Errorf("form = %v", f.lastForm)
	}
}

func TestSearchCustomers_BadQueryInput(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_search_customers", map[string]any{},
		map[string]core.Ref{"query": {Inline: 9}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestSearchCustomers_MissingQuery(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_search_customers", map[string]any{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("res = %+v", res)
	}
}

func TestListSubscriptions_Drop_BadCustomerInput(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_list_subscriptions", nil,
		map[string]core.Ref{"customer": {Inline: 7}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestListSubscriptions_Drop_LimitClamp(t *testing.T) {
	f := newFakeStripe(t)
	// limit above 100 clamps to 100; below 1 clamps to 1.
	run(t, f, "stripe_list_subscriptions", map[string]any{"customer": "cus_7", "limit": 500}, nil)
	if f.lastForm.Get("limit") != "100" {
		t.Errorf("clamp-high limit = %v", f.lastForm.Get("limit"))
	}
	run(t, f, "stripe_list_subscriptions", map[string]any{"customer": "cus_7", "limit": 0}, nil)
	if f.lastForm.Get("limit") != "1" {
		t.Errorf("clamp-low limit = %v", f.lastForm.Get("limit"))
	}
}

func TestSendInvoice_BadInputs(t *testing.T) {
	f := newFakeStripe(t)
	// Non-text customer input.
	res := run(t, f, "stripe_send_invoice", map[string]any{"amount": 100},
		map[string]core.Ref{"customer": {Inline: 1}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("bad customer res = %+v", res)
	}
	// Fractional amount input.
	res = run(t, f, "stripe_send_invoice", map[string]any{"customer": "cus_7"},
		map[string]core.Ref{"amount": {Inline: float64(1.5)}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("fractional amount res = %+v", res)
	}
	// Bad description input.
	res = run(t, f, "stripe_send_invoice", map[string]any{"customer": "cus_7", "amount": 100},
		map[string]core.Ref{"description": {Inline: 5}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("bad description res = %+v", res)
	}
}

func TestCancelSubscription_BadInput(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_cancel_subscription", nil,
		map[string]core.Ref{"subscription": {Inline: 3}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestCreatePaymentLink_BadQuantityType(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_ok"},
		map[string]core.Ref{"quantity": {Inline: true}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestListEvents_BadCursorInput(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_list_events", map[string]any{},
		map[string]core.Ref{"after_id": {Inline: 7}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestListEvents_LimitClampAndTypesSkipEmpty(t *testing.T) {
	f := newFakeStripe(t)
	// limit > 100 clamps; an empty/non-string type entry is skipped.
	run(t, f, "stripe_list_events", map[string]any{
		"limit": 999,
		"types": []any{"payment_intent.succeeded", "", 5},
	}, nil)
	if f.lastForm.Get("limit") != "100" {
		t.Errorf("clamp limit = %v", f.lastForm.Get("limit"))
	}
	if f.lastForm.Get("types[0]") != "payment_intent.succeeded" {
		t.Errorf("types[0] = %v", f.lastForm.Get("types[0]"))
	}
	if f.lastForm.Has("types[1]") {
		t.Errorf("empty type should be skipped: %v", f.lastForm)
	}
}

func TestPaymentLink_QuantityBounds(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_payment_link", map[string]any{"price": "price_ok"},
		map[string]core.Ref{"quantity": {Inline: float64(1000000)}})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("over-max quantity res = %+v", res)
	}
}

func TestPaymentLink_BadPriceInput(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_create_payment_link", map[string]any{},
		map[string]core.Ref{"price": {Inline: 1}})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("res = %+v", res)
	}
}

func TestPaymentLink_NoURLInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, `{"id":"plink_1"}`)
	}))
	t.Cleanup(srv.Close)
	drop, _ := engine.Default.Get("stripe_create_payment_link")
	res, _ := drop.Execute(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"api_key": "sk_test_good", "base_url": srv.URL, "price": "price_ok"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "stripe_error" {
		t.Errorf("res = %+v", res)
	}
}

func TestListEvents_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, `not json`)
	}))
	t.Cleanup(srv.Close)
	drop, _ := engine.Default.Get("stripe_list_events")
	res, _ := drop.Execute(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"api_key": "sk_test_good", "base_url": srv.URL},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "stripe_error" {
		t.Errorf("res = %+v", res)
	}
}

// timeout_ms <= 0 falls back to the default rather than dialing with a
// zero/negative deadline.
func TestStripeDo_NonPositiveTimeoutDefaults(t *testing.T) {
	f := newFakeStripe(t)
	res := run(t, f, "stripe_search_customers",
		map[string]any{"query": "email:'a@b.com'", "timeout_ms": 0}, nil)
	if res.Status != core.StatusOK {
		t.Errorf("res = %+v", res)
	}
}
