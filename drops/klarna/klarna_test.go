// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package klarna

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeKlarna stands in for the Klarna Order Management API: it checks the Basic
// auth header and serves the order / capture / refund endpoints, recording the
// last request body and auth so tests can assert on them.
type fakeKlarna struct {
	srv      *httptest.Server
	lastAuth string
	lastBody map[string]any
	lastPath string
	// order the GET endpoint returns.
	order string
	// captureStatus/refundStatus let a test force a non-2xx.
	captureStatus int
	refundStatus  int
}

func newFakeKlarna(t *testing.T) *fakeKlarna {
	t.Helper()
	f := &fakeKlarna{
		order:         `{"order_id":"o1","status":"ORDER_OPEN","order_amount":5000,"captured_amount":1000,"refunded_amount":200,"remaining_authorized_amount":4000,"purchase_currency":"SEK"}`,
		captureStatus: 201,
		refundStatus:  201,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		f.lastPath = r.URL.Path
		if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("u1:p1")) {
			rw.WriteHeader(401)
			_, _ = io.WriteString(rw, `{"error_code":"UNAUTHORIZED","error_messages":["Bad credentials."]}`)
			return
		}
		f.lastBody = nil
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &f.lastBody)
			}
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/ordermanagement/v1/orders/o1":
			_, _ = io.WriteString(rw, f.order)
		case r.Method == "GET" && r.URL.Path == "/ordermanagement/v1/orders/missing":
			rw.WriteHeader(404)
			_, _ = io.WriteString(rw, `{"error_code":"NO_SUCH_ORDER","error_messages":["Order not found."]}`)
		case r.Method == "POST" && r.URL.Path == "/ordermanagement/v1/orders/o1/captures":
			if f.captureStatus != 201 {
				rw.WriteHeader(f.captureStatus)
				_, _ = io.WriteString(rw, `{"error_code":"CAPTURE_NOT_ALLOWED","error_messages":["Amount too high."]}`)
				return
			}
			rw.Header().Set("Capture-ID", "cap1")
			rw.Header().Set("Location", "/ordermanagement/v1/orders/o1/captures/cap1")
			rw.WriteHeader(201)
		case r.Method == "POST" && r.URL.Path == "/ordermanagement/v1/orders/o1/refunds":
			if f.refundStatus != 201 {
				rw.WriteHeader(f.refundStatus)
				_, _ = io.WriteString(rw, `{"error_code":"REFUND_NOT_ALLOWED","error_messages":["Amount too high."]}`)
				return
			}
			// Exercise the Location-only fallback (no Refund-ID header).
			rw.Header().Set("Location", "/ordermanagement/v1/orders/o1/refunds/ref1")
			rw.WriteHeader(201)
		default:
			rw.WriteHeader(404)
			_, _ = io.WriteString(rw, `{"error_code":"NOT_FOUND","error_messages":["No route."]}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKlarna) job(extra map[string]any) core.Job {
	p := map[string]any{
		"api_username": "u1", "api_password": "p1", "base_url": f.srv.URL,
	}
	for k, v := range extra {
		p[k] = v
	}
	return core.Job{ID: "j1", Params: p}
}

func TestGetOrder_OK(t *testing.T) {
	f := newFakeKlarna(t)
	res, err := executeGetOrder(context.Background(), f.job(map[string]any{"order_id": "o1"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["status"].Inline; got != "ORDER_OPEN" {
		t.Errorf("status = %v, want ORDER_OPEN", got)
	}
	if got := res.Output["remaining_authorized_amount"].Inline; got != "4000" {
		t.Errorf("remaining = %v, want 4000", got)
	}
	if got := res.Output["currency"].Inline; got != "SEK" {
		t.Errorf("currency = %v, want SEK", got)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u1:p1"))
	if f.lastAuth != wantAuth {
		t.Errorf("auth = %q, want %q", f.lastAuth, wantAuth)
	}
}

func TestGetOrder_RequiresOrderID(t *testing.T) {
	f := newFakeKlarna(t)
	res, _ := executeGetOrder(context.Background(), f.job(nil), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without an order id")
	}
}

func TestGetOrder_SurfacesKlarnaError(t *testing.T) {
	f := newFakeKlarna(t)
	res, _ := executeGetOrder(context.Background(), f.job(map[string]any{"order_id": "missing"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 404")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "Order not found") {
		t.Errorf("error = %+v, want the Klarna reason surfaced", res.Error)
	}
}

func TestCapture_PartialAmount(t *testing.T) {
	f := newFakeKlarna(t)
	res, err := executeCaptureOrder(context.Background(),
		f.job(map[string]any{"order_id": "o1", "amount": 2500, "description": "First shipment"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["capture_id"].Inline; got != "cap1" {
		t.Errorf("capture_id = %v, want cap1", got)
	}
	if got := res.Output["captured_amount"].Inline; got != "2500" {
		t.Errorf("captured_amount = %v, want 2500", got)
	}
	// A partial capture must not GET the order first — it should POST straight to
	// /captures with the given amount.
	if f.lastPath != "/ordermanagement/v1/orders/o1/captures" {
		t.Errorf("lastPath = %q, want the captures endpoint", f.lastPath)
	}
	if amt, _ := f.lastBody["captured_amount"].(float64); amt != 2500 {
		t.Errorf("captured_amount body = %v, want 2500", f.lastBody["captured_amount"])
	}
}

func TestCapture_FullUsesRemainingAuthorized(t *testing.T) {
	f := newFakeKlarna(t)
	res, err := executeCaptureOrder(context.Background(),
		f.job(map[string]any{"order_id": "o1"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	// Full capture reads remaining_authorized_amount (4000) and captures it.
	if got := res.Output["captured_amount"].Inline; got != "4000" {
		t.Errorf("captured_amount = %v, want 4000 (remaining authorized)", got)
	}
	if amt, _ := f.lastBody["captured_amount"].(float64); amt != 4000 {
		t.Errorf("captured_amount body = %v, want 4000", f.lastBody["captured_amount"])
	}
}

func TestCapture_FullNothingLeft(t *testing.T) {
	f := newFakeKlarna(t)
	f.order = `{"order_id":"o1","status":"CAPTURED","order_amount":5000,"captured_amount":5000,"remaining_authorized_amount":0,"purchase_currency":"SEK"}`
	res, _ := executeCaptureOrder(context.Background(), f.job(map[string]any{"order_id": "o1"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result when nothing is left to capture")
	}
}

func TestCapture_SurfacesKlarnaError(t *testing.T) {
	f := newFakeKlarna(t)
	f.captureStatus = 400
	res, _ := executeCaptureOrder(context.Background(),
		f.job(map[string]any{"order_id": "o1", "amount": 999999}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 400")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "Amount too high") {
		t.Errorf("error = %+v, want the Klarna reason surfaced", res.Error)
	}
}

func TestRefund_PartialAmount_LocationFallback(t *testing.T) {
	f := newFakeKlarna(t)
	res, err := executeRefundOrder(context.Background(),
		f.job(map[string]any{"order_id": "o1", "amount": 500, "description": "Goodwill"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	// The refund handler sends only a Location header — the id must fall back to
	// its last path segment.
	if got := res.Output["refund_id"].Inline; got != "ref1" {
		t.Errorf("refund_id = %v, want ref1 (from Location)", got)
	}
	if amt, _ := f.lastBody["refunded_amount"].(float64); amt != 500 {
		t.Errorf("refunded_amount body = %v, want 500", f.lastBody["refunded_amount"])
	}
}

func TestRefund_FullUsesRemainingRefundable(t *testing.T) {
	f := newFakeKlarna(t)
	res, err := executeRefundOrder(context.Background(),
		f.job(map[string]any{"order_id": "o1"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	// captured 1000 − refunded 200 = 800 refundable.
	if got := res.Output["refunded_amount"].Inline; got != "800" {
		t.Errorf("refunded_amount = %v, want 800", got)
	}
}

func TestRefund_MissingCreds(t *testing.T) {
	f := newFakeKlarna(t)
	job := core.Job{ID: "j1", Params: map[string]any{"base_url": f.srv.URL, "order_id": "o1", "amount": 500}}
	res, _ := executeRefundOrder(context.Background(), job, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without credentials")
	}
}

func TestAmount_RejectsFractionalInput(t *testing.T) {
	f := newFakeKlarna(t)
	job := f.job(map[string]any{"order_id": "o1"})
	job.Input = map[string]core.Ref{"amount": {MIME: "application/json", Inline: 12.5}}
	res, _ := executeCaptureOrder(context.Background(), job, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result for a fractional amount")
	}
}

func TestRegionBase(t *testing.T) {
	cases := map[string]string{
		"eu":            "https://api.klarna.com",
		"na-playground": "https://api-na.playground.klarna.com",
		"":              "https://api.playground.klarna.com", // unset → safe sandbox
		"bogus":         "https://api.playground.klarna.com", // unknown → safe sandbox
	}
	for region, want := range cases {
		if got := regionBase(region); got != want {
			t.Errorf("regionBase(%q) = %q, want %q", region, got, want)
		}
	}
}

func TestBaseURL_OverrideWinsOverRegion(t *testing.T) {
	job := core.Job{Params: map[string]any{"region": "eu", "base_url": "http://localhost:1/"}}
	if got := baseURL(job); got != "http://localhost:1" {
		t.Errorf("baseURL = %q, want the trimmed override", got)
	}
}
