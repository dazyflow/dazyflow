// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// Tests hit httptest servers on loopback; the shared SSRF guard blocks
// loopback unless the operator opt-in is set, so enable it for the suite.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func job(params map[string]any) core.Job { return core.Job{Params: params} }

func TestResolveToken_ExplicitToken(t *testing.T) {
	tok, err := ResolveToken(context.Background(), job(map[string]any{"token": "ya29.x"}))
	if err != nil || tok != "ya29.x" {
		t.Fatalf("ResolveToken = %q, %v", tok, err)
	}
}

func TestResolveToken_NoLookup(t *testing.T) {
	SetTokenLookup(nil)
	_, err := ResolveToken(context.Background(), job(nil))
	if err == nil || !strings.Contains(err.Error(), "no Google token") {
		t.Fatalf("ResolveToken = %v, want no-token guidance", err)
	}
}

func TestResolveToken_DefaultAccount(t *testing.T) {
	var got string
	SetTokenLookup(func(_ context.Context, account string) (string, error) {
		got = account
		return "tok", nil
	})
	t.Cleanup(func() { SetTokenLookup(nil) })
	tok, err := ResolveToken(context.Background(), job(nil))
	if err != nil || tok != "tok" {
		t.Fatalf("ResolveToken = %q, %v", tok, err)
	}
	if got != "default" {
		t.Errorf("account = %q, want default", got)
	}
}

func TestResolveToken_NamedAccount(t *testing.T) {
	var got string
	SetTokenLookup(func(_ context.Context, account string) (string, error) {
		got = account
		return "tok", nil
	})
	t.Cleanup(func() { SetTokenLookup(nil) })
	if _, err := ResolveToken(context.Background(), job(map[string]any{"account": "work"})); err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if got != "work" {
		t.Errorf("account = %q, want work", got)
	}
}

func TestResolveToken_LookupError(t *testing.T) {
	SetTokenLookup(func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	})
	t.Cleanup(func() { SetTokenLookup(nil) })
	_, err := ResolveToken(context.Background(), job(nil))
	if err == nil || !strings.Contains(err.Error(), "lookup token for account") {
		t.Fatalf("ResolveToken = %v, want wrapped lookup error", err)
	}
}

func TestResolveToken_NotConnected(t *testing.T) {
	SetTokenLookup(func(_ context.Context, _ string) (string, error) { return "", nil })
	t.Cleanup(func() { SetTokenLookup(nil) })
	_, err := ResolveToken(context.Background(), job(map[string]any{"account": "ada"}))
	if err == nil || !strings.Contains(err.Error(), `google account "ada" is not connected`) {
		t.Fatalf("ResolveToken = %v, want not-connected error", err)
	}
}

func TestDo_HappyPathAndHeaders(t *testing.T) {
	var gotAuth, gotCT, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	status, raw, err := Do(context.Background(), "POST", srv.URL, "tok-123", "application/json", []byte(`{"x":1}`), 5000, 1<<20)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != 200 || string(raw) != `{"ok":true}` {
		t.Errorf("status=%d raw=%s", status, raw)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotMethod != "POST" || string(gotBody) != `{"x":1}` {
		t.Errorf("method=%s body=%s", gotMethod, gotBody)
	}
}

func TestDo_NoContentTypeAndTimeoutDefault(t *testing.T) {
	var hadCT bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadCT = r.Header["Content-Type"]
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	// timeoutMS <= 0 exercises the 15s fallback; empty contentType omits the header.
	status, raw, err := Do(context.Background(), "GET", srv.URL, "tok", "", nil, 0, 0)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != http.StatusTeapot || string(raw) != "nope" {
		t.Errorf("status=%d raw=%s", status, raw)
	}
	if hadCT {
		t.Error("Content-Type header should be absent when contentType is empty")
	}
}

func TestErrMessage(t *testing.T) {
	// Envelope message wins.
	if got := ErrMessage([]byte(`{"error":{"message":"bad scope"}}`), 100); got != "bad scope" {
		t.Errorf("envelope = %q", got)
	}
	// Malformed/empty envelope, short body → full body.
	if got := ErrMessage([]byte("oops"), 100); got != "oops" {
		t.Errorf("short body = %q", got)
	}
	// Long body with no envelope → truncated to limit.
	long := strings.Repeat("z", 50)
	if got := ErrMessage([]byte(long), 10); got != long[:10] {
		t.Errorf("truncated = %q (len %d)", got, len(got))
	}
}
