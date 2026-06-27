// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

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
