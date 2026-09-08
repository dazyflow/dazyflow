// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcptest

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/dazyflow/dazyflow/engine/mcp"
)

type ToolHandler func(name string, args map[string]any) mcp.ToolCallResult

type FakeServer struct {
	Name    string
	Version string
	Tools   []mcp.Tool
	Handler ToolHandler
}

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
