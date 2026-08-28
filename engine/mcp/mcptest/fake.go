// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mcptest provides a minimal MCP server implementation used by
// unit tests (over io.Pipe) and the live demo (compiled as a real
// subprocess). It implements initialize, tools/list, and tools/call —
// enough to exercise the client end-to-end without external dependencies.
package mcptest

import (
	"bufio"
	"encoding/json"
	"io"

	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// ToolHandler runs server-side when the client calls a tool. Return a
// successful result, or an error result by setting IsError=true.
type ToolHandler func(name string, args map[string]any) mcp.ToolCallResult

// FakeServer is a deliberately small MCP server. Set Tools to declare
// what tools/list returns; Handler runs for each tools/call.
type FakeServer struct {
	Name    string
	Version string
	Tools   []mcp.Tool
	Handler ToolHandler
}

// Serve reads JSON-RPC requests from r line by line, dispatches each
// known method, and writes responses to w. Returns when r reaches EOF.
func (s *FakeServer) Serve(r io.Reader, w io.Writer) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(w)

	for scanner.Scan() {
		line := scanner.Bytes()
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.Method == "" {
			continue
		}
		// Notifications carry no id; skip them.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		s.handle(enc, req.ID, req.Method, req.Params)
	}
}

func (s *FakeServer) handle(enc *json.Encoder, id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "initialize":
		write(enc, id, mcp.InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    map[string]any{"tools": map[string]any{}},
			ServerInfo:      mcp.ServerInfo{Name: s.name(), Version: s.version()},
		})
	case "tools/list":
		write(enc, id, map[string]any{"tools": s.Tools})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(params, &p)
		var result mcp.ToolCallResult
		if s.Handler != nil {
			result = s.Handler(p.Name, p.Arguments)
		} else {
			result = mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "text", Text: "ok"}}}
		}
		write(enc, id, result)
	default:
		writeError(enc, id, -32601, "method not found: "+method)
	}
}

func write(enc *json.Encoder, id json.RawMessage, result any) {
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
}

func writeError(enc *json.Encoder, id json.RawMessage, code int, message string) {
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	})
}

func (s *FakeServer) name() string {
	if s.Name != "" {
		return s.Name
	}
	return "fake-mcp"
}

func (s *FakeServer) version() string {
	if s.Version != "" {
		return s.Version
	}
	return "0.0.1"
}
