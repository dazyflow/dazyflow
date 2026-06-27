// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func seedUploadFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPUpload_RawPut(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "upload.txt", "payload")

	var gotMethod, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	res, err := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": srv.URL, "path": "upload.txt", "allow_private_networks": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if gotMethod != "PUT" {
		t.Errorf("method = %q, want PUT (raw default)", gotMethod)
	}
	if string(gotBody) != "payload" {
		t.Errorf("body = %q, want payload", gotBody)
	}
	if gotCT != "text/plain" {
		t.Errorf("content-type = %q, want text/plain (guessed from .txt)", gotCT)
	}
	if meta, _ := res.Output["meta"].Inline.(map[string]any); meta["bytes_sent"].(int64) != 7 {
		t.Errorf("bytes_sent = %v, want 7", meta["bytes_sent"])
	}
}

func TestHTTPUpload_Multipart(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "upload.txt", "payload")

	var gotMethod, gotFilename, gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		file, hdr, err := r.FormFile("doc")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer file.Close()
		gotFilename = hdr.Filename
		b, _ := io.ReadAll(file)
		gotContent = string(b)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	res, err := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "upload.txt",
			"multipart": true, "field_name": "doc", "filename": "report.txt",
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST (multipart default)", gotMethod)
	}
	if gotFilename != "report.txt" || gotContent != "payload" {
		t.Errorf("multipart got filename=%q content=%q, want report.txt/payload", gotFilename, gotContent)
	}
}

// TestHTTPUpload_MultipartServerRespondsEarly exercises the streaming
// multipart path when the server responds before draining the request body:
// the writer goroutine is mid-stream copying a large file into the pipe. The
// call must not deadlock, and the writer goroutine must clean up (the deferred
// pr.Close() guarantees it exits on every return, independent of transport
// internals).
func TestHTTPUpload_MultipartServerRespondsEarly(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "big.bin", strings.Repeat("x", 2<<20)) // 2 MiB

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // respond immediately, don't drain the body
	}))
	t.Cleanup(srv.Close)

	before := runtime.NumGoroutine()
	done := make(chan struct{})
	go func() {
		executeHTTPUpload(t.Context(), core.Job{
			WorkspaceRoot: ws,
			Params: map[string]any{
				"url": srv.URL, "path": "big.bin", "multipart": true,
				"allow_private_networks": true, "timeout_ms": 5000,
			},
		}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("multipart upload hung (writer goroutine deadlocked on the pipe)")
	}
	// Writer goroutine should have exited.
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("goroutine leak after early server response: before=%d after=%d", before, got)
	}
}

func TestHTTPUpload_FromInputRef(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "from-input.txt", "via-port")

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(201)
	}))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "from-input.txt"}},
		Params:        map[string]any{"url": srv.URL, "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if string(gotBody) != "via-port" {
		t.Errorf("body = %q, want via-port (from input ref)", gotBody)
	}
}

func TestHTTPUpload_SSRFBlockedByDefault(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": srv.URL, "path": "f.txt"}, // no allow_private_networks
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "ssrf_blocked" {
		t.Errorf("status=%q code=%q, want ssrf_blocked", res.Status, errCode(res))
	}
}

func TestHTTPUpload_MissingFile(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": "https://example.com", "path": "nope.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("status=%q code=%q, want io (missing file)", res.Status, errCode(res))
	}
}
