// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"git.sr.ht/~klahr/dazyflow/engine/mcp/mcptest"
)

// registerInProcess wires a FakeServer into a fresh Catalog using
// RegisterStream — no subprocess, just io.Pipe.
func registerInProcess(t *testing.T, serverName string, srv *mcptest.FakeServer) *mcp.Catalog {
	t.Helper()
	clientReadFromServer, serverWritesToClient := io.Pipe()
	serverReadsFromClient, clientWritesToServer := io.Pipe()
	go srv.Serve(serverReadsFromClient, serverWritesToClient)

	client := mcp.NewClient(clientWritesToServer, clientReadFromServer)
	info, err := client.Initialize(t.Context(), "dazyflow-test", "1.0")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	cat := mcp.NewCatalog()
	closer := func() error {
		_ = clientWritesToServer.Close()
		_ = serverWritesToClient.Close()
		return nil
	}
	if err := cat.RegisterStream(serverName, client, info.ServerInfo, tools, closer); err != nil {
		t.Fatalf("RegisterStream: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	return cat
}

func TestCatalog_SynthesizesManifestPerTool(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{
			{Name: "read_file", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
			{Name: "list_dir"},
		},
	}
	cat := registerInProcess(t, "fs", srv)

	manifests := cat.Manifests()
	if len(manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(manifests))
	}
	rf, ok := manifests["mcp:fs:read_file"]
	if !ok {
		t.Fatal("missing manifest mcp:fs:read_file")
	}
	if rf.Label != "fs — read_file" {
		t.Errorf("Label = %q", rf.Label)
	}
	if rf.Idempotent {
		t.Error("MCP tools default to non-idempotent")
	}
	if !strings.Contains(string(rf.ParamsSchema), `"path"`) {
		t.Errorf("ParamsSchema = %q; expected to carry tool's inputSchema", rf.ParamsSchema)
	}
}

func TestTransport_ExecuteEchoesToolResult(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "echo"}},
		Handler: func(name string, args map[string]any) mcp.ToolCallResult {
			text, _ := args["msg"].(string)
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: "you said: " + text}},
			}
		},
	}
	cat := registerInProcess(t, "demo", srv)

	transport, ok := cat.Get("", "mcp:demo:echo")
	if !ok {
		t.Fatal("transport not registered")
	}
	job := core.Job{
		ID:     "j1",
		Params: map[string]any{"msg": "hi"},
	}
	res, err := transport.Execute(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	got, _ := res.Output["out"].Inline.(string)
	if got != "you said: hi" {
		t.Errorf("out = %q", got)
	}
}

func TestTransport_InputPortMergesIntoArguments(t *testing.T) {
	// The "input" port lets one node's output feed another's tool args
	// without hardcoding them in the graph. Verify the merge happens.
	var seen map[string]any
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "noop"}},
		Handler: func(_ string, args map[string]any) mcp.ToolCallResult {
			seen = args
			return mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "text", Text: "ok"}}}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:noop")

	job := core.Job{
		ID:     "j1",
		Params: map[string]any{"channel": "#general", "tag": "from-params"},
		Input: map[string]core.Ref{
			"input": {Inline: map[string]any{"text": "from-input", "tag": "from-input"}},
		},
	}
	_, err := transport.Execute(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen["channel"] != "#general" {
		t.Errorf("channel = %v, want #general (from params)", seen["channel"])
	}
	if seen["text"] != "from-input" {
		t.Errorf("text = %v, want from-input (from input port)", seen["text"])
	}
	if seen["tag"] != "from-input" {
		t.Errorf("tag = %v, want from-input (input port overrides params)", seen["tag"])
	}
}

func TestTransport_InputPortAcceptsJSONString(t *testing.T) {
	// http_request emits its response_body as a string when MIME is text/json.
	// branch can route it; if the recipient is an MCP tool, the transport
	// has to JSON-decode it to extract arguments.
	var seen map[string]any
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "noop"}},
		Handler: func(_ string, args map[string]any) mcp.ToolCallResult {
			seen = args
			return mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "text", Text: "ok"}}}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:noop")

	job := core.Job{
		ID: "j1",
		Input: map[string]core.Ref{
			"input": {MIME: "application/json", Inline: `{"k":"v","n":42}`},
		},
	}
	_, err := transport.Execute(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen["k"] != "v" {
		t.Errorf("k = %v", seen["k"])
	}
	if seen["n"].(float64) != 42 {
		t.Errorf("n = %v", seen["n"])
	}
}

func TestTransport_ToolErrorBecomesNodeFailure(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "bad"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: "rate-limited"}},
				IsError: true,
			}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:bad")

	res, err := transport.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status=%q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "mcp_tool_error" {
		t.Errorf("error = %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "rate-limited") {
		t.Errorf("error message = %q", res.Error.Message)
	}
}

func TestTransport_MultiPartContentReturnsArray(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "multi"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{
					{Type: "text", Text: "preamble"},
					{Type: "image", Data: "base64png", MIMEType: "image/png"},
				},
			}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:multi")

	res, err := transport.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
	arr, ok := res.Output["out"].Inline.([]mcp.ContentItem)
	if !ok {
		t.Fatalf("Inline is %T, want []mcp.ContentItem", res.Output["out"].Inline)
	}
	if len(arr) != 2 {
		t.Errorf("got %d items, want 2", len(arr))
	}
	if res.Output["out"].MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json for multi-part", res.Output["out"].MIME)
	}
}

func TestCatalog_GetReturnsFalseForUnknown(t *testing.T) {
	cat := registerInProcess(t, "any", &mcptest.FakeServer{})
	if _, ok := cat.Get("", "mcp:any:nope"); ok {
		t.Error("Get should return false for unknown tool")
	}
}

func TestCatalog_RejectsDuplicateServerName(t *testing.T) {
	cat := mcp.NewCatalog()
	defer cat.Close()

	for i, name := range []string{"same-name", "same-name"} {
		clientR, serverW := io.Pipe()
		serverR, clientW := io.Pipe()
		srv := &mcptest.FakeServer{}
		go srv.Serve(serverR, serverW)
		client := mcp.NewClient(clientW, clientR)
		info, _ := client.Initialize(context.Background(), "x", "1")
		tools, _ := client.ListTools(context.Background())
		err := cat.RegisterStream(name, client, info.ServerInfo, tools, func() error {
			_ = clientW.Close()
			_ = serverW.Close()
			return nil
		})
		if i == 0 && err != nil {
			t.Fatalf("first register: %v", err)
		}
		if i == 1 && err == nil {
			t.Errorf("second register of %q should fail", name)
		}
	}
}
