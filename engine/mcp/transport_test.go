// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/mcp"
	"github.com/dazyflow/dazyflow/engine/mcp/mcptest"
)

// registerInProcess wires a FakeServer into a fresh Catalog using
// RegisterStream — no subprocess, just io.Pipe.
func registerInProcess(t *testing.T, serverName string, srv *mcptest.FakeServer) *mcp.Catalog {
	t.Helper()
	clientReadFromServer, serverWritesToClient := io.Pipe()
	serverReadsFromClient, clientWritesToServer := io.Pipe()
	go srv.Serve(serverReadsFromClient, serverWritesToClient)

	client := mcp.NewClient(clientWritesToServer, clientReadFromServer)
	info, err := client.Initialize(t.Context(), "dazyflow-test", "1.0")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}

	cat := mcp.NewCatalog()
	closer := func() error {
		_ = clientWritesToServer.Close()
		_ = serverWritesToClient.Close()
		return nil
	}
	if err := cat.RegisterStream(serverName, client, info.ServerInfo, tools, closer); err != nil {
		t.Fatalf("RegisterStream: %v", err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	return cat
}

func TestCatalog_SynthesizesManifestPerTool(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{
			{Name: "read_file", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
			{Name: "list_dir"},
		},
	}
	cat := registerInProcess(t, "fs", srv)

	manifests := cat.Manifests()
	if len(manifests) != 2 {
		t.Fatalf("got %d manifests, want 2", len(manifests))
	}
	rf, ok := manifests["mcp:fs:read_file"]
	if !ok {
		t.Fatal("missing manifest mcp:fs:read_file")
	}
	if rf.Label != "fs — read_file" {
		t.Errorf("Label = %q", rf.Label)
	}
	if rf.Idempotent {
		t.Error("MCP tools default to non-idempotent")
	}
	if !strings.Contains(string(rf.ParamsSchema), `"path"`) {
		t.Errorf("ParamsSchema = %q; expected to carry tool's inputSchema", rf.ParamsSchema)
	}
}

func TestTransport_ExecuteEchoesToolResult(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "echo"}},
		Handler: func(name string, args map[string]any) mcp.ToolCallResult {
			text, _ := args["msg"].(string)
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: "you said: " + text}},
			}
		},
	}
	cat := registerInProcess(t, "demo", srv)

	transport, ok := cat.Get("", "mcp:demo:echo")
	if !ok {
		t.Fatal("transport not registered")
	}
	job := core.Job{
		ID:     "j1",
		Params: map[string]any{"msg": "hi"},
	}
	res, err := transport.Execute(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	got, _ := res.Output["out"].Inline.(string)
	if got != "you said: hi" {
		t.Errorf("out = %q", got)
	}
}

func TestTransport_InputPortMergesIntoArguments(t *testing.T) {
	// The "input" port lets one node's output feed another's tool args
	// without hardcoding them in the graph. Verify the merge happens.
	var seen map[string]any
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "noop"}},
		Handler: func(_ string, args map[string]any) mcp.ToolCallResult {
			seen = args
			return mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "text", Text: "ok"}}}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:noop")

	job := core.Job{
		ID:     "j1",
		Params: map[string]any{"channel": "#general", "tag": "from-params"},
		Input: map[string]core.Ref{
			"input": {Inline: map[string]any{"text": "from-input", "tag": "from-input"}},
		},
	}
	_, err := transport.Execute(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen["channel"] != "#general" {
		t.Errorf("channel = %v, want #general (from params)", seen["channel"])
	}
	if seen["text"] != "from-input" {
		t.Errorf("text = %v, want from-input (from input port)", seen["text"])
	}
	if seen["tag"] != "from-input" {
		t.Errorf("tag = %v, want from-input (input port overrides params)", seen["tag"])
	}
}

