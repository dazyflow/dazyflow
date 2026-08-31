// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func downloadServer(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPDownload_StreamsToWorkspace(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("hello world"), 200)

	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "out.txt",
			"allow_private_networks": true, // httptest listens on loopback
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
	got, rerr := os.ReadFile(filepath.Join(ws, "out.txt"))
	if rerr != nil || string(got) != "hello world" {
		t.Errorf("file = %q err=%v, want 'hello world'", got, rerr)
	}
	if res.Output["out"].Ref != "out.txt" {
		t.Errorf("out Ref = %q", res.Output["out"].Ref)
	}
	if meta, _ := res.Output["meta"].Inline.(map[string]any); meta["bytes"].(int64) != 11 {
		t.Errorf("meta bytes = %v, want 11", meta["bytes"])
	}
}

func TestHTTPDownload_ScratchPath(t *testing.T) {
	ws, scratch := t.TempDir(), t.TempDir()
	srv := downloadServer(t, []byte("ephemeral"), 200)

	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws, ScratchRoot: scratch,
		Params: map[string]any{"url": srv.URL, "path": "scratch://dl.bin", "allow_private_networks": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if _, serr := os.Stat(filepath.Join(scratch, "dl.bin")); serr != nil {
		t.Errorf("file not under scratch root: %v", serr)
	}
	if res.Output["out"].Ref != "scratch://dl.bin" {
		t.Errorf("out Ref = %q, want scratch:// preserved", res.Output["out"].Ref)
	}
}

func TestHTTPDownload_MaxBytesAbortsAndCleansUp(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, make([]byte, 1000), 200)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "big.bin", "max_bytes": 100,
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "too_large" {
		t.Fatalf("status=%q code=%q, want too_large", res.Status, errCode(res))
	}
	// Partial file must be removed.
	if _, serr := os.Stat(filepath.Join(ws, "big.bin")); !os.IsNotExist(serr) {
		t.Errorf("partial file not cleaned up: %v", serr)
	}
}

func TestHTTPDownload_QuotaExceededAbortsAndCleansUp(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, make([]byte, 1000), 200)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    100, QuotaUsed: 0, // snapshot budget far below the 1000-byte body
		Params: map[string]any{"url": srv.URL, "path": "q.bin", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "quota_exceeded" {
		t.Fatalf("status=%q code=%q, want quota_exceeded", res.Status, errCode(res))
	}
	if _, serr := os.Stat(filepath.Join(ws, "q.bin")); !os.IsNotExist(serr) {
		t.Errorf("partial file not cleaned up: %v", serr)
	}
}

func TestHTTPDownload_SSRFBlockedByDefault(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("x"), 200) // loopback

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": srv.URL, "path": "x.txt"}, // no allow_private_networks
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "ssrf_blocked" {
		t.Errorf("status=%q code=%q, want ssrf_blocked (loopback)", res.Status, errCode(res))
	}
}

func TestHTTPDownload_UnexpectedStatus(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("nope"), 404)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": srv.URL, "path": "x.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "unexpected_status" {
		t.Errorf("status=%q code=%q, want unexpected_status", res.Status, errCode(res))
	}
}

func errCode(r core.Result) string {
	if r.Error != nil {
		return r.Error.Code
	}
	return ""
}

// A non-http(s) URL is rejected up front with bad_url, not left to fail later
// as an opaque transport error.
func TestHTTPDownload_RejectsNonHTTPScheme(t *testing.T) {
	for _, url := range []string{"file:///etc/passwd", "ftp://host/x", "example.com/x"} {
		res, _ := executeHTTPDownload(t.Context(), core.Job{
			WorkspaceRoot: t.TempDir(),
			Params:        map[string]any{"url": url, "path": "out.txt"},
		}, nil)
		if res.Status != core.StatusError || errCode(res) != "bad_url" {
			t.Errorf("url %q: status=%q code=%q, want bad_url", url, res.Status, errCode(res))
		}
	}
}

// The 'url' input accepts raw bytes (e.g. from an upstream text step) and
// trims surrounding whitespace so a trailing newline doesn't break the request.
func TestHTTPDownload_URLFromBytesInputTrimmed(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("via input"), 200)

	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"url": {Inline: []byte(srv.URL + "\n")}},
		Params:        map[string]any{"path": "out.txt", "allow_private_networks": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "out.txt"))
	if string(got) != "via input" {
		t.Errorf("file = %q, want 'via input'", got)
	}
}

// POST sends a body: from the 'body' param, and from the 'request_body' input
// port (which wins and JSON-marshals a structured value).
func TestHTTPDownload_POSTSendsBody(t *testing.T) {
	ws := t.TempDir()
	var gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	// (a) body from the param
	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "a.bin", "method": "POST",
			"body": `{"q":1}`, "allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("param body: status=%q (%+v)", res.Status, res.Error)
	}
	if gotMethod != "POST" || gotBody != `{"q":1}` {
		t.Errorf("param body: method=%q body=%q", gotMethod, gotBody)
	}

	// (b) request_body input wins over the param and JSON-marshals a struct.
	res, err = executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"request_body": {Inline: map[string]any{"k": "v"}}},
		Params: map[string]any{
			"url": srv.URL, "path": "b.bin", "method": "POST",
			"body": "ignored", "allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("input body: status=%q (%+v)", res.Status, res.Error)
	}
	if gotBody != `{"k":"v"}` {
		t.Errorf("input body should override param and be JSON-marshalled, got %q", gotBody)
	}
}

// Saving into a folder that doesn't exist (mkdirs off) gives a friendly
// message pointing at "Create missing folders", not a raw ENOENT.
func TestHTTPDownload_MissingFolderFriendlyError(t *testing.T) {
	srv := downloadServer(t, []byte("x"), 200)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"url": srv.URL, "path": "nope/out.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || errCode(res) != "io" {
		t.Fatalf("status=%q code=%q, want io", res.Status, errCode(res))
	}
	if !strings.Contains(res.Error.Message, "Create missing folders") {
		t.Errorf("message should point at the fix, got %q", res.Error.Message)
	}
}
