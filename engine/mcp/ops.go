// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"strings"
)

const protocolVersion = "2024-11-05"

// httpProtocolVersion is later than the stdio one ON PURPOSE. These are not "the
// version we support" but what each transport's framing is defined by: streamable
// HTTP arrived in 2025-03-26, so announcing 2024-11-05 over it would claim a
// revision in which this transport has no definition.
//
// 2025-11-25 rather than 2025-06-18 because that is where tool icons arrived. A
// server speaking an older one answers with the version it will use and sends no
// icons, and we do not require the echo to match, so the downgrade costs nothing.
// The negotiated version is recorded on the server's status.
const httpProtocolVersion = "2025-11-25"

// caller lets initialize, tools/list and tools/call be written once against the
// PROTOCOL rather than once per way of moving bytes. The alternative was a second
// copy of these three on HTTPClient, which is precisely where the two would drift
// the first time a field is added.
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
	Instructions    string         `json:"instructions,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Per spec the client must send notifications/initialized after a successful
// initialize, before any other method.
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

func (c *Client) Initialize(ctx context.Context, name, version string) (*InitializeResult, error) {
	return initialize(ctx, c, protocolVersion, name, version)
}

type Tool struct {
	Name string `json:"name"`
	// Icons are never load-bearing: a tool with no icon, or one we decline to fetch,
	// falls back to the category glyph.
	Icons []Icon `json:"icons,omitempty"`
	// Title is optional and NOT an identifier: the step id keeps using Name, so a
	// server that starts sending titles re-captions its steps without moving anything
	// a flow references.
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// DisplayName is bounded because a title is arbitrary third-party text landing
// in a palette row. A server sending a paragraph gets its first line rather than a
// broken layout; prose belongs in the description, which is already shown in
// full.
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

// maxToolTitleLen bounds a caption to a palette row; the spec sets no limit.
const maxToolTitleLen = 60

type Icon struct {
	Src      string `json:"src"`
	MimeType string `json:"mimeType,omitempty"`
	// Sizes is parsed to match the spec but not chosen on: the palette renders one
	// small square and any icon will do.
	Sizes []string `json:"sizes,omitempty"`
	Theme string   `json:"theme,omitempty"`
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
