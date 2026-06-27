package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/mcp/server"
)

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
