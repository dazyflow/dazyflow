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

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
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
	if f.lastForm.Get("query") != "email:'a@b.com'" {
		t.Errorf("query = %v", f.lastForm.Get("query"))
	}

	res = run(t, f, "stripe_search_customers", map[string]any{"query": "email:'nobody@x.com'"}, nil)
	if res.Output["count"].Inline != 0 {
		t.Errorf("empty search count = %v", res.Output["count"].Inline)
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
