package io

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
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
