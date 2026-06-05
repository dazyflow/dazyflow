package net

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestHTTP_GETSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	res, err := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"allow_private_networks": true, // httptest binds 127.0.0.1
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (err=%+v)", res.Status, res.Error)
	}
	if got, _ := res.Output["response_body"].Inline.(string); got != "hello" {
		t.Errorf("body = %q, want hello", got)
	}
	if status, _ := res.Output["status"].Inline.(int); status != 200 {
		t.Errorf("status = %v, want 200", res.Output["status"].Inline)
	}
	if _, ok := res.Output["headers"].Inline.(map[string]string); !ok {
		t.Errorf("headers port missing or wrong type: %T", res.Output["headers"].Inline)
	}
}

func TestHTTP_URLFromInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("from-input"))
	}))
	defer srv.Close()

	// No url param — the target comes entirely from the wired `url` input,
	// proving the input port can drive the request on its own.
	res, err := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{"allow_private_networks": true},
		Input:  map[string]core.Ref{"url": {Inline: srv.URL}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (err=%+v)", res.Status, res.Error)
	}
	if got, _ := res.Output["response_body"].Inline.(string); got != "from-input" {
		t.Errorf("body = %q, want from-input", got)
	}
}

func TestHTTP_InputURLOverridesParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// Both set: the wired input must win over the param.
	res, err := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    "https://param.example.com/should-not-be-called",
			"allow_private_networks": true,
		},
		Input: map[string]core.Ref{"url": {Inline: srv.URL}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (err=%+v) — input URL should have been used", res.Status, res.Error)
	}
}

func TestHTTP_POSTWithInputBody(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = string(body)
		w.WriteHeader(201)
	}))
	defer srv.Close()

	res, err := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"method":                 "POST",
			"expect_status":          []any{201.0},
			"allow_private_networks": true,
		},
		Input: map[string]core.Ref{
			"request_body": {MIME: "text/plain", Inline: "payload from upstream"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if seen != "payload from upstream" {
		t.Errorf("server saw %q, want 'payload from upstream'", seen)
	}
}

func TestHTTP_HeadersForwarded(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	_, err := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"headers":                map[string]any{"Authorization": "Bearer tok"},
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", auth)
	}
}

func TestHTTP_UnexpectedStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error.Code != "unexpected_status" {
		t.Errorf("code = %q, want unexpected_status", res.Error.Code)
	}
}

func TestHTTP_ExpectStatusOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"expect_status":          []any{404.0, 410.0},
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Errorf("status=%q (%+v); 404 should pass when explicitly expected", res.Status, res.Error)
	}
}

func TestHTTP_BodyTooLargeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"max_body_bytes":         100,
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error.Code != "body_too_large" {
		t.Errorf("code = %q, want body_too_large", res.Error.Code)
	}
}

func TestHTTP_TimeoutHonored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	start := time.Now()
	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"timeout_ms":             50,
			"allow_private_networks": true,
		},
	}, nil)
	elapsed := time.Since(start)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("took %v; timeout should have cut earlier", elapsed)
	}
}

// SSRF — these tests do NOT need a server. The block happens at dial
// time before the handshake completes.
func TestHTTP_SSRFBlocksLoopback(t *testing.T) {
	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":        "http://127.0.0.1:1/",
			"timeout_ms": 1000,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error.Code != "ssrf_blocked" {
		t.Errorf("code = %q, want ssrf_blocked (msg=%q)", res.Error.Code, res.Error.Message)
	}
}

func TestHTTP_SSRFBlocksAWSMetadataIP(t *testing.T) {
	// 169.254.169.254 is the AWS instance-metadata service. Any tenant
	// hitting this from a sandbox would leak instance credentials.
	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":        "http://169.254.169.254/latest/meta-data/",
			"timeout_ms": 1000,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error.Code != "ssrf_blocked" {
		t.Errorf("code = %q, want ssrf_blocked", res.Error.Code)
	}
}

func TestHTTP_SSRFBlocksRFC1918(t *testing.T) {
	for _, addr := range []string{
		"http://10.0.0.1/",
		"http://192.168.1.1/",
		"http://172.16.0.1/",
	} {
		t.Run(addr, func(t *testing.T) {
			res, _ := executeHTTPRequest(t.Context(), core.Job{
				Params: map[string]any{
					"url":        addr,
					"timeout_ms": 1000,
				},
			}, nil)
			if res.Status != core.StatusError || res.Error.Code != "ssrf_blocked" {
				t.Errorf("%s: status=%q code=%q, want ssrf_blocked",
					addr, res.Status, res.Error.Code)
			}
		})
	}
}

func TestHTTP_SSRFOptInAllowsLoopback(t *testing.T) {
	// Sanity: with allow_private_networks=true the same loopback URL
	// can be reached. Otherwise httptest-based tests above couldn't run.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Errorf("status=%q, want ok with allow_private_networks=true", res.Status)
	}
}

func TestHTTP_BadURLRejected(t *testing.T) {
	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url": "not a url at all",
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

func TestHTTP_MissingURL(t *testing.T) {
	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", res.Error.Code)
	}
}

func TestHTTP_JSONBodyResponseRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body) // echo
	}))
	defer srv.Close()

	res, _ := executeHTTPRequest(t.Context(), core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"method":                 "POST",
			"body":                   `{"hello":"world"}`,
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	body, _ := res.Output["response_body"].Inline.(string)
	if !strings.Contains(body, `"hello":"world"`) {
		t.Errorf("body = %q, want JSON echo", body)
	}
	if res.Output["response_body"].MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json", res.Output["response_body"].MIME)
	}
}

func TestHTTP_CancellationViaContext(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	res, err := executeHTTPRequest(ctx, core.Job{
		Params: map[string]any{
			"url":                    srv.URL,
			"timeout_ms":             5000,
			"allow_private_networks": true,
		},
	}, nil)
	if err == nil && res.Status == core.StatusOK {
		t.Fatal("expected cancellation to surface as error")
	}
}