func TestTransport_InputPortAcceptsJSONString(t *testing.T) {
	// http_request emits its response_body as a string when MIME is text/json.
	// branch can route it; if the recipient is an MCP tool, the transport
	// has to JSON-decode it to extract arguments.
	var seen map[string]any
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "noop"}},
		Handler: func(_ string, args map[string]any) mcp.ToolCallResult {
			seen = args
			return mcp.ToolCallResult{Content: []mcp.ContentItem{{Type: "text", Text: "ok"}}}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:noop")

	job := core.Job{
		ID: "j1",
		Input: map[string]core.Ref{
			"input": {MIME: "application/json", Inline: `{"k":"v","n":42}`},
		},
	}
	_, err := transport.Execute(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen["k"] != "v" {
		t.Errorf("k = %v", seen["k"])
	}
	if seen["n"].(float64) != 42 {
		t.Errorf("n = %v", seen["n"])
	}
}

func TestTransport_ToolErrorBecomesNodeFailure(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "bad"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{{Type: "text", Text: "rate-limited"}},
				IsError: true,
			}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:bad")

	res, err := transport.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status=%q", res.Status)
	}
	if res.Error == nil || res.Error.Code != "mcp_tool_error" {
		t.Errorf("error = %+v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "rate-limited") {
		t.Errorf("error message = %q", res.Error.Message)
	}
}

func TestTransport_MultiPartContentReturnsArray(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{{Name: "multi"}},
		Handler: func(_ string, _ map[string]any) mcp.ToolCallResult {
			return mcp.ToolCallResult{
				Content: []mcp.ContentItem{
					{Type: "text", Text: "preamble"},
					{Type: "image", Data: "base64png", MIMEType: "image/png"},
				},
			}
		},
	}
	cat := registerInProcess(t, "demo", srv)
	transport, _ := cat.Get("", "mcp:demo:multi")

	res, err := transport.Execute(t.Context(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q", res.Status)
	}
	arr, ok := res.Output["out"].Inline.([]mcp.ContentItem)
	if !ok {
		t.Fatalf("Inline is %T, want []mcp.ContentItem", res.Output["out"].Inline)
	}
	if len(arr) != 2 {
		t.Errorf("got %d items, want 2", len(arr))
	}
	if res.Output["out"].MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json for multi-part", res.Output["out"].MIME)
	}
}

func TestCatalog_GetReturnsFalseForUnknown(t *testing.T) {
	cat := registerInProcess(t, "any", &mcptest.FakeServer{})
	if _, ok := cat.Get("", "mcp:any:nope"); ok {
		t.Error("Get should return false for unknown tool")
	}
}

func TestCatalog_RejectsDuplicateServerName(t *testing.T) {
	cat := mcp.NewCatalog()
	defer cat.Close()

	for i, name := range []string{"same-name", "same-name"} {
		clientR, serverW := io.Pipe()
		serverR, clientW := io.Pipe()
		srv := &mcptest.FakeServer{}
		go srv.Serve(serverR, serverW)
		client := mcp.NewClient(clientW, clientR)
		info, _ := client.Initialize(context.Background(), "x", "1")
		tools, _ := client.ListTools(context.Background())
		err := cat.RegisterStream(name, client, info.ServerInfo, tools, func() error {
			_ = clientW.Close()
			_ = serverW.Close()
			return nil
		})
		if i == 0 && err != nil {
			t.Fatalf("first register: %v", err)
		}
		if i == 1 && err == nil {
			t.Errorf("second register of %q should fail", name)
		}
	}
}

// TestCatalog_CaptionsWithTheToolTitle covers the display half of a tool
// descriptor: the server's own title becomes the caption, while the id — the
// part a flow holds — stays on the wire name.
func TestCatalog_CaptionsWithTheToolTitle(t *testing.T) {
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{
			{Name: "get_weather", Title: "Weather Information Provider"},
			{Name: "list_dir"},
		},
	}
	cat := registerInProcess(t, "fs", srv)
	manifests := cat.Manifests()

	titled, ok := manifests["mcp:fs:get_weather"]
	if !ok {
		t.Fatal("a titled tool changed its id")
	}
	if titled.Label != "fs — Weather Information Provider" {
		t.Errorf("Label = %q, want the server's title", titled.Label)
	}
	// A tool with no title is captioned by its wire name, as before.
	if untitled := manifests["mcp:fs:list_dir"]; untitled.Label != "fs — list_dir" {
		t.Errorf("untitled Label = %q", untitled.Label)
	}
}

