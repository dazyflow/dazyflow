// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"strings"
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
//
// 2025-11-25 rather than 2025-06-18 because that is the revision tool icons
// arrived in. A server that only speaks an older one answers initialize with
// the version it will use and simply sends no icons; we do not require the
// echo to match, so the downgrade costs nothing. The negotiated version is
// recorded on the server's status, so an admin wondering where the icons went
// can see what was actually agreed.
const httpProtocolVersion = "2025-11-25"

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
	// Instructions is the server's own prose about how its tools are meant to
	// be used — optional, and addressed to whoever is composing calls. We do
	// not act on it; it is shown to the admin who added the server, which is
	// the one place a paragraph from a third party is useful and harmless.
	Instructions string `json:"instructions,omitempty"`
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
	Name string `json:"name"`
	// Icons are the images a server offers for this tool, newest-revision
	// metadata we use for the palette. Never load-bearing: a tool with no
	// icon, or one we decline to fetch, falls back to the category glyph.
	Icons []Icon `json:"icons,omitempty"`
	// Title is the server's display name for the tool — "Weather Information
	// Provider" where Name is get_weather. Optional, and NOT an identifier:
	// the step id keeps using Name, so a server that starts sending titles
	// re-captions its steps without moving anything a flow references.
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// DisplayName is what to caption this tool with: its title when the server
// offered one, its wire name otherwise.
//
// Bounded, because a title is arbitrary text from a third party that lands in
// a palette row. A server that sends a paragraph gets the first line of it
// rather than a broken layout — the description is where prose belongs, and
// that field is already shown in full.
func (t Tool) DisplayName() string {
	title := strings.TrimSpace(t.Title)
	if title == "" {
		return t.Name
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if r := []rune(title); len(r) > maxToolTitleLen {
		title = strings.TrimSpace(string(r[:maxToolTitleLen])) + "…"
	}
	if title == "" {
		return t.Name
	}
	return title
}

// maxToolTitleLen bounds a caption to what a palette row can show. The spec
// puts no limit on the field.
const maxToolTitleLen = 60

// Icon is one image a server offers for a tool (MCP revision 2025-11-25,
// SEP-973).
//
// Src is a URI: an https URL, or a data: URI carrying the bytes inline. Both
// are third-party input — see resolveIcon for what we will and will not do
// with one.
type Icon struct {
	Src string `json:"src"`
	// MimeType overrides the source's own type when that is missing or
	// generic. Advisory: a fetched icon is trusted for its RESPONSE type, not
	// for what the descriptor claims.
	MimeType string `json:"mimeType,omitempty"`
	// Sizes are the sizes this icon suits ("48x48", "any"). We do not choose
	// on it — the palette renders one small square and any icon will do — but
	// it is parsed so the shape matches the spec.
	Sizes []string `json:"sizes,omitempty"`
	// Theme is "light" or "dark" when the icon is designed for one. A manifest
	// carries a single logo and the app is theme-aware, so an icon that
	// declares no theme is preferred over one that does.
	Theme string `json:"theme,omitempty"`
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
