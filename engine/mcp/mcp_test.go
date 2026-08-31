// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/mcp"
	"github.com/dazyflow/dazyflow/engine/mcp/mcptest"
)

// covPair wires a FakeServer into a Catalog over io.Pipe and returns the
// catalog plus the named tool's transport. It mirrors registerInProcess
// in transport_test.go but is self-contained so this file doesn't depend
// on that helper's signature.
func covRegister(t *testing.T, server string, srv *mcptest.FakeServer) *mcp.Catalog {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	go srv.Serve(serverR, serverW)

	client := mcp.NewClient(clientW, clientR)
	info, err := client.Initialize(t.Context(), "cov", "1.0")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cat := mcp.NewCatalog()
	closer := func() error {
		_ = clientW.Close()
		_ = serverW.Close()
		return nil
	}
	if err := cat.RegisterStream(server, client, info.ServerInfo, tools, closer); err != nil {
		t.Fatalf("RegisterStream: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	return cat
}

func echoArgsServer(seen *map[string]any) *mcptest.FakeServer {
	return &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "noop"}},
		Handler: func(_ string, args map[string]any) mcp.ToolCallResult {
			*seen = args
			return mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "text", Text: "ok"}}}
		},
	}
}

// TestTransport_Manifest covers Transport.Manifest(), the trivial getter
// returning the synthesized manifest.
func TestTransport_Manifest(t *testing.T) {
	cat := covRegister(t, "srv", &mcptest.FakeServer{Tools: []mcp.Tool{{Name: "t1", Description: "d"}}})
	tr, ok := cat.Get("", "mcp:srv:t1")
	if !ok {
		t.Fatal("transport not registered")
	}
	m := tr.Manifest()
	if m.ID != "mcp:srv:t1" {
		t.Errorf("manifest ID = %q", m.ID)
	}
	if m.Idempotent {
		t.Error("MCP tools should default to non-idempotent")
	}
}

// TestTransport_InputPort_Variants drives inlineToObject through its
// branches (nil, map, []byte JSON, and a struct via the marshal default).
func TestTransport_InputPort_Variants(t *testing.T) {
	type payload struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	cases := []struct {
		name   string
		inline any
		check  func(t *testing.T, seen map[string]any)
	}{
		{
			name:   "nil inline leaves params untouched",
			inline: nil,
			check: func(t *testing.T, seen map[string]any) {
				if seen["p"] != "param" {
					t.Errorf("p = %v, want param", seen["p"])
				}
			},
		},
		{
			name:   "byte-slice JSON object overlays",
			inline: []byte(`{"a":"frombytes"}`),
			check: func(t *testing.T, seen map[string]any) {
				if seen["a"] != "frombytes" {
					t.Errorf("a = %v, want frombytes", seen["a"])
				}
			},
		},
		{
			name:   "struct via marshal default branch",
			inline: payload{A: "s", B: 9},
			check: func(t *testing.T, seen map[string]any) {
				if seen["a"] != "s" {
					t.Errorf("a = %v, want s", seen["a"])
				}
				if seen["b"].(float64) != 9 {
					t.Errorf("b = %v, want 9", seen["b"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen map[string]any
			cat := covRegister(t, "demo", echoArgsServer(&seen))
			tr, _ := cat.Get("", "mcp:demo:noop")
			job := core.Job{
				ID:     "j1",
				Params: map[string]any{"p": "param"},
				Input:  map[string]core.Ref{"input": {Inline: tc.inline}},
			}
			res, err := tr.Execute(t.Context(), job, nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if res.Status != core.StatusOK {
				t.Fatalf("status=%q err=%+v", res.Status, res.Error)
			}
			tc.check(t, seen)
		})
	}
}

// TestTransport_BadInputBecomesBadInputError covers buildArguments'
// error path: a string input port that isn't a JSON object and a value
// that can't be coerced both surface a bad_input node error (no err).
func TestTransport_BadInputBecomesBadInputError(t *testing.T) {
	cases := []struct {
		name   string
		inline any
	}{
		{"string that is not JSON", "this is not json"},
		{"bytes that are not a JSON object", []byte("not json either")},
		{"string JSON array not object", "[1,2,3]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen map[string]any
			cat := covRegister(t, "demo", echoArgsServer(&seen))
			tr, _ := cat.Get("", "mcp:demo:noop")
			job := core.Job{
				ID:    "j1",
				Input: map[string]core.Ref{"input": {Inline: tc.inline}},
			}
			res, err := tr.Execute(t.Context(), job, nil)
			if err != nil {
				t.Fatalf("Execute should not return a transport err: %v", err)
			}
			if res.Status != core.StatusError {
				t.Fatalf("status=%q, want error", res.Status)
			}
			if res.Error == nil || res.Error.Code != "bad_input" {
				t.Errorf("error = %+v, want code bad_input", res.Error)
			}
		})
	}
}

// TestTransport_EmptyContentEmptyText covers contentToOutput's zero-item
// branch: an empty content array becomes an empty text/plain output.
func TestTransport_EmptyContentEmptyText(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "empty"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{Content: nil}
		},
	}
	cat := covRegister(t, "demo", srv)
	tr, _ := cat.Get("", "mcp:demo:empty")
	res, err := tr.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
	out := res.Output["out"]
	if out.Inline != "" {
		t.Errorf("inline = %v, want empty string", out.Inline)
	}
	if out.MIME != "text/plain" {
		t.Errorf("mime = %q, want text/plain", out.MIME)
	}
}

// TestTransport_SingleNonTextContentIsJSON covers contentToOutput's
// single non-text branch: one image item becomes an application/json
// ContentItem rather than a plain string.
func TestTransport_SingleNonTextContentIsJSON(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "img"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "image", Data: "b64", MIMEType: "image/png"}}}
		},
	}
	cat := covRegister(t, "demo", srv)
	tr, _ := cat.Get("", "mcp:demo:img")
	res, err := tr.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output["out"].MIME != "application/json" {
		t.Errorf("mime = %q, want application/json", res.Output["out"].MIME)
	}
	item, ok := res.Output["out"].Inline.(mcp.ContentItem)
	if !ok {
		t.Fatalf("inline is %T, want mcp.ContentItem", res.Output["out"].Inline)
	}
	if item.Type != "image" {
		t.Errorf("type = %q", item.Type)
	}
}