// TestToolDisplayName bounds what a third party can put in a palette row.
func TestToolDisplayName(t *testing.T) {
	cases := []struct {
		name, title, want string
	}{
		{"get_weather", "", "get_weather"},
		{"get_weather", "  Weather  ", "Weather"},
		{"get_weather", "   ", "get_weather"},
		// A paragraph is not a caption: first line only.
		{"get_weather", "Weather\nUse this for forecasts.", "Weather"},
		{"get_weather", strings.Repeat("x", 200), strings.Repeat("x", 60) + "…"},
	}
	for _, c := range cases {
		got := mcp.Tool{Name: c.name, Title: c.title}.DisplayName()
		if got != c.want {
			t.Errorf("Tool{%q, %q}.DisplayName() = %q, want %q", c.name, c.title, got, c.want)
		}
	}
}

// TestCatalog_InlinesAToolIcon covers the wiring from a tool descriptor to the
// manifest field the palette reads. A data: icon keeps this off the network:
// what is under test here is that the logo reaches the manifest at all.
func TestCatalog_InlinesAToolIcon(t *testing.T) {
	const png = "data:image/png;base64," +
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	srv := &mcptest.FakeServer{
		Tools: []mcp.Tool{
			{Name: "with_icon", Icons: []mcp.Icon{{Src: png, MimeType: "image/png"}}},
			{Name: "without_icon"},
			// A source we will not fetch or inline leaves the tool bare rather
			// than failing the handshake.
			{Name: "bad_icon", Icons: []mcp.Icon{{Src: "http://example.test/x.png"}}},
		},
	}
	cat := registerInProcess(t, "fs", srv)
	manifests := cat.Manifests()

	if got := manifests["mcp:fs:with_icon"].BrandLogo; got != png {
		t.Errorf("BrandLogo = %.40q…, want the inlined icon", got)
	}
	if got := manifests["mcp:fs:without_icon"].BrandLogo; got != "" {
		t.Errorf("a tool with no icon got BrandLogo %.40q", got)
	}
	if got := manifests["mcp:fs:bad_icon"].BrandLogo; got != "" {
		t.Errorf("a refused icon reached a manifest: %.40q", got)
	}
	// The tool is still there and still callable — an icon is decoration.
	if _, ok := cat.Get("", "mcp:fs:bad_icon"); !ok {
		t.Error("a tool lost its transport over an icon")
	}
}

// TestCatalog_StepsReportMCPAsTheirApp: without an Integration the palette
// badges these steps "Built-in" — its fallback for a manifest with no app —
// and the Apps page files them under the standard library. Both are wrong: the
// step came from someone else's server that an org added deliberately.
func TestCatalog_StepsReportMCPAsTheirApp(t *testing.T) {
	srv := &mcptest.FakeServer{Tools: []mcp.Tool{{Name: "create_issue"}}}
	cat := registerInProcess(t, "vendor", srv)

	man, ok := cat.Manifests()["mcp:vendor:create_issue"]
	if !ok {
		t.Fatal("missing manifest")
	}
	if man.Integration != mcp.Integration {
		t.Errorf("Integration = %q, want %q", man.Integration, mcp.Integration)
	}
	// The provider stays per-server: it is what scopes a step to the server it
	// came from, and only the APP grouping is shared.
	if man.Provider != "mcp:vendor" {
		t.Errorf("Provider = %q, want it to stay per-server", man.Provider)
	}
	// Carrying an Integration must not make an MCP step look like a vendor app
	// awaiting a connection — that machinery keys on ConnectionFields.
	if len(man.ConnectionFields) != 0 {
		t.Errorf("ConnectionFields = %+v, want none", man.ConnectionFields)
	}
}
