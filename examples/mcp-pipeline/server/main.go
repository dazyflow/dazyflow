// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command server is a tiny MCP stdio server that exposes a couple of
// tools — enough to demonstrate the Dazyflow ↔ MCP integration without
// pulling in a real ecosystem server (the npm-published filesystem
// server, for instance, would require Node in the test environment).
//
// Tools:
//
//	tools/lookup_user(id)      → user record JSON
//	tools/categorize(text)     → category string
//
// Both tools are deterministic to make assertions in run.sh easy.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"git.sr.ht/~klahr/dazyflow/engine/mcp/mcptest"
)

func main() {
	srv := &mcptest.FakeServer{
		Name:    "ap-demo",
		Version: "1.0",
		Tools: []mcp.Tool{
			{
				Name:        "lookup_user",
				Description: "Look up a user record by id.",
				InputSchema: json.RawMessage(`{
					"type":"object",
					"properties":{"id":{"type":"string"}},
					"required":["id"]
				}`),
			},
			{
				Name:        "categorize",
				Description: "Classify free-text into one of {urgent, normal, low}.",
				InputSchema: json.RawMessage(`{
					"type":"object",
					"properties":{"text":{"type":"string"}},
					"required":["text"]
				}`),
			},
		},
		Handler: func(name string, args map[string]any) mcp.ToolCallResult {
			switch name {
			case "lookup_user":
				id, _ := args["id"].(string)
				body := map[string]any{
					"id":    id,
					"name":  "User " + strings.ToUpper(id),
					"email": id + "@example.com",
					"tier":  "premium",
				}
				js, _ := json.Marshal(body)
				return mcp.ToolCallResult{
					Content: []mcp.ContentItem{{Type: "text", Text: string(js)}},
				}
			case "categorize":
				text, _ := args["text"].(string)
				cat := "normal"
				lower := strings.ToLower(text)
				switch {
				case strings.Contains(lower, "urgent"), strings.Contains(lower, "critical"):
					cat = "urgent"
				case strings.Contains(lower, "fyi"), strings.Contains(lower, "low"):
					cat = "low"
				}
				return mcp.ToolCallResult{
					Content: []mcp.ContentItem{{Type: "text", Text: cat}},
				}
			default:
				return mcp.ToolCallResult{
					Content: []mcp.ContentItem{{Type: "text", Text: "unknown tool: " + name}},
					IsError: true,
				}
			}
		},
	}
	fmt.Fprintf(os.Stderr, "ap-demo MCP server starting\n")
	srv.Serve(os.Stdin, os.Stdout)
}
