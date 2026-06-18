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
