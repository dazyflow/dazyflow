// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// ----------------------------------------------------------------------
// http_download helpers: downloadURL, downloadHeaders, downloadStatusOK.
// (int-slice parsing now lives in drops/internal/params.IntSlice.)
// ----------------------------------------------------------------------

func TestDownloadURL_InputOverridesParam(t *testing.T) {
	// Input["url"] wins over params.url.
	job := core.Job{
		Input:  map[string]core.Ref{"url": {Inline: "https://from-input"}},
		Params: map[string]any{"url": "https://from-params"},
	}
	if got := downloadURL(job); got != "https://from-input" {
		t.Errorf("got %q, want from-input", got)
	}
	// Empty Input falls through to params.
	job.Input = nil
	if got := downloadURL(job); got != "https://from-params" {
		t.Errorf("got %q, want from-params", got)
	}
	// Input present but Inline is not a string → falls through to params.
	job.Input = map[string]core.Ref{"url": {Inline: 42}}
	if got := downloadURL(job); got != "https://from-params" {
		t.Errorf("got %q (non-string input), want from-params", got)
	}
	// Input with empty string → falls through.
	job.Input = map[string]core.Ref{"url": {Inline: ""}}
	if got := downloadURL(job); got != "https://from-params" {
		t.Errorf("got %q (empty input), want from-params", got)
	}
}

func TestDownloadHeaders_Variants(t *testing.T) {
	// nil/missing returns nil headers, no error.
	if got, err := downloadHeaders(map[string]any{}); err != nil || got != nil {
		t.Errorf("missing → (%v, %v)", got, err)
	}
	if got, err := downloadHeaders(map[string]any{"headers": nil}); err != nil || got != nil {
		t.Errorf("nil → (%v, %v)", got, err)
	}
	// Happy path.
	in := map[string]any{"headers": map[string]any{"X-Auth": "abc"}}
	got, err := downloadHeaders(in)
	if err != nil || got["X-Auth"] != "abc" {
		t.Errorf("happy → (%v, %v)", got, err)
	}
	// Not an object → error.
	if _, err := downloadHeaders(map[string]any{"headers": "string"}); err == nil {
		t.Error("string headers: want error")
	}
	// Non-string value → error mentioning the key.
	bad := map[string]any{"headers": map[string]any{"X-Num": 42}}
	if _, err := downloadHeaders(bad); err == nil || !strings.Contains(err.Error(), "X-Num") {
		t.Errorf("non-string val → %v, want one mentioning 'X-Num'", err)
	}
}

func TestDownloadStatusOK(t *testing.T) {
	// Empty expect → default 2xx.
	if !params.StatusAccepted(200, nil) {
		t.Error("200 with empty expect: want OK")
	}
	if params.StatusAccepted(404, nil) {
		t.Error("404 with empty expect: want NOT OK")
	}
	// Non-empty expect must match exactly.
	if !params.StatusAccepted(404, []int{404}) {
		t.Error("404 in expect=[404]: want OK")
	}
	if params.StatusAccepted(200, []int{404}) {
		t.Error("200 with expect=[404]: want NOT OK")
	}
	if !params.StatusAccepted(204, []int{200, 204}) {
		t.Error("204 in expect=[200,204]: want OK")
	}
}

// TestHTTPDownload_AcceptsCustomExpectStatus covers the
// expect_status configuration path end-to-end.
func TestHTTPDownload_AcceptsCustomExpectStatus(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("nope"), 404)
	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"expect_status":          []any{float64(404)},
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
}

// TestHTTPDownload_BadHeaders covers the bad-param branch for headers
// (object map → string val expected).
func TestHTTPDownload_BadHeaders(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("x"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"headers":                map[string]any{"X-N": 42},
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, errCode(res))
	}
}

// TestHTTPDownload_SendsHeaders confirms the headers loop in the
// execute path actually sets request headers (the for-range over
// headers). Uses an httptest server that echoes back the seen
// X-Foo header.
func TestHTTPDownload_SendsHeaders(t *testing.T) {
	ws := t.TempDir()
	var seenHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Foo")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "out.txt",
			"headers":                map[string]any{"X-Foo": "bar"},
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if seenHeader != "bar" {
		t.Errorf("server saw X-Foo=%q, want 'bar'", seenHeader)
	}
}

// TestHTTPDownload_MkdirsBeforeCreate covers the mkdirs=true branch of
// executeHTTPDownload.
func TestHTTPDownload_MkdirsBeforeCreate(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("hi"), 200)
	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "deep/sub/dir/file.txt",
			"mkdirs":                 true,
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
	if _, err := os.Stat(filepath.Join(ws, "deep/sub/dir/file.txt")); err != nil {
		t.Errorf("file not written under mkdir'd path: %v", err)
	}
}

// TestHTTPDownload_BadURLRequest covers the http.NewRequestWithContext
// error path — pass a URL the http package can't parse.
func TestHTTPDownload_BadURLRequest(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url":                    "http://%%%%/bad",
			"path":                   "x.txt",
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
}

