// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"git.sr.ht/~klahr/dazyflow/engine/mcp/mcptest"
)

// inProcessPair wires a FakeServer to a Client via two io.Pipes,
// returning the client and a cancel function that tears everything down.
func inProcessPair(t *testing.T, server *mcptest.FakeServer) (*mcp.Client, func()) {
	t.Helper()

	clientReadFromServer, serverWritesToClient := io.Pipe()
	serverReadsFromClient, clientWritesToServer := io.Pipe()

	go server.Serve(serverReadsFromClient, serverWritesToClient)
	client := mcp.NewClient(clientWritesToServer, clientReadFromServer)

	return client, func() {
		_ = clientWritesToServer.Close()
		_ = serverWritesToClient.Close()
	}
}

func TestClient_InitializeRoundTrip(t *testing.T) {
	srv := &mcptest.FakeServer{Name: "test-srv", Version: "9.9"}
	client, close := inProcessPair(t, srv)
	defer close()

	info, err := client.Initialize(t.Context(), "dazyflow", "1.0")
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.ServerInfo.Name != "test-srv" {
		t.Errorf("ServerInfo.Name = %q, want test-srv", info.ServerInfo.Name)
	}
}

func TestClient_ListTools(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{
			{Name: "echo", Description: "echoes input", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "shout"},
		},
	}
	client, close := inProcessPair(t, srv)
	defer close()

	_, _ = client.Initialize(t.Context(), "test", "1.0")
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("first tool = %q, want echo", tools[0].Name)
	}
}

func TestClient_CallToolWithEchoHandler(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "echo"}},
		Handler: func(name string, args map[string]any) mcp.ToolCallResult {
			text, _ := args["msg"].(string)
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: "echo: " + text}},
			}
		},
	}
	client, close := inProcessPair(t, srv)
	defer close()
	_, _ = client.Initialize(t.Context(), "test", "1.0")

	result, err := client.CallTool(t.Context(), "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "echo: hello" {
		t.Errorf("content = %+v", result.Content)
	}
	if result.IsError {
		t.Error("IsError should be false")
	}
}

func TestClient_ToolErrorResult(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "fail"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: "ratelimited"}},
				IsError: true,
			}
		},
	}
	client, close := inProcessPair(t, srv)
	defer close()
	_, _ = client.Initialize(t.Context(), "test", "1.0")

	result, err := client.CallTool(t.Context(), "fail", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Error("IsError should be true")
	}
	if result.Content[0].Text != "ratelimited" {
		t.Errorf("content = %+v", result.Content)
	}
}

func TestClient_UnknownMethodReturnsRPCError(t *testing.T) {
	// FakeServer responds with JSON-RPC error -32601 for unknown methods.
	srv := &mcptest.FakeServer{}
	client, close := inProcessPair(t, srv)
	defer close()

	err := client.Call(t.Context(), "prompts/list", nil, nil) // not supported
	if err == nil {
		t.Fatal("expected RPC error")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("err = %v", err)
	}
}

func TestClient_ContextCancelDuringCall(t *testing.T) {
	// Server pipe is read-only from the client's perspective (server
	// never replies). Write to io.Discard so the request doesn't block.
	r, _ := io.Pipe()
	client := mcp.NewClient(io.Discard, r)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel immediately

	err := client.Call(ctx, "anything", nil, nil)
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %v; expected context cancellation", err)
	}
}

func TestClient_ConnectionClosedFailsPending(t *testing.T) {
	// Reader closes while a call is in flight → the call must return
	// with an error rather than hanging.
	r, rw := io.Pipe()
	client := mcp.NewClient(io.Discard, r)

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Call(t.Context(), "anything", nil, nil)
	}()

	// Close the server's writer end so the client's read returns EOF
	// and the reader goroutine fails all pending calls.
	_ = rw.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-t.Context().Done():
		t.Fatal("call did not return after connection closed")
	}
}
