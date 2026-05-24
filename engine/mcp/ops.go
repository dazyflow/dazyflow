package mcp

import (
	"context"
	"encoding/json"
)

// MCP-specific request/response shapes. Only the slice we actively use
// (initialize, tools/list, tools/call) is modeled; resources, prompts,
// and sampling can be added without changing the client.

const protocolVersion = "2024-11-05"

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

// Initialize runs the MCP handshake. Per spec, the client must send
// notifications/initialized after a successful initialize before
// calling any other method.
func (c *Client) Initialize(ctx context.Context, name, version string) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: protocolVersion,
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

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result toolsListResult
	if err := c.Call(ctx, "tools/list", nil, &result); err != nil {
		return nil, err
	}
	return result.Tools, nil
}

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

// CallTool invokes the named tool with the supplied arguments. A nil
// arguments map is sent as an empty object — MCP servers tend to
// reject missing argument fields with a JSON-RPC error rather than
// treating them as empty.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	var result ToolCallResult
	if err := c.Call(ctx, "tools/call", toolCallParams{Name: name, Arguments: args}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
