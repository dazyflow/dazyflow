// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package fortnox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeFortnox is a stand-in Fortnox API. Tests register handlers per path and
// read back the last request body to assert the envelope shape.
type fakeFortnox struct {
	server   *httptest.Server
	lastBody []byte
	lastAuth string
}

func newFakeFortnox(t *testing.T, handler http.HandlerFunc) *fakeFortnox {
	t.Helper()
	f := &fakeFortnox{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastBody, _ = io.ReadAll(r.Body)
		f.lastAuth = r.Header.Get("Authorization")
		handler(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// job builds a job pointed at the fake server with an injected token (the
// `token` param wins in the oauthtok resolve sequence, so no daemon lookup is
// needed in unit tests).
func (f *fakeFortnox) job(params map[string]any) core.Job {
	p := map[string]any{"token": "test-token", "base_url": f.server.URL}
	for k, v := range params {
		p[k] = v
	}
	return core.Job{ID: "j1", Params: p}
}

func TestCreateCustomer_OK(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/customers" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"Customer":{"CustomerNumber":"42","Name":"Acme AB","Email":"faktura@acme.se"}}`))
	})

	res, err := executeCreateCustomer(context.Background(),
		f.job(map[string]any{"name": "Acme AB", "email": "faktura@acme.se"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["customer_number"].Inline; got != "42" {
		t.Errorf("customer_number = %v, want 42", got)
	}
	if f.lastAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", f.lastAuth)
	}
	// Request body must use Fortnox's singular "Customer" envelope.
	var sent struct {
		Customer struct{ Name, Email string }
	}
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, f.lastBody)
	}
	if sent.Customer.Name != "Acme AB" || sent.Customer.Email != "faktura@acme.se" {
		t.Errorf("sent envelope = %+v", sent)
	}
}

func TestCreateCustomer_RequiresName(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not call the API without a name")
	})
	res, _ := executeCreateCustomer(context.Background(), f.job(nil), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result for a missing name")
	}
}

func TestCreateCustomer_SurfacesFortnoxError(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ErrorInformation":{"Message":"Kunden kunde inte hittas","Code":2000434}}`))
	})
	res, _ := executeCreateCustomer(context.Background(),
		f.job(map[string]any{"name": "Acme AB"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 400")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "Kunden kunde inte hittas") {
		t.Errorf("error = %+v, want the Fortnox message surfaced", res.Error)
	}
}

func TestCreateInvoice_OK(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/invoices" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"Invoice":{"DocumentNumber":"1001","CustomerNumber":"42","Total":3000}}`))
	})
	res, err := executeCreateInvoice(context.Background(), f.job(map[string]any{
		"customer_number": "42",
		"rows": []any{map[string]any{
			"Description": "Consulting", "Price": 1500, "DeliveredQuantity": "2",
		}},
	}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["document_number"].Inline; got != "1001" {
		t.Errorf("document_number = %v, want 1001", got)
	}
	// Body must carry the CustomerNumber + InvoiceRows inside the Invoice envelope.
	var sent struct {
		Invoice struct {
			CustomerNumber string
			InvoiceRows    []map[string]any
		}
	}
	if err := json.Unmarshal(f.lastBody, &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if sent.Invoice.CustomerNumber != "42" || len(sent.Invoice.InvoiceRows) != 1 {
		t.Errorf("sent envelope = %+v", sent)
	}
}

func TestCreateInvoice_RequiresRows(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not call the API without rows")
	})
	res, _ := executeCreateInvoice(context.Background(),
		f.job(map[string]any{"customer_number": "42"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result for missing rows")
	}
}

func TestListInvoices_OK(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("filter"); got != "fullypaid" {
			t.Errorf("filter = %q, want fullypaid", got)
		}
		_, _ = w.Write([]byte(`{"Invoices":[{"DocumentNumber":"1001"},{"DocumentNumber":"1000"}],"MetaInformation":{"@CurrentPage":1,"@TotalPages":3}}`))
	})
	res, err := executeListInvoices(context.Background(),
		f.job(map[string]any{"filter": "fullypaid"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	invs, ok := res.Output["invoices"].Inline.([]map[string]any)
	if !ok || len(invs) != 2 {
		t.Fatalf("invoices = %v", res.Output["invoices"].Inline)
	}
	if got := res.Output["has_more"].Inline; got != "true" {
		t.Errorf("has_more = %v, want true (page 1 of 3)", got)
	}
}

func TestListCustomers_Picker(t *testing.T) {
	f := newFakeFortnox(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Customers":[{"CustomerNumber":"42","Name":"Acme AB","Email":"x@acme.se"},{"CustomerNumber":"43","Name":""}]}`))
	})
	got, err := ListCustomers(context.Background(), f.job(nil))
	if err != nil {
		t.Fatalf("ListCustomers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d options, want 2", len(got))
	}
	if got[0].ID != "42" || got[0].Name != "Acme AB — 42" {
		t.Errorf("option[0] = %+v, want {42, Acme AB — 42}", got[0])
	}
	// An unnamed customer falls back to the bare number for its label.
	if got[1].Name != "43" {
		t.Errorf("option[1].Name = %q, want 43", got[1].Name)
	}
}
