// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llmtask

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Tests hit httptest servers on loopback; the SSRF guard blocks loopback
// unless the operator opt-in is set, so enable it for the suite.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestPostJSON_HappyPath(t *testing.T) {
	var gotHdr, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header.Get("X-Test")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	status, body, jerr := PostJSON(context.Background(), srv.URL, map[string]string{"X-Test": "v"}, []byte(`{"q":1}`), 5000)
	if jerr != nil {
		t.Fatalf("PostJSON: %+v", jerr)
	}
	if status != 200 || string(body) != `{"ok":true}` {
		t.Errorf("status=%d body=%s", status, body)
	}
	if gotHdr != "v" || gotBody != `{"q":1}` {
		t.Errorf("header=%q body=%q", gotHdr, gotBody)
	}
}

func TestPostJSON_BadEndpoint(t *testing.T) {
	// A control char in the URL makes http.NewRequestWithContext fail.
	_, _, jerr := PostJSON(context.Background(), "http://\x00bad", nil, []byte(`{}`), 1000)
	if jerr == nil || jerr.Code != "bad_param" {
		t.Fatalf("PostJSON bad endpoint = %+v, want bad_param", jerr)
	}
}

func TestPostJSON_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	// timeoutMS small → the request deadline fires → llm_timeout.
	_, _, jerr := PostJSON(context.Background(), srv.URL, nil, []byte(`{}`), 100)
	if jerr == nil || jerr.Code != "llm_timeout" {
		t.Fatalf("PostJSON timeout = %+v, want llm_timeout", jerr)
	}
}

func TestGetStatus_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	status, body, err := GetStatus(context.Background(), srv.URL, map[string]string{"Authorization": "Bearer k"})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status != 200 || string(body) != `{"data":[]}` {
		t.Errorf("status=%d body=%s", status, body)
	}
}

func TestGetStatus_BadEndpoint(t *testing.T) {
	if _, _, err := GetStatus(context.Background(), "http://\x00bad", nil); err == nil {
		t.Fatal("GetStatus bad endpoint: want error, got nil")
	}
}

func TestRegisterAll_RegistersDropsAndVerifier(t *testing.T) {
	var verifiedKey string
	cfg := Config{
		Provider:     &fakeProvider{text: "x"},
		Integration:  "CovTest",
		DefaultModel: "m1",
		Models:       []ModelOption{{ID: "m1", Label: "M1"}},
		AskID:        "covtest_ask",
		TaskIDPrefix: "covtest",
		VerifyKey: func(_ context.Context, apiKey, _ string) error {
			verifiedKey = apiKey
			return nil
		},
	}
	// Registers 5 drops + the connection verifier; must not panic.
	RegisterAll(cfg)

	v, ok := engine.ConnectionVerifierFor(core.ConnectionSlug("CovTest"))
	if !ok || v == nil {
		t.Fatal("RegisterAll did not register a connection verifier for CovTest")
	}
	// Empty key is rejected with guidance.
	if err := v(context.Background(), map[string]string{}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("verifier with no key = %v, want API-key guidance", err)
	}
	// A present key flows through to the provider's VerifyKey.
	if err := v(context.Background(), map[string]string{"api_key": "sk-123"}); err != nil {
		t.Errorf("verifier with key: %v", err)
	}
	if verifiedKey != "sk-123" {
		t.Errorf("VerifyKey got %q, want sk-123", verifiedKey)
	}
}
