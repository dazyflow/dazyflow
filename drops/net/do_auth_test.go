// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoBearer_HeadersAndContentType(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	extra := map[string]string{"Accept": "application/json"}
	if _, _, _, err := DoBearer(context.Background(), "GET", srv.URL, "tok", "application/json", extra, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer tok" || got.Get("Accept") != "application/json" {
		t.Fatalf("headers = %v", got)
	}
	if got.Get("Content-Type") != "" {
		t.Fatalf("Content-Type set without a body: %q", got.Get("Content-Type"))
	}
	if len(extra) != 1 {
		t.Fatalf("extra was mutated: %v", extra)
	}

	if _, _, _, err := DoBearer(context.Background(), "POST", srv.URL, "tok", "application/json", nil, []byte("{}"), 0, 0); err != nil {
		t.Fatal(err)
	}
	if got.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", got.Get("Content-Type"))
	}
}

func TestBasicAuth(t *testing.T) {
	if got, want := BasicAuth("user", "pass"), "Basic dXNlcjpwYXNz"; got != want {
		t.Fatalf("BasicAuth = %q, want %q", got, want)
	}
}
