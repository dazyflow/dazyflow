// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/mcp/server"
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

// TestServer_Ping covers the ping liveness method.
func TestServer_Ping(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s, `{"jsonrpc":"2.0","id":7,"method":"ping"}`+"\n")
	if !strings.Contains(out, `"result"`) {
		t.Errorf("ping result = %q", out)
	}
}

// TestServer_BadJSONRPCVersion covers the jsonrpc!="2.0" guard.
func TestServer_BadJSONRPCVersion(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s, `{"jsonrpc":"1.0","id":1,"method":"ping"}`+"\n")
	if !strings.Contains(out, "-32600") || !strings.Contains(out, "jsonrpc must be") {
		t.Errorf("out = %q", out)
	}
}

// TestServer_ParseError covers the malformed-line branch in handle.
func TestServer_ParseError(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s, "not json at all\n")
	if !strings.Contains(out, "parse error") {
		t.Errorf("out = %q", out)
	}
}

// TestServer_UnknownMethod covers the default method-not-found arm.
func TestServer_UnknownMethod(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s, `{"jsonrpc":"2.0","id":2,"method":"frobnicate"}`+"\n")
	if !strings.Contains(out, "method not found") {
		t.Errorf("out = %q", out)
	}
}

// TestServer_ToolsCall_BadParams covers the params-decode failure arm.
func TestServer_ToolsCall_BadParams(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":[1,2]}`+"\n")
	if !strings.Contains(out, "decode params") {
		t.Errorf("out = %q", out)
	}
}

// TestServer_ToolsCall_HandlerError covers the handler-returns-error
// path that surfaces as a JSON-RPC internal error.
func TestServer_ToolsCall_HandlerError(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	s.Register(server.Tool{
		Name: "boom",
		Handler: func(_ context.Context, _ json.RawMessage) (server.ToolCallResult, error) {
			return server.ToolCallResult{}, errors.New("infra down")
		},
	})
	out := runServer(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"boom","arguments":{}}}`+"\n")
	if !strings.Contains(out, "infra down") {
		t.Errorf("out = %q", out)
	}
	var resp struct {
		Error *server.RPCError `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Errorf("error = %+v, want internal -32603", resp.Error)
	}
}

// TestServer_BlankLineSkipped covers the empty-line continue in Serve.
func TestServer_BlankLineSkipped(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	out := runServer(t, s, "\n"+`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n")
	if !strings.Contains(out, `"result"`) {
		t.Errorf("out = %q", out)
	}
}

// TestServer_ToolsListEmptySchema covers the empty-inputSchema fallback
// branch in handleToolsList (a tool registered with no schema).
func TestServer_ToolsListEmptySchema(t *testing.T) {
	s := &server.Server{Name: "t", Version: "1"}
	s.Register(server.Tool{Name: "noschema"})
	out := runServer(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
	if !strings.Contains(out, `"inputSchema"`) || !strings.Contains(out, `"type":"object"`) {
		t.Errorf("out = %q", out)
	}
}

// TestRPCError_Error covers the RPCError.Error string method.
func TestRPCError_Error(t *testing.T) {
	e := &server.RPCError{Code: -32603, Message: "boom"}
	if !strings.Contains(e.Error(), "-32603") || !strings.Contains(e.Error(), "boom") {
		t.Errorf("Error() = %q", e.Error())
	}
}
