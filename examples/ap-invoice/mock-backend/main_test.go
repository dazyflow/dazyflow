// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckBearer(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"valid", "Bearer " + expectedInvoiceKey, true},
		{"valid-with-space", "Bearer  " + expectedInvoiceKey + " ", true},
		{"wrong-token", "Bearer nope", false},
		{"missing-prefix", expectedInvoiceKey, false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			if got := checkBearer(r, expectedInvoiceKey); got != c.want {
				t.Fatalf("checkBearer=%v want %v", got, c.want)
			}
		})
	}
}

func TestRefuse(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Authorization", "Bearer bad")
	refuse(w, "invoices", r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", w.Code)
	}
	if w.Body.String() != "unauthorized" {
		t.Fatalf("body=%q", w.Body.String())
	}
}

func TestRespondJSON(t *testing.T) {
	w := httptest.NewRecorder()
	respondJSON(w, 201, map[string]any{"k": "v"})
	if w.Code != 201 {
		t.Fatalf("code=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["k"] != "v" {
		t.Fatalf("body=%v", got)
	}
}

func TestReadBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("hello"))
	if b := readBody(r); b != "hello" {
		t.Fatalf("readBody=%q", b)
	}
	// empty body
	r2 := httptest.NewRequest(http.MethodPost, "/x", io.NopCloser(strings.NewReader("")))
	if b := readBody(r2); b != "" {
		t.Fatalf("readBody empty=%q", b)
	}
}