// TestTransport_ToolErrorNonTextSummary covers contentSummary's fallback
// branch: an error result whose first content item is NOT text yields the
// generic "tool reported error" message.
func TestTransport_ToolErrorNonTextSummary(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "errimg"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "image", Data: "x"}},
				IsError: true,
			}
		},
	}
	cat := covRegister(t, "demo", srv)
	tr, _ := cat.Get("", "mcp:demo:errimg")
	res, err := tr.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status=%q", res.Status)
	}
	if res.Error == nil || res.Error.Message != "tool reported error" {
		t.Errorf("error = %+v, want generic summary", res.Error)
	}
}

// TestTransport_ToolErrorEmptyContentSummary covers contentSummary with a
// zero-length content slice (the len>0 guard is false).
func TestTransport_ToolErrorEmptyContentSummary(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "erre"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{IsError: true}
		},
	}
	cat := covRegister(t, "demo", srv)
	tr, _ := cat.Get("", "mcp:demo:erre")
	res, _ := tr.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if res.Error == nil || res.Error.Message != "tool reported error" {
		t.Errorf("error = %+v, want generic summary", res.Error)
	}
}

// TestTransport_CallFailsWhenConnectionClosed covers Execute's
// mcp_call branch: when the underlying client connection is gone, the
// CallTool returns an error and Execute reports an mcp_call node error
// (and returns the error too).
func TestTransport_CallFailsWhenConnectionClosed(t *testing.T) {
	var seen map[string]any
	cat := covRegister(t, "demo", echoArgsServer(&seen))
	tr, _ := cat.Get("", "mcp:demo:noop")

	// Tear the connection down before calling.
	_ = cat.Close()

	res, err := tr.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err == nil {
		t.Fatal("expected an error after the connection was closed")
	}
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
	if res.Error == nil || res.Error.Code != "mcp_call" {
		t.Errorf("error = %+v, want code mcp_call", res.Error)
	}
}

// TestClient_NotifyWriteError covers Notify's write-error return: a
// writer that always fails should propagate the error.
func TestClient_NotifyWriteError(t *testing.T) {
	r, _ := io.Pipe()
	client := mcp.NewClient(failWriter{}, r)
	if err := client.Notify("notifications/initialized", nil); err == nil {
		t.Error("Notify should return the writer error")
	}
}

