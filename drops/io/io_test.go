// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// ----------------------------------------------------------------------
// file_read / file_picker: missing-file stat io error (non-escape).
// ----------------------------------------------------------------------

func TestFileRead_MissingFileStatIO(t *testing.T) {
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"path": "ghost.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io (stat of missing file)", errCode(res))
	}
}

// ----------------------------------------------------------------------
// file_write: inline marshal error (both in the write and the quota-size
// path), plus the empty-input determineWriteSize branch.
// ----------------------------------------------------------------------

// unmarshalable is a value json.Marshal refuses (a func can't be encoded), so
// inlineToBytes returns an error.
func unmarshalable() any { return map[string]any{"bad": func() {}} }

func TestFileWrite_InlineMarshalError(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"in": {Inline: unmarshalable()}},
		Params:        map[string]any{"path": "out.json"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io (inline marshal failure)", errCode(res))
	}
}

func TestFileWrite_QuotaSizeMarshalError(t *testing.T) {
	// QuotaLimit > 0 routes through determineWriteSize, whose inlineToBytes
	// fails on the unmarshalable value before any disk write.
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		QuotaLimit:    1024,
		Input:         map[string]core.Ref{"in": {Inline: unmarshalable()}},
		Params:        map[string]any{"path": "out.json"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io (quota size marshal failure)", errCode(res))
	}
}

// TestFileWrite_EmptyInputUnderQuota covers determineWriteSize's final branch
// (input has neither Ref nor Inline ⇒ size 0) under an active quota, and the
// subsequent zero-byte write succeeds.
func TestFileWrite_EmptyInputUnderQuota(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    1024,
		Input:         map[string]core.Ref{"in": {}}, // no Ref, no Inline
		Params:        map[string]any{"path": "empty.txt"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "empty.txt")); len(b) != 0 {
		t.Errorf("file = %q, want empty", b)
	}
}

// TestFileWrite_MkdirsSuccess covers the mkdirs success branch (nested folder
// created before the write).
func TestFileWrite_MkdirsSuccess(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Inline: "hi"}},
		Params:        map[string]any{"path": "a/b/c.txt", "mkdirs": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "a", "b", "c.txt")); string(b) != "hi" {
		t.Errorf("file = %q, want hi", b)
	}
}

// TestFileWrite_RefInputCopies covers the file-ref copy branch (input.Ref set,
// src opened through its own root and io.Copy'd to dest).
func TestFileWrite_RefInputCopies(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("copied"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "src.txt"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "dst.txt")); string(b) != "copied" {
		t.Errorf("dst = %q, want copied", b)
	}
}

// TestFileWrite_RefInputMissingFile covers the open-input io-error branch (the
// ref points at a file that doesn't exist).
func TestFileWrite_RefInputMissingFile(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"in": {Ref: "nope.txt"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io (open missing input ref)", errCode(res))
	}
}

// ----------------------------------------------------------------------
// http_download: egress allowlist block, cancelled context, ErrNotExist on a
// nested path without mkdirs, and mkdirs success.
// ----------------------------------------------------------------------

func TestHTTPDownload_EgressBlocked(t *testing.T) {
	t.Cleanup(func() { _ = hfnet.SetEgressAllowlist(nil) })
	if err := hfnet.SetEgressAllowlist([]string{"allowed.example"}); err != nil {
		t.Fatalf("SetEgressAllowlist: %v", err)
	}
	srv := downloadServer(t, []byte("x"), 200) // 127.0.0.1, not in allowlist
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt", "allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "egress_blocked" {
		t.Errorf("code = %q, want egress_blocked", errCode(res))
	}
}

func TestHTTPDownload_Cancelled(t *testing.T) {
	srv := downloadServer(t, []byte("x"), 200)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel before the request runs
	res, err := executeHTTPDownload(ctx, core.Job{
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt", "allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "cancelled" {
		t.Errorf("code = %q (err %v), want cancelled", errCode(res), err)
	}
}

func TestHTTPDownload_NestedPathNoMkdirs(t *testing.T) {
	srv := downloadServer(t, []byte("data"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"url": srv.URL, "path": "missing/dir/x.txt",
			"allow_private_networks": true,
		},
	}, nil)
	// Parent folder doesn't exist and mkdirs is off ⇒ the friendly io error.
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io (nested path, mkdirs off)", errCode(res))
	}
}

func TestHTTPDownload_MkdirsSuccess(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("data"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "nested/out.txt", "mkdirs": true,
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "nested", "out.txt")); string(b) != "data" {
		t.Errorf("file = %q, want data", b)
	}
}

