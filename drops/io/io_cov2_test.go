package io

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
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
