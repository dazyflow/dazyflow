// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server exposes Dazyflow's pipeline-management operations as MCP tools,
// the counterpart to engine/mcp, which consumes external MCP servers as flow
// nodes.
//
// Transport is stdio plus JSON-RPC 2.0, newline-delimited. Stderr is reserved for
// logging so the protocol stream stays clean. HTTP+SSE would be a future addition
// over the same Handler/Registry pair.
//
// Targets the 2024-11-05 protocol version, matching engine/mcp's client.
// Spec: https://spec.modelcontextprotocol.io/
package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
)

const ProtocolVersion = "2024-11-05"

// Tool-level failures go through ToolCallResult.IsError, NOT these codes: the
// spec distinguishes "tool ran and failed" from "tool couldn't be invoked".
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

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

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     func(ctx context.Context, args json.RawMessage) (ToolCallResult, error)
}

// ContentItem mirrors the MCP content-array element. Only text is emitted today;
// image and resource entries are valid wire shapes too.
type ContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

type ToolCallResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

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

// ErrorResult's text becomes the LLM-visible explanation, so keep it actionable:
// "workspace path must be set" beats "validation failed".
func ErrorResult(msg string) ToolCallResult {
	return ToolCallResult{
		IsError: true,
		Content: []ContentItem{{Type: "text", Text: msg}},
	}
}

type Server struct {
	Name    string
	Version string
	Logger  *log.Logger

	tools       []Tool
	toolsByName map[string]Tool

	wm sync.Mutex
}

// Register must be called before Serve: once the reader goroutine starts, the
// registry is immutable so handlers need no lock.
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

// Serve reads one JSON-RPC message per line and writes one response per line,
// until EOF or ctx is cancelled. All logging goes to stderr through s.Logger,
// never the protocol stream.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if s.Logger == nil {
		s.Logger = log.New(io.Discard, "", 0)
	}
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, tooLong, err := readLine(br, maxMessageBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if tooLong {
			// One oversized message is the client's problem, not a reason to
			// drop the session: the rest of the line is discarded and the next
			// one is read as usual.
			s.writeError(w, nil, codeInvalidRequest,
				fmt.Sprintf("message exceeds %d bytes", maxMessageBytes))
			continue
		}
		if len(line) == 0 {
			continue
		}
		s.handle(ctx, line, w)
	}
}

// maxMessageBytes caps one newline-delimited JSON-RPC message.
const maxMessageBytes = 4 * 1024 * 1024

// readLine returns the next line without its terminator. A line longer than
// max is consumed to its end and reported as tooLong, with no content, so a
// single oversized message cannot end the stream or be held in memory.
func readLine(br *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	for {
		chunk, more, err := br.ReadLine()
		if err != nil {
			return nil, tooLong, err
		}
		if !tooLong {
			if len(line)+len(chunk) > max {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if !more {
			return line, tooLong, nil
		}
	}
}

func (s *Server) handle(ctx context.Context, line []byte, w io.Writer) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(w, nil, codeParseError, "parse error: "+err.Error())
		return
	}
	if len(req.ID) == 0 {
		// A notification. initialized, progress and cancelled are valid wire
		// shapes but none is acted on — tools are served whether or not the
		// client sent initialized — and a notification never gets a reply.
		return
	}
	// JSON-RPC allows a null id, but MCP forbids it — and a null id cannot
	// be told apart from the id of a parse error. Refused, never executed.
	if bytes.Equal(bytes.TrimSpace(req.ID), []byte("null")) {
		s.writeError(w, nil, codeInvalidRequest, "id must be a string or number, not null")
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
		s.writeResult(w, req.ID, struct{}{})
	default:
		s.writeError(w, req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *Server) handleInitialize(w io.Writer, req request) {
	result := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{
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
		s.writeError(w, req.ID, codeInternalError, err.Error())
		return
	}
	s.writeResult(w, req.ID, res)
}

func (s *Server) writeResult(w io.Writer, id json.RawMessage, result any) {
	s.writeMessage(w, response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(w io.Writer, id json.RawMessage, code int, msg string) {
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