// TestHTTPDownload_HTTPErrorWithoutSSRF covers the "http" error code
// branch — the server closes the connection without responding so
// resp.Do() errors with a non-SSRF reason.
func TestHTTPDownload_HTTPErrorWithoutSSRF(t *testing.T) {
	ws := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close to produce a stream error after request acceptance.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
}

// ----------------------------------------------------------------------
// streamToFile via mock root: short-write injection covers the "io"
// error code branch when the destination Write fails.
// ----------------------------------------------------------------------

type erroringWriter struct{}

func (erroringWriter) Write(_ []byte) (int, error) { return 0, errors.New("disk full") }

type noopRoot struct{}

func (noopRoot) Remove(_ string) error { return nil }

func TestStreamToFile_WriteFailure(t *testing.T) {
	job := core.Job{ID: "j"}
	src := strings.NewReader("some content")
	written, errRes := streamToFile(job, src, erroringWriter{}, noopRoot{}, "rel", 0)
	if errRes == nil {
		t.Fatal("expected error result")
	}
	if errRes.Error == nil || errRes.Error.Code != "io" {
		t.Errorf("code = %q, want io", errCode(*errRes))
	}
	if written != 0 {
		t.Errorf("written = %d, want 0 on failure", written)
	}
}

// ----------------------------------------------------------------------
// file_write: isSandboxEscape on various error shapes
// ----------------------------------------------------------------------

func TestIsSandboxEscape(t *testing.T) {
	if isSandboxEscape(nil) {
		t.Error("nil err: want false")
	}
	if !isSandboxEscape(os.ErrInvalid) {
		t.Error("os.ErrInvalid: want true")
	}
	if !isSandboxEscape(errors.New("path escapes root")) {
		t.Error("'path escapes': want true")
	}
	if !isSandboxEscape(errors.New("argument outside root")) {
		t.Error("'outside root': want true")
	}
	if !isSandboxEscape(errors.New("invalid argument")) {
		t.Error("'invalid argument': want true")
	}
	if isSandboxEscape(errors.New("some other error")) {
		t.Error("unrelated err: want false")
	}
}

// ----------------------------------------------------------------------
// inlineToBytes: every input shape
// ----------------------------------------------------------------------

func TestInlineToBytes_Variants(t *testing.T) {
	// []byte passes through.
	got, err := inlineToBytes([]byte("hello"))
	if err != nil || string(got) != "hello" {
		t.Errorf("[]byte → (%q, %v)", got, err)
	}
	// string → []byte.
	got, err = inlineToBytes("hi")
	if err != nil || string(got) != "hi" {
		t.Errorf("string → (%q, %v)", got, err)
	}
	// Anything else marshals to JSON.
	got, err = inlineToBytes(map[string]any{"k": "v"})
	if err != nil {
		t.Errorf("map: err = %v", err)
	}
	var back map[string]string
	if err := json.Unmarshal(got, &back); err != nil || back["k"] != "v" {
		t.Errorf("JSON marshal: %s err=%v", got, err)
	}
}

// silence unused: io.Discard if a sub-test stops using it
var _ = io.Discard

// ----------------------------------------------------------------------
// file_write error branches: missing param, missing input
// ----------------------------------------------------------------------

func TestFileWrite_MissingPathParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Inline: "hi"}},
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

func TestFileWrite_MissingInputPort(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("code = %q, want missing_input", errCode(res))
	}
}

func TestFileWrite_NoSandbox(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		// No WorkspaceRoot, no ScratchRoot.
		Input:  map[string]core.Ref{"in": {Inline: "hi"}},
		Params: map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}

// TestFileWrite_FromRef covers the input.Ref path: read from a source
// file (in the sandbox) and write to a destination.
func TestFileWrite_FromRef(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("from-ref"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "src.txt", MIME: "text/plain"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "dst.txt"))
	if string(got) != "from-ref" {
		t.Errorf("dst = %q, want 'from-ref'", got)
	}
}

func TestFileWrite_FromMissingRef(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "ghost.txt"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
}

// ----------------------------------------------------------------------
// file_read missing path param + missing sandbox.
// ----------------------------------------------------------------------

func TestFileRead_MissingPathParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestHTTPDownload_MissingURL covers the early bad_param branch.
func TestHTTPDownload_MissingURL(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "x.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestHTTPDownload_MissingPath covers the bad_param branch when url is
// supplied but path isn't.
func TestHTTPDownload_MissingPath(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": "https://example/x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestHTTPDownload_NoSandbox covers the no_sandbox branch.
func TestHTTPDownload_NoSandbox(t *testing.T) {
	srv := downloadServer(t, []byte("x"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		// no WorkspaceRoot, no scratch
		Params: map[string]any{"url": srv.URL, "path": "x.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}

// TestHTTPUpload_MissingURL covers the bad_param branch.
func TestHTTPUpload_MissingURL(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "x.txt"}},
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}