// TestDownloadRequestBody_MarshalError covers downloadRequestBody's
// json.Marshal failure on an unmarshalable structured input.
func TestDownloadRequestBody_MarshalError(t *testing.T) {
	_, err := downloadRequestBody(core.Job{
		Input: map[string]core.Ref{"request_body": {Inline: func() {}}},
	})
	if err == nil {
		t.Error("expected marshal error for an unmarshalable request_body")
	}
}

// ----------------------------------------------------------------------
// http_upload: egress allowlist block, bad headers param, cancelled context.
// ----------------------------------------------------------------------

func TestHTTPUpload_EgressBlocked(t *testing.T) {
	t.Cleanup(func() { _ = hfnet.SetEgressAllowlist(nil) })
	if err := hfnet.SetEgressAllowlist([]string{"allowed.example"}); err != nil {
		t.Fatalf("SetEgressAllowlist: %v", err)
	}
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	srv := downloadServer(t, []byte("ok"), 200)
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "f.txt", "allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "egress_blocked" {
		t.Errorf("code = %q, want egress_blocked", errCode(res))
	}
}

func TestHTTPUpload_BadHeaders(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": "https://allowed.example/x", "path": "f.txt",
			"headers":                "not-an-object",
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param (bad headers)", errCode(res))
	}
}

func TestHTTPUpload_Cancelled(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	srv := downloadServer(t, []byte("ok"), 200)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, _ := executeHTTPUpload(ctx, core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "f.txt", "allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "cancelled" {
		t.Errorf("code = %q, want cancelled", errCode(res))
	}
}

// errResv is a non-quota reserve failure for the generic "quota" error branch.
var errResv = errors.New("reserver unavailable")

// ----------------------------------------------------------------------
// file_picker: scratch://, inline, sandbox escape, missing-file io error.
// ----------------------------------------------------------------------

func TestFilePicker_MissingPathParam(t *testing.T) {
	res, _ := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

func TestFilePicker_NoSandbox(t *testing.T) {
	res, _ := executeFilePicker(t.Context(), core.Job{
		Params: map[string]any{"path": "scratch://x.txt"}, // scratch scheme, no ScratchRoot
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}

func TestFilePicker_MissingFileIsIOError(t *testing.T) {
	res, _ := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"path": "ghost.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io", errCode(res))
	}
}

func TestFilePicker_InlineBinaryMIME(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := executeFilePicker(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "blob.bin", "inline": true, "mime": "application/octet-stream"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	got, ok := res.Output["file"].Inline.([]byte)
	if !ok || !bytes.Equal(got, []byte{0x00, 0x01, 0x02}) {
		t.Errorf("inline = %v (%T), want raw bytes", res.Output["file"].Inline, res.Output["file"].Inline)
	}
}

// ----------------------------------------------------------------------
// file_read: directory error, sandbox escape, inline binary.
// ----------------------------------------------------------------------

func TestFileRead_NoSandbox(t *testing.T) {
	res, _ := executeFileRead(t.Context(), core.Job{
		Params: map[string]any{"path": "scratch://x.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}

func TestFileRead_DirectoryIsError(t *testing.T) {
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "sub"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io for directory", errCode(res))
	}
}

func TestFileRead_SandboxEscape(t *testing.T) {
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"path": "../escape.txt"},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for escape", res.Status)
	}
}

func TestFileRead_InlineBinary(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "b.bin"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "b.bin", "inline": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if got, ok := res.Output["out"].Inline.([]byte); !ok || !bytes.Equal(got, []byte{0xff, 0xfe}) {
		t.Errorf("inline = %v, want raw bytes (default octet-stream MIME)", res.Output["out"].Inline)
	}
}

// ----------------------------------------------------------------------
// file_write: quota snapshot, mkdirs escape, sandbox escape on create.
// ----------------------------------------------------------------------

func TestFileWrite_QuotaSnapshotExceeded(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    4,
		QuotaUsed:     0,
		Input:         map[string]core.Ref{"in": {Inline: "way too many bytes"}},
		Params:        map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "quota_exceeded" {
		t.Errorf("code = %q, want quota_exceeded", errCode(res))
	}
}

func TestFileWrite_QuotaAllowsUnderLimit(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    1024,
		Input:         map[string]core.Ref{"in": {Inline: "small"}},
		Params:        map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
}

func TestFileWrite_LiveReserverRejects(t *testing.T) {
	t.Cleanup(func() { SetQuotaReserver(nil) })
	SetQuotaReserver(func(string, int64) (func(), error) { return nil, core.ErrQuotaExceeded })

	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    1 << 30, // snapshot passes; live reserver rejects
		Input:         map[string]core.Ref{"in": {Inline: "data"}},
		Params:        map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "quota_exceeded" {
		t.Errorf("code = %q, want quota_exceeded", errCode(res))
	}
}

func TestFileWrite_LiveReserverGenericError(t *testing.T) {
	t.Cleanup(func() { SetQuotaReserver(nil) })
	SetQuotaReserver(func(string, int64) (func(), error) { return nil, errResv })

	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    1 << 30,
		Input:         map[string]core.Ref{"in": {Inline: "data"}},
		Params:        map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "quota" {
		t.Errorf("code = %q, want quota", errCode(res))
	}
}

func TestFileWrite_SandboxEscapeOnCreate(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"in": {Inline: "x"}},
		Params:        map[string]any{"path": "../escape.txt"},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for escape", res.Status)
	}
}

func TestFileWrite_MkdirsEscape(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"in": {Inline: "x"}},
		Params:        map[string]any{"path": "../../out.txt", "mkdirs": true},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for mkdirs escape", res.Status)
	}
}

