// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/mcp/server"
)

// runServer wires Server.Serve over an in-memory pipe pair so tests
// can drive it like a real client. Each test ships a full
// initialize → request → response loop; the goroutine exits when
// the test closes the client-side writer.
func runServer(t *testing.T, s *server.Server, body string) string {
	t.Helper()
	in := strings.NewReader(body)
	out := &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := s.Serve(ctx, in, out); err != nil && err != io.EOF {
		t.Fatalf("Serve: %v", err)
	}
	return out.String()
}

func TestServer_Initialize(t *testing.T) {
	s := &server.Server{Name: "test", Version: "1.0"}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}` + "\n"

	out := runServer(t, s, body)
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode response: %v\nbody=%q", err, out)
	}
	if resp.ID != 1 {
		t.Errorf("id = %d, want 1", resp.ID)
	}
	if resp.Result.ProtocolVersion != server.ProtocolVersion {
		t.Errorf("protocolVersion = %q, want %q", resp.Result.ProtocolVersion, server.ProtocolVersion)
	}
	if resp.Result.ServerInfo.Name != "test" {
		t.Errorf("server name = %q, want test", resp.Result.ServerInfo.Name)
	}
}

// tools/list must return every registered tool with its inputSchema
// — clients pre-validate args against the schema before calling, so
// an empty/missing schema means the LLM can't safely invoke the
// tool.
func TestServer_ToolsList(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	s.Register(server.Tool{
		Name:        "echo",
		Description: "echoes its input",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		Handler: func(_ context.Context, args json.RawMessage) (server.ToolCallResult, error) {
			return server.TextResult(map[string]any{"got": string(args)}), nil
		},
	})

	out := runServer(t, s,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n",
	)
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v\nbody=%q", err, out)
	}
	if len(resp.Result.Tools) != 1 || resp.Result.Tools[0].Name != "echo" {
		t.Errorf("tools = %+v", resp.Result.Tools)
	}
	if resp.Result.Tools[0].InputSchema == nil {
		t.Error("inputSchema missing — clients can't validate args without it")
	}
}

// tools/call dispatches to the registered handler and surfaces its
// result verbatim. A handler that returns an error (not a tool-error
// result) should travel as a JSON-RPC error, because the spec
// distinguishes "tool couldn't be invoked" from "tool reported a
// failure to the user."
func TestServer_ToolsCall_HandlerResult(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	s.Register(server.Tool{
		Name:        "ping",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(_ context.Context, _ json.RawMessage) (server.ToolCallResult, error) {
			return server.TextResult(map[string]any{"pong": true}), nil
		},
	})
	out := runServer(t, s,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ping","arguments":{}}}`+"\n",
	)
	var resp struct {
		Result server.ToolCallResult `json:"result"`
		Error  *server.RPCError      `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v\nbody=%q", err, out)
	}
	if resp.Error != nil {
		t.Fatalf("rpc error = %v", resp.Error)
	}
	if resp.Result.IsError {
		t.Error("result.isError true")
	}
	if !strings.Contains(resp.Result.Content[0].Text, `"pong": true`) {
		t.Errorf("content = %q", resp.Result.Content[0].Text)
	}
}

func TestServer_ToolsCall_UnknownTool(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`+"\n",
	)
	if !strings.Contains(out, "no such tool") {
		t.Errorf("expected method-not-found, got %q", out)
	}
}

// Initialized notifications carry no id and must produce no
// response. A response to a notification would corrupt the client's
// request/response correlation.
func TestServer_NotificationProducesNoResponse(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n",
	)
	if out != "" {
		t.Errorf("server replied to notification: %q", out)
	}
}
