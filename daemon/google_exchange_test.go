// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/auth"
)

// withGoogleEndpoints points the package-level token/userinfo endpoints at
// test servers for the duration of a test, restoring them afterward.
func withGoogleEndpoints(t *testing.T, tokenURL, userinfoURL string) {
	t.Helper()
	ot, ou := googleTokenURL, googleUserinfoURL
	googleTokenURL, googleUserinfoURL = tokenURL, userinfoURL
	t.Cleanup(func() { googleTokenURL, googleUserinfoURL = ot, ou })
}

func TestExchangeGoogleCode_HappyPath(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "auth-code" || r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("unexpected token form: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-1","id_token":"idt-1","token_type":"Bearer"}`))
	}))
	defer tokenSrv.Close()
	uiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" {
			t.Errorf("userinfo auth = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"email":"ada@example.com","email_verified":true,"name":"Ada","sub":"u123","hd":"example.com"}`))
	}))
	defer uiSrv.Close()
	withGoogleEndpoints(t, tokenSrv.URL, uiSrv.URL)

	cfg := auth.OrgAuthConfig{GoogleClientID: "cid", GoogleClientSecret: "secret"}
	at, idt, info, err := exchangeGoogleCode(context.Background(), cfg, "auth-code", "https://app/cb")
	if err != nil {
		t.Fatalf("exchangeGoogleCode: %v", err)
	}
	if at != "at-1" || idt != "idt-1" {
		t.Errorf("tokens = %q / %q", at, idt)
	}
	if info.Email != "ada@example.com" || !info.EmailVerified || info.Sub != "u123" {
		t.Errorf("info = %+v", info)
	}
}

func TestExchangeGoogleCode_TokenNon2xx(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokenSrv.Close()
	withGoogleEndpoints(t, tokenSrv.URL, "http://unused.invalid")

	_, _, _, err := exchangeGoogleCode(context.Background(), auth.OrgAuthConfig{}, "c", "r")
	if err == nil || !strings.Contains(err.Error(), "token exchange 400") {
		t.Fatalf("err = %v, want token-exchange status error", err)
	}
}

func TestExchangeGoogleCode_MissingAccessToken(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id_token":"x"}`))
	}))
	defer tokenSrv.Close()
	withGoogleEndpoints(t, tokenSrv.URL, "http://unused.invalid")

	_, _, _, err := exchangeGoogleCode(context.Background(), auth.OrgAuthConfig{}, "c", "r")
	if err == nil || !strings.Contains(err.Error(), "no access_token") {
		t.Fatalf("err = %v, want no-access-token error", err)
	}
}

func TestExchangeGoogleCode_BadTokenJSON(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer tokenSrv.Close()
	withGoogleEndpoints(t, tokenSrv.URL, "http://unused.invalid")

	_, _, _, err := exchangeGoogleCode(context.Background(), auth.OrgAuthConfig{}, "c", "r")
	if err == nil || !strings.Contains(err.Error(), "parse token response") {
		t.Fatalf("err = %v, want parse-token error", err)
	}
}

func TestExchangeGoogleCode_UserinfoNon2xx(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","id_token":"idt"}`))
	}))
	defer tokenSrv.Close()
	uiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("nope"))
	}))
	defer uiSrv.Close()
	withGoogleEndpoints(t, tokenSrv.URL, uiSrv.URL)

	_, _, _, err := exchangeGoogleCode(context.Background(), auth.OrgAuthConfig{}, "c", "r")
	if err == nil || !strings.Contains(err.Error(), "userinfo 401") {
		t.Fatalf("err = %v, want userinfo status error", err)
	}
}

func TestExchangeGoogleCode_BadUserinfoJSON(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"at","id_token":"idt"}`))
	}))
	defer tokenSrv.Close()
	uiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{bad`))
	}))
	defer uiSrv.Close()
	withGoogleEndpoints(t, tokenSrv.URL, uiSrv.URL)

	_, _, _, err := exchangeGoogleCode(context.Background(), auth.OrgAuthConfig{}, "c", "r")
	if err == nil || !strings.Contains(err.Error(), "parse userinfo") {
		t.Fatalf("err = %v, want parse-userinfo error", err)
	}
}

func TestExchangeGoogleCode_TokenTransportError(t *testing.T) {
	// Point at a closed port so the token POST fails at transport.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()
	withGoogleEndpoints(t, addr, "http://unused.invalid")

	_, _, _, err := exchangeGoogleCode(context.Background(), auth.OrgAuthConfig{}, "c", "r")
	if err == nil || !strings.Contains(err.Error(), "token request") {
		t.Fatalf("err = %v, want token transport error", err)
	}
}
