// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// uploadHarness extends the default gateway harness with a sandbox
// wired into the engine — the upload endpoint refuses to run without
// one, so every upload test needs it.
func newUploadHarness(t *testing.T) (*gatewayHarness, string) {
	t.Helper()
	h := newGatewayHarness(t)
	root := t.TempDir()
	sb, err := NewFSSandbox(root)
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	h.svc.Engine.Sandbox = sb
	return h, root
}

// uploadDo posts a multipart request with one "file" part containing
// payload, optionally with a "path" form field, using the given token.
func uploadDo(t *testing.T, h *gatewayHarness, token, tenant, workspace, filename, destPath string, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if destPath != "" {
		_ = mw.WriteField("path", destPath)
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(payload)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close mw: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/workspaces/"+tenant+"/"+workspace+"/files", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestUpload_OK(t *testing.T) {
	h, root := newUploadHarness(t)
	rw := uploadDo(t, h, h.token, "t", "ws", "sales.xlsx", "", []byte("hello world"))
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path != "sales.xlsx" || resp.Size != 11 {
		t.Errorf("resp = %+v, want path=sales.xlsx size=11", resp)
	}
	got, err := os.ReadFile(filepath.Join(root, "t", "ws", "sales.xlsx"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("contents = %q, want hello world", got)
	}
}

func TestUpload_CustomDestinationCreatesDirs(t *testing.T) {
	h, root := newUploadHarness(t)
	rw := uploadDo(t, h, h.token, "t", "ws", "ignored.csv", "imports/2026/q1/sales.xlsx", []byte("data"))
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "t", "ws", "imports/2026/q1/sales.xlsx"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "data" {
		t.Errorf("contents = %q", got)
	}
}

func TestUpload_StripsBrowserPathFromFilename(t *testing.T) {
	// Some browsers (legacy IE, some uploads) send "C:\\…\\sales.xlsx";
	// only the leaf should land on disk.
	h, root := newUploadHarness(t)
	rw := uploadDo(t, h, h.token, "t", "ws", `C:\Users\alice\sales.xlsx`, "", []byte("x"))
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "t", "ws", "sales.xlsx")); err != nil {
		t.Errorf("expected leaf-only filename to be written: %v", err)
	}
}

func TestUpload_PathTraversalRejected(t *testing.T) {
	h, _ := newUploadHarness(t)
	for _, attempt := range []string{"../escape.xlsx", "/etc/passwd", "../../leak"} {
		t.Run(attempt, func(t *testing.T) {
			rw := uploadDo(t, h, h.token, "t", "ws", "name.xlsx", attempt, []byte("x"))
			if rw.Code == http.StatusOK {
				t.Fatalf("upload to %q should have been rejected", attempt)
			}
		})
	}
}

func TestUpload_CrossTenantRejected(t *testing.T) {
	h, _ := newUploadHarness(t)
	// Token is scoped to tenant "t"; try writing to tenant "other".
	rw := uploadDo(t, h, h.token, "other", "ws", "x.bin", "", []byte("x"))
	if rw.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 403", rw.Code)
	}
}

func TestUpload_RequiresEditPermission(t *testing.T) {
	// Issue a runner-only key (graph:run, no graph:edit).
	h, _ := newUploadHarness(t)
	role := core.Role{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}
	_, runnerTok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-runner", "t", "ws", "bob", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	rw := uploadDo(t, h, runnerTok, "t", "ws", "x.bin", "", []byte("x"))
	if rw.Code != http.StatusForbidden {
		t.Errorf("status=%d, want 403", rw.Code)
	}
}

func TestUpload_MissingFilePart(t *testing.T) {
	h, _ := newUploadHarness(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("path", "x.bin")
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/workspaces/t/ws/files", &body)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rw.Code)
	}
}

func TestUpload_NoSandboxConfigured(t *testing.T) {
	// Skip the upload harness; Engine.Sandbox is nil → 503.
	h := newGatewayHarness(t)
	rw := uploadDo(t, h, h.token, "t", "ws", "x.bin", "", []byte("x"))
	if rw.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", rw.Code)
	}
}

// TestUpload_OutlivesServerReadTimeout is the regression test for the
// upload-timeout bug. http.Server.ReadTimeout covers reading the ENTIRE
// request including the body, and the gateway sets one global 30s value
// (ServeListener) shared by every route — so an upload was implicitly capped
// at whatever fits in 30s, not at maxUploadBytes: finishing a 200 MiB body
// inside it needs ~6.7 MB/s sustained. A large upload over an ordinary uplink
// died as a severed connection, never reaching the handler's 413.
//
// The handler now lifts that ceiling for its own request via
// http.ResponseController (which also requires jsonErrorWriter to expose
// Unwrap — it is the writer handlers actually receive). This drives a real
// server with a deliberately tiny ReadTimeout and a body streamed slower than
// it, so the request only completes if the deadline was extended.
func TestUpload_OutlivesServerReadTimeout(t *testing.T) {
	h, root := newUploadHarness(t)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		ServeForTest(h.gw, rw, r)
	}))
	// Stand in for the production 30s: short enough to hit in a unit test,
	// with the same "covers the whole body" semantics.
	srv.Config.ReadTimeout = 250 * time.Millisecond
	srv.Start()
	defer srv.Close()

	// Stream the multipart body in chunks spread over ~1s — comfortably past
	// ReadTimeout, comfortably inside uploadReadTimeout.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", "slow.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		for i := 0; i < 5; i++ {
			if _, err := part.Write([]byte("chunk")); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		_ = mw.Close()
		_ = pw.Close()
	}()

	req, err := http.NewRequest("POST", srv.URL+"/api/v1/workspaces/t/ws/files", pr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("slow upload failed (read deadline not extended?): %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	got, err := os.ReadFile(filepath.Join(root, "t", "ws", "slow.bin"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "chunkchunkchunkchunkchunk" {
		t.Errorf("contents = %q", got)
	}
}
