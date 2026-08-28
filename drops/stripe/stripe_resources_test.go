// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestListPrices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk_test_good" {
			rw.WriteHeader(401)
			fmt.Fprint(rw, `{"error":{"message":"Invalid API Key provided","code":"invalid_api_key"}}`)
			return
		}
		if r.URL.Path != "/prices" {
			rw.WriteHeader(404)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("active") != "true" || r.Form.Get("expand[]") != "data.product" {
			t.Errorf("unexpected query: %v", r.Form)
		}
		fmt.Fprint(rw, `{"data":[
			{"id":"price_1","unit_amount":4999,"currency":"usd",
			 "recurring":{"interval":"month"},
			 "product":{"id":"prod_1","name":"Pro plan"}},
			{"id":"price_2","nickname":"Launch deal","unit_amount":5000,"currency":"jpy",
			 "product":{"id":"prod_2","name":"Sticker"}},
			{"id":"price_3","unit_amount":0,"currency":"usd",
			 "product":{"id":"prod_3","name":"Metered API"}}
		]}`)
	}))
	t.Cleanup(srv.Close)

	job := core.Job{Params: map[string]any{"api_key": "sk_test_good", "base_url": srv.URL}}
	got, err := ListPrices(context.Background(), job)
	if err != nil {
		t.Fatalf("ListPrices: %v", err)
	}
	want := []core.AccountResource{
		{ID: "price_1", Name: "Pro plan — 49.99 USD/month"},
		{ID: "price_2", Name: "Sticker / Launch deal — 5000 JPY"},
		{ID: "price_3", Name: "Metered API"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d prices, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("price[%d] = %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestListPrices_BadKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(401)
		fmt.Fprint(rw, `{"error":{"message":"Invalid API Key provided","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(srv.Close)

	job := core.Job{Params: map[string]any{"api_key": "sk_bad", "base_url": srv.URL}}
	_, err := ListPrices(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("want invalid-key error, got %v", err)
	}
}
