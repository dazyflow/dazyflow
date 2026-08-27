// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
)

// MCP-specific request/response shapes. Only the slice we actively use
// (initialize, tools/list, tools/call) is modeled; resources, prompts,
// and sampling can be added without changing the client.

// protocolVersion is what the STDIO transport negotiates. Streamable HTTP did
// not exist in this revision, which is why the HTTP transport announces a
// later one — see httpProtocolVersion.
const protocolVersion = "2024-11-05"

// httpProtocolVersion is what the HTTP transport negotiates. Streamable HTTP
// (one POST endpoint answering either JSON or SSE, with a session header)
// arrived in 2025-03-26 and is the shape every hosted MCP server publishes
// today; announcing 2024-11-05 over HTTP would be claiming a revision in which
// this transport has no definition.
//
// The two versions differ ON PURPOSE. They are not "the version we support" —
// they are what each transport's framing is defined by, and pinning both to
// one number would mean lying about one of them.
const httpProtocolVersion = "2025-06-18"

// caller is the JSON-RPC surface an MCP operation needs.
//
// Both transports implement it — Client over a subprocess's pipes, HTTPClient
// over POSTs — so initialize/tools-list/tools-call are written once against
// the PROTOCOL rather than once per way of moving bytes. The alternative was a
// second copy of these three functions on HTTPClient, which is precisely where
// the two would drift the first time a field is added.
type caller interface {
	Call(ctx context.Context, method string, params, result any) error
	Notify(method string, params any) error
}

type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      ClientInfo     `json:"clientInfo"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initialize runs the MCP handshake. Per spec, the client must send
// notifications/initialized after a successful initialize before
// calling any other method.
func initialize(ctx context.Context, c caller, protocol, name, version string) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: protocol,
		Capabilities:    map[string]any{},
		ClientInfo:      ClientInfo{Name: name, Version: version},
	}
	var result InitializeResult
	if err := c.Call(ctx, "initialize", params, &result); err != nil {
		return nil, err
	}
	if err := c.Notify("notifications/initialized", nil); err != nil {
		return nil, err
	}
	return &result, nil
}

// Initialize runs the MCP handshake over the stdio transport.
func (c *Client) Initialize(ctx context.Context, name, version string) (*InitializeResult, error) {
	return initialize(ctx, c, protocolVersion, name, version)
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

func listTools(ctx context.Context, c caller) ([]Tool, error) {
	var result toolsListResult
	if err := c.Call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) { return listTools(ctx, c) }

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ContentItem represents one slice of a tool's result. MCP tools can
// return mixed-mode results (text + image + reference), modeled as an
// array.
type ContentItem struct {
	Type     string `json:"type"` // "text" | "image" | "resource"
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`     // base64 (for images)
	MIMEType string `json:"mimeType,omitempty"` // for images
}

type ToolCallResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// callTool invokes the named tool with the supplied arguments. A nil
// arguments map is sent as an empty object — MCP servers tend to
// reject missing argument fields with a JSON-RPC error rather than
// treating them as empty.
func callTool(ctx context.Context, c caller, name string, args map[string]any) (*ToolCallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	var result ToolCallResult
	if err := c.Call(ctx, "tools/call", toolCallParams{Name: name, Arguments: args}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error) {
	return callTool(ctx, c, name, args)
}
