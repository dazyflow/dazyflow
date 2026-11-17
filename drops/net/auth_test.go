// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func captureHeaders(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestBasicAuth(t *testing.T) {
	if got, want := BasicAuth("Aladdin", "open sesame"), "Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ=="; got != want {
		t.Fatalf("BasicAuth = %q, want %q", got, want)
	}
}

func TestDoBearer_HeadersWithBody(t *testing.T) {
	srv, got := captureHeaders(t)
	status, _, _, err := DoBearer(context.Background(), "POST", srv.URL, "tok", "application/json",
		map[string]string{"Accept": "application/json"}, []byte("{}"), 0, 0)
	if err != nil || status != 200 {
		t.Fatalf("status=%d err=%v", status, err)
	}
	h := *got
	if h.Get("Authorization") != "Bearer tok" || h.Get("Content-Type") != "application/json" || h.Get("Accept") != "application/json" {
		t.Fatalf("unexpected headers: %v", h)
	}
}

func TestDoBearer_NoContentTypeWithoutBody(t *testing.T) {
	srv, got := captureHeaders(t)
	if _, _, _, err := DoBearer(context.Background(), "GET", srv.URL, "tok", "application/json", nil, nil, 0, 0); err != nil {
		t.Fatal(err)
	}
	if ct := (*got).Get("Content-Type"); ct != "" {
		t.Fatalf("Content-Type = %q on a bodyless request", ct)
	}
}

func TestDoBasic_FormBody(t *testing.T) {
	srv, got := captureHeaders(t)
	if _, _, _, err := DoBasic(context.Background(), "POST", srv.URL, "u", "p", "application/x-www-form-urlencoded", nil, FormBody("a=1"), 0, 0); err != nil {
		t.Fatal(err)
	}
	h := *got
	if h.Get("Authorization") != BasicAuth("u", "p") || h.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("unexpected headers: %v", h)
	}
	if FormBody("") != nil {
		t.Fatal("FormBody(\"\") should be nil")
	}
}
