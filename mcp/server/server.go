// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server implements an MCP (Model Context Protocol) server
// that exposes Dazyflow's pipeline-management operations as tools. It
// is the server-side counterpart to engine/mcp (which is the client we
// use to consume external MCP servers as flow nodes).
//
// Transport is stdio + JSON-RPC 2.0 — newline-delimited JSON on
// stdin/stdout. Stderr is reserved for logging so the protocol stream
// stays clean. The framing is intentionally minimal: a single read
// goroutine in Server.Serve, synchronous handler dispatch, and
// non-blocking writes. HTTP+SSE would be a future addition with the
// same Handler/Registry pair underneath.
//
// Spec: https://spec.modelcontextprotocol.io/ — we target the
// 2024-11-05 protocol version, matching engine/mcp's client.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
)

// ProtocolVersion matches engine/mcp.protocolVersion. Bump both
// together when we move the floor.
const ProtocolVersion = "2024-11-05"

// JSON-RPC 2.0 error codes used by this server. The standard codes
// (-32700 .. -32600) are reserved by JSON-RPC; we lean on -32602
// (invalid params) for argument validation and -32603 (internal
// error) for upstream-call failures. Tool-level failures are
// returned via ToolCallResult.IsError, NOT as RPC errors — the spec
// distinguishes "tool ran and failed" from "tool couldn't even be
// invoked."
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// RPCError is the wire shape of a JSON-RPC error response. Errors
// surface here only for hard failures (bad framing, missing method,
// invalid args). Tool-level errors travel inside ToolCallResult.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Tool is one capability the server advertises. Name is the wire
// identifier (snake_case by convention); Description is what shows up
// in the LLM client's tool picker; InputSchema is a JSON Schema
// describing the arguments. Handler is called when the client invokes
// the tool.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     func(ctx context.Context, args json.RawMessage) (ToolCallResult, error)
}

// ContentItem mirrors the MCP content-array element. We only emit
// text today; image/resource entries are valid wire shapes and easy
// to add later.
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// ToolCallResult is what a tool returns to the client. IsError=true
// signals "the tool ran and the operation failed" — distinct from a
// JSON-RPC error, which means the tool couldn't be invoked at all.
type ToolCallResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// TextResult is a convenience for the common "tool returns a JSON
// blob" case — marshal the payload once and wrap it.
func TextResult(payload any) ToolCallResult {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ToolCallResult{
			IsError: true,
			Content: []ContentItem{{Type: "text", Text: "marshal failed: " + err.Error()}},
		}
	}
	return ToolCallResult{Content: []ContentItem{{Type: "text", Text: string(b)}}}
}

// ErrorResult wraps a server-side operation failure as a tool-error
// result. The text becomes the LLM-visible explanation, so keep it
// actionable ("workspace path must be set" beats "validation
// failed").
func ErrorResult(msg string) ToolCallResult {
	return ToolCallResult{
		IsError: true,
		Content: []ContentItem{{Type: "text", Text: msg}},
	}
}

// Server hosts a Tool registry and serves it over a single
// reader/writer pair. One Server per process is the norm under
// stdio; for HTTP+SSE you'd build a Server per connection.
type Server struct {
	Name    string
	Version string
	Logger  *log.Logger

	mu          sync.Mutex
	tools       []Tool
	toolsByName map[string]Tool

	wm          sync.Mutex
	initialized bool
}

// Register adds a tool to the server. Must be called before Serve;
// after Serve starts the reader goroutine the registry is treated as
// immutable so handlers don't have to lock.
func (s *Server) Register(t Tool) {
	if s.toolsByName == nil {
		s.toolsByName = make(map[string]Tool)
	}
	if _, dup := s.toolsByName[t.Name]; dup {
		panic("duplicate MCP tool: " + t.Name)
	}
	s.tools = append(s.tools, t)
	s.toolsByName[t.Name] = t
}