func TestFileWrite_InlineStructMarshalsJSON(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Inline: map[string]any{"k": "v"}}},
		Params:        map[string]any{"path": "obj.json"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "obj.json"))
	if string(got) != `{"k":"v"}` {
		t.Errorf("file = %q, want JSON object", got)
	}
}

// ----------------------------------------------------------------------
// determineWriteSize: ref stat error (missing source) under quota.
// ----------------------------------------------------------------------

func TestFileWrite_QuotaSizeFromMissingRef(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    1024,
		Input:         map[string]core.Ref{"in": {Ref: "ghost.txt"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io (size of missing ref)", errCode(res))
	}
}

func TestFileWrite_QuotaSizeFromRef(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("seven!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		QuotaLimit:    1024,
		Input:         map[string]core.Ref{"in": {Ref: "src.txt"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
}

// ----------------------------------------------------------------------
// streamToFile: too_large (max_bytes) and quota_exceeded snapshot.
// ----------------------------------------------------------------------

func TestStreamToFile_TooLarge(t *testing.T) {
	src := strings.NewReader("0123456789")
	written, errRes := streamToFile(core.Job{ID: "j"}, src, &bytes.Buffer{}, noopRoot{}, "rel", 4)
	if errRes == nil || errRes.Error.Code != "too_large" {
		t.Fatalf("got %+v, want too_large", errRes)
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
}

func TestStreamToFile_QuotaSnapshotExceeded(t *testing.T) {
	src := strings.NewReader("0123456789")
	written, errRes := streamToFile(core.Job{ID: "j", QuotaLimit: 4}, src, &bytes.Buffer{}, noopRoot{}, "rel", 0)
	if errRes == nil || errRes.Error.Code != "quota_exceeded" {
		t.Fatalf("got %+v, want quota_exceeded", errRes)
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
}

func TestStreamToFile_LiveReserverRejects(t *testing.T) {
	t.Cleanup(func() { SetQuotaReserver(nil) })
	SetQuotaReserver(func(string, int64) (func(), error) { return nil, core.ErrQuotaExceeded })

	// QuotaLimit set high so the snapshot passes and the live reserver is the
	// one that rejects.
	written, errRes := streamToFile(core.Job{ID: "j", QuotaLimit: 1 << 30}, strings.NewReader("data"), &bytes.Buffer{}, noopRoot{}, "rel", 0)
	if errRes == nil || errRes.Error.Code != "quota_exceeded" {
		t.Fatalf("got %+v, want quota_exceeded from live reserver", errRes)
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
}

func TestStreamToFile_LiveReserverGenericError(t *testing.T) {
	t.Cleanup(func() { SetQuotaReserver(nil) })
	SetQuotaReserver(func(string, int64) (func(), error) { return nil, errResv })

	written, errRes := streamToFile(core.Job{ID: "j", QuotaLimit: 1 << 30}, strings.NewReader("data"), &bytes.Buffer{}, noopRoot{}, "rel", 0)
	if errRes == nil || errRes.Error.Code != "quota" {
		t.Fatalf("got %+v, want quota (generic reserve error)", errRes)
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
}

func TestStreamToFile_LiveReserverAllows(t *testing.T) {
	t.Cleanup(func() { SetQuotaReserver(nil) })
	released := false
	SetQuotaReserver(func(string, int64) (func(), error) { return func() { released = true }, nil })

	var buf bytes.Buffer
	written, errRes := streamToFile(core.Job{ID: "j", QuotaLimit: 1 << 30}, strings.NewReader("hi"), &buf, noopRoot{}, "rel", 0)
	if errRes != nil {
		t.Fatalf("unexpected error: %+v", errRes)
	}
	if written != 2 || !released {
		t.Errorf("written=%d released=%v, want 2/true", written, released)
	}
}

func TestStreamToFile_Success(t *testing.T) {
	var buf bytes.Buffer
	written, errRes := streamToFile(core.Job{ID: "j"}, strings.NewReader("hello"), &buf, noopRoot{}, "rel", 0)
	if errRes != nil {
		t.Fatalf("unexpected error: %+v", errRes)
	}
	if written != 5 || buf.String() != "hello" {
		t.Errorf("written=%d buf=%q, want 5/hello", written, buf.String())
	}
}

// ----------------------------------------------------------------------
// downloadRequestBody: each input shape + params.body.
// ----------------------------------------------------------------------

func TestDownloadRequestBody_Variants(t *testing.T) {
	read := func(t *testing.T, job core.Job) string {
		t.Helper()
		r, err := downloadRequestBody(job)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if r == nil {
			return ""
		}
		b, _ := io.ReadAll(r)
		return string(b)
	}

	if got := read(t, core.Job{Input: map[string]core.Ref{"request_body": {Inline: "str-body"}}}); got != "str-body" {
		t.Errorf("string input = %q, want str-body", got)
	}
	if got := read(t, core.Job{Input: map[string]core.Ref{"request_body": {Inline: []byte("byte-body")}}}); got != "byte-body" {
		t.Errorf("bytes input = %q, want byte-body", got)
	}
	if got := read(t, core.Job{Input: map[string]core.Ref{"request_body": {Inline: map[string]any{"a": 1}}}}); got != `{"a":1}` {
		t.Errorf("struct input = %q, want JSON", got)
	}
	// nil inline falls through to params.body.
	if got := read(t, core.Job{
		Input:  map[string]core.Ref{"request_body": {Inline: nil}},
		Params: map[string]any{"body": "param-body"},
	}); got != "param-body" {
		t.Errorf("param fallthrough = %q, want param-body", got)
	}
	// Nothing set ⇒ nil reader.
	if got := read(t, core.Job{}); got != "" {
		t.Errorf("no body = %q, want empty", got)
	}
}

// ----------------------------------------------------------------------
// http_download: too_large via max_bytes, request_body POST, method.
// ----------------------------------------------------------------------

func TestHTTPDownload_TooLarge(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("0123456789ABCDEF"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"max_bytes":              float64(4),
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "too_large" {
		t.Errorf("code = %q, want too_large", errCode(res))
	}
}

func TestHTTPDownload_PostWithBody(t *testing.T) {
	ws := t.TempDir()
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"request_body": {Inline: "payload"}},
		Params: map[string]any{
			"url": srv.URL, "path": "out.txt", "method": "post",
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if gotMethod != "POST" || string(gotBody) != "payload" {
		t.Errorf("server saw %s/%q, want POST/payload", gotMethod, gotBody)
	}
}

func TestHTTPDownload_BadScheme(t *testing.T) {
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"url": "ftp://example/x", "path": "x.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_url" {
		t.Errorf("code = %q, want bad_url", errCode(res))
	}
}

// ----------------------------------------------------------------------
// http_upload: bad scheme, expect_status, content_type override, response
// body text/binary, uploadSrcPath from input ref.
// ----------------------------------------------------------------------

func TestHTTPUpload_BadScheme(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": "ftp://example/x", "path": "f.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_url" {
		t.Errorf("code = %q, want bad_url", errCode(res))
	}
}

func TestHTTPUpload_MissingPath(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": "https://example.com/x", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

func TestHTTPUpload_UnexpectedStatus(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	srv := downloadServer(t, []byte("nope"), 500)
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": srv.URL, "path": "f.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "unexpected_status" {
		t.Errorf("code = %q, want unexpected_status", errCode(res))
	}
}

func TestHTTPUpload_ContentTypeOverrideAndTextResponse(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "data", "payload") // no extension ⇒ exercises content_type override
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("server-reply"))
	}))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "data",
			"content_type":           "application/x-custom",
			"expect_status":          []any{float64(200)},
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if gotCT != "application/x-custom" {
		t.Errorf("server saw content-type %q, want application/x-custom", gotCT)
	}
	// text/plain response inlines as a string.
	if got, _ := res.Output["response_body"].Inline.(string); got != "server-reply" {
		t.Errorf("response body = %v, want string 'server-reply'", res.Output["response_body"].Inline)
	}
}

func TestHTTPUpload_BinaryResponse(t *testing.T) {
	ws := t.TempDir()
	seedUploadFile(t, ws, "f.txt", "x")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x01, 0x02})
	}))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": srv.URL, "path": "f.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if got, ok := res.Output["response_body"].Inline.([]byte); !ok || !bytes.Equal(got, []byte{0x01, 0x02}) {
		t.Errorf("binary response = %v, want raw bytes", res.Output["response_body"].Inline)
	}
}

func TestHTTPUpload_NoSandbox(t *testing.T) {
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		Params: map[string]any{"url": "https://example.com/x", "path": "scratch://f.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}