// TestClient_CallWriteError covers Call's write-error path: the request
// marshals fine but the write fails, so Call returns a write error and
// discards the pending entry.
func TestClient_CallWriteError(t *testing.T) {
	r, _ := io.Pipe()
	client := mcp.NewClient(failWriter{}, r)
	err := client.Call(t.Context(), "tools/list", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Errorf("want a write error, got %v", err)
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

// --- RegisterStdio (real subprocess) -------------------------------------

// TestRegisterStdio_ValidationErrors covers the two guard branches of
// RegisterStdio that reject an empty Name or empty Command without ever
// spawning a process.
func TestRegisterStdio_ValidationErrors(t *testing.T) {
	cat := mcp.NewCatalog()
	defer cat.Close()

	if err := cat.RegisterStdio(mcp.StdioDescriptor{}); err == nil {
		t.Error("empty Name should error")
	}
	if err := cat.RegisterStdio(mcp.StdioDescriptor{Name: "x"}); err == nil {
		t.Error("empty Command should error")
	}
}

// TestRegisterStdio_StartFailure covers the start-failure branch: a
// command that does not exist on PATH fails at cmd.Start().
func TestRegisterStdio_StartFailure(t *testing.T) {
	cat := mcp.NewCatalog()
	defer cat.Close()
	err := cat.RegisterStdio(mcp.StdioDescriptor{
		Name:    "ghost",
		Command: "definitely-not-a-real-binary-xyz",
	})
	if err == nil {
		t.Error("a missing command should fail to start")
	}
}

// TestRegisterStdio_HandshakeTimeout covers the initialize-failure branch
// of RegisterStdio (and killSubprocess): `cat` is a real process that
// never speaks MCP, so the handshake times out and the subprocess is
// killed.
func TestRegisterStdio_HandshakeTimeout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	cat := mcp.NewCatalog()
	cat.HandshakeTimeout = 300_000_000 // 300ms
	defer cat.Close()

	// This shell drains stdin into /dev/null but never writes a JSON-RPC
	// response, so initialize blocks until the handshake context times
	// out. (Plain `cat` would echo the request back and the client would
	// mistake it for a reply.)
	err := cat.RegisterStdio(mcp.StdioDescriptor{
		Name:    "muteserver",
		Command: "sh",
		Args:    []string{"-c", "cat >/dev/null"},
	})
	if err == nil {
		t.Fatal("handshake against a non-MCP process should time out")
	}
	if !strings.Contains(err.Error(), "initialize") {
		t.Errorf("err = %v, want an initialize failure", err)
	}
}

// TestRegisterStdio_RealServerRoundTrip builds the example MCP server,
// registers it as a real subprocess, calls a tool, then closes — covering
// RegisterStdio's success path, the subprocess closer, and the graceful
// stdin-close shutdown.
func TestRegisterStdio_RealServerRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a subprocess; skipped in -short")
	}
	bin := buildExampleServer(t)

	cat := mcp.NewCatalog()
	if err := cat.RegisterStdio(mcp.StdioDescriptor{
		Name:    "ap",
		Command: bin,
		Env:     map[string]string{"DAZY_TEST": "1"},
	}); err != nil {
		t.Fatalf("RegisterStdio: %v", err)
	}

	man := cat.Manifests()
	if _, ok := man["mcp:ap:lookup_user"]; !ok {
		t.Fatalf("expected tool mcp:ap:lookup_user in %v", keys(man))
	}

	tr, ok := cat.Get("", "mcp:ap:categorize")
	if !ok {
		t.Fatal("categorize transport missing")
	}
	res, err := tr.Execute(t.Context(), core.Job{
		ID:     "j1",
		Params: map[string]any{"text": "this is URGENT please"},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, _ := res.Output["out"].Inline.(string); got != "urgent" {
		t.Errorf("categorize = %q, want urgent", got)
	}

	// Close triggers the graceful subprocess shutdown closer.
	if err := cat.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Idempotent: a second Close is a no-op.
	if err := cat.Close(); err != nil {
		t.Errorf("second Close should be a no-op, got %v", err)
	}
}

// TestRegisterStdio_DuplicateServerName covers the duplicate-name guard
// inside RegisterStdio (after a successful spawn) which kills the second
// subprocess.
func TestRegisterStdio_DuplicateServerName(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a subprocess; skipped in -short")
	}
	bin := buildExampleServer(t)
	cat := mcp.NewCatalog()
	defer cat.Close()

	if err := cat.RegisterStdio(mcp.StdioDescriptor{Name: "dup", Command: bin}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := cat.RegisterStdio(mcp.StdioDescriptor{Name: "dup", Command: bin})
	if err == nil {
		t.Error("registering the same server name twice should fail")
	}
}

func buildExampleServer(t *testing.T) string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Skipf("cannot locate repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "ap-demo-server")
	cmd := exec.Command("go", "build", "-o", bin, "./tests/e2e/mcp-pipeline/server")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go build example server failed: %v\n%s", err, out)
	}
	return bin
}

// repoRoot walks up from the cwd looking for go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", context.DeadlineExceeded
		}
		dir = parent
	}
}

func keys(m map[string]core.Manifest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