// Serve runs the request/response loop until r returns EOF or ctx is
// cancelled. Reads one JSON-RPC message per line on r, writes one
// response per line on w. Logging — including the *errors* parameter
// in the spec — goes through s.Logger to stderr, never the protocol
// stream.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if s.Logger == nil {
		s.Logger = log.New(io.Discard, "", 0)
	}
	scanner := bufio.NewScanner(r)
	// MCP messages can be larger than the default 64KB scanner limit
	// (a flow with many nodes easily exceeds it). 4MB matches the
	// hard cap most stdio implementations use.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		s.handle(ctx, line, w)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) handle(ctx context.Context, line []byte, w io.Writer) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(w, nil, codeParseError, "parse error: "+err.Error())
		return
	}
	// Notifications carry no id; we acknowledge but don't respond.
	if len(req.ID) == 0 {
		s.handleNotification(req)
		return
	}
	if req.JSONRPC != "2.0" {
		s.writeError(w, req.ID, codeInvalidRequest, `jsonrpc must be "2.0"`)
		return
	}
	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(ctx, w, req)
	case "ping":
		// MCP ping is a liveness probe. Empty result is the right
		// answer per spec.
		s.writeResult(w, req.ID, struct{}{})
	default:
		s.writeError(w, req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) handleNotification(req request) {
	switch req.Method {
	case "notifications/initialized":
		s.mu.Lock()
		s.initialized = true
		s.mu.Unlock()
	default:
		// Other notifications (progress, cancelled) are valid wire
		// shapes; we don't act on them yet.
	}
}

func (s *Server) handleInitialize(w io.Writer, req request) {
	// We advertise only the tools capability. Resources, prompts,
	// and sampling can be added when we have a concrete use case.
	result := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{
				// We don't support list-change notifications yet;
				// clients re-fetch tools/list on demand.
				"listChanged": false,
			},
		},
		"serverInfo": map[string]any{
			"name":    s.Name,
			"version": s.Version,
		},
	}
	s.writeResult(w, req.ID, result)
}

func (s *Server) handleToolsList(w io.Writer, req request) {
	wire := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		entry := map[string]any{
			"name": t.Name,
		}
		if t.Description != "" {
			entry["description"] = t.Description
		}
		if len(t.InputSchema) > 0 {
			entry["inputSchema"] = json.RawMessage(t.InputSchema)
		} else {
			// MCP requires an inputSchema field; an empty object is
			// the right shape for tools that take no arguments.
			entry["inputSchema"] = map[string]any{"type": "object"}
		}
		wire = append(wire, entry)
	}
	s.writeResult(w, req.ID, map[string]any{"tools": wire})
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, w io.Writer, req request) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(w, req.ID, codeInvalidParams, "decode params: "+err.Error())
		return
	}
	t, ok := s.toolsByName[p.Name]
	if !ok {
		s.writeError(w, req.ID, codeMethodNotFound, "no such tool: "+p.Name)
		return
	}
	res, err := t.Handler(ctx, p.Arguments)
	if err != nil {
		// Internal failure — the handler couldn't even attempt the
		// operation (e.g. bad credentials, daemon unreachable).
		// Surface as a JSON-RPC error so the client distinguishes
		// "infra broken" from "operation reported error".
		s.writeError(w, req.ID, codeInternalError, err.Error())
		return
	}
	s.writeResult(w, req.ID, res)
}

func (s *Server) writeResult(w io.Writer, id json.RawMessage, result any) {
	s.writeMessage(w, response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(w io.Writer, id json.RawMessage, code int, msg string) {
	// Errors without an id can still be reported (per JSON-RPC spec,
	// id null is allowed for parse errors). We send a literal
	// null id to match what every other server I've inspected does.
	if len(id) == 0 {
		id = json.RawMessage(`null`)
	}
	s.writeMessage(w, response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	})
}

func (s *Server) writeMessage(w io.Writer, msg response) {
	b, err := json.Marshal(msg)
	if err != nil {
		s.Logger.Printf("marshal response: %v", err)
		return
	}
	b = append(b, '\n')
	s.wm.Lock()
	defer s.wm.Unlock()
	if _, err := w.Write(b); err != nil {
		s.Logger.Printf("write response: %v", err)
	}
}
