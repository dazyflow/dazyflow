// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llmtask

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
)

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
	if err := v(context.Background(), map[string]string{}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("verifier with no key = %v, want API-key guidance", err)
	}
	if err := v(context.Background(), map[string]string{"api_key": "sk-123"}); err != nil {
		t.Errorf("verifier with key: %v", err)
	}
	if verifiedKey != "sk-123" {
		t.Errorf("VerifyKey got %q, want sk-123", verifiedKey)
	}
}

func TestVerifyGET(t *testing.T) {
	extract := func(b []byte) string { return "detail:" + string(b) }
	for _, tc := range []struct {
		status int
		want   string
	}{
		{200, ""},
		{401, "Prov rejected the API key"},
		{400, "Prov rejected the API key"},
		{500, "Prov returned HTTP 500: detail:boom"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("boom"))
		}))
		body, err := VerifyGET(context.Background(), "Prov", srv.URL, nil, []int{400, 401, 403}, extract)
		srv.Close()
		if tc.want == "" {
			if err != nil || string(body) != "boom" {
				t.Errorf("status %d: body=%q err=%v", tc.status, body, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.want {
			t.Errorf("status %d: err = %v, want %q", tc.status, err, tc.want)
		}
		if (tc.status == 401 || tc.status == 400) && !errors.Is(err, ErrKeyRejected) {
			t.Errorf("status %d: err does not wrap ErrKeyRejected", tc.status)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u := srv.URL
	srv.Close()
	if _, err := VerifyGET(context.Background(), "Prov", u, nil, nil, extract); err == nil || !strings.HasPrefix(err.Error(), "could not reach Prov: ") {
		t.Errorf("unreachable: err = %v", err)
	}
}

func TestBaseOrAndMediaType(t *testing.T) {
	if got := BaseOr("  http://x/ ", "d"); got != "http://x" {
		t.Errorf("BaseOr = %q", got)
	}
	if got := BaseOr(" / ", "d"); got != "d" {
		t.Errorf("BaseOr empty = %q", got)
	}
	if got := MediaType("image/png; charset=binary"); got != "image/png" {
		t.Errorf("MediaType = %q", got)
	}
}
