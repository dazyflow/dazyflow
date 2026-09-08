// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/schemaports"
)

type Transport struct {
	serverName string
	toolName   string
	manifest   core.Manifest
	server     *serverConn
}

func (t *Transport) Manifest() core.Manifest { return t.manifest }

func (t *Transport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// Checked before the arguments are built: a step whose server is down must
	// report the connection, not a complaint about a param — "missing required
	// argument" would send the author into the step instead of to the admin page.
	if t.server != nil && t.server.offlineReason != "" {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error: &core.JobError{
				Code: "mcp_disconnected",
				Message: fmt.Sprintf("MCP server %q is not connected: %s",
					t.server.displayName(), t.server.offlineReason),
			},
		}, nil
	}
	args, err := buildArguments(job, t.manifest)
	if err != nil {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error:  &core.JobError{Code: "bad_input", Message: err.Error()},
		}, nil
	}

	defer t.server.lock()()

	result, err := t.server.client.CallTool(ctx, t.toolName, args)
	if err != nil {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error:  &core.JobError{Code: "mcp_call", Message: err.Error()},
		}, err
	}
	if result.IsError {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error:  &core.JobError{Code: "mcp_tool_error", Message: contentSummary(result.Content)},
		}, nil
	}

	value, mime := contentToOutput(result.Content)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: mime, Inline: value},
		},
	}, nil
}

func buildArguments(job core.Job, manifest core.Manifest) (map[string]any, error) {
	return schemaports.Assemble(job.Params, job.Input, manifest.Inputs, toolOverlayPort)
}

func contentToOutput(content []ContentItem) (any, string) {
	switch len(content) {
	case 0:
		return "", "text/plain"
	case 1:
		if content[0].Type == "text" {
			return content[0].Text, "text/plain"
		}
		return content[0], "application/json"
	default:
		return content, "application/json"
	}
}

func contentSummary(content []ContentItem) string {
	if len(content) > 0 && content[0].Type == "text" {
		return content[0].Text
	}
	return "tool reported error"
}

// Integration is what every MCP-provided step reports as its app. Without one
// the palette badges them "Built-in" and the Apps page files them under the
// standard library — the opposite of true.
//
// One shared value rather than one per server, because the step's LABEL already
// names the server ("Vendor Tools — Create an issue"): a badge repeating that
// carries no information, while a badge saying how the step got here does.
//
// Safe despite Integration being the connection machinery's key: that machinery
// gates on ConnectionFields, which these manifests lack, and on an OAuth
// allowlist "MCP" is not in. Its credential lives on the server row.
const Integration = "MCP"

// offlineAware leaves the manifest otherwise COMPLETE — ports, params schema,
// icon — because a flow already using the step needs its shape. Only the flag
// changes, and the editor renders the "needs connection" banner from it.
func offlineAware(m core.Manifest, offlineReason string) core.Manifest {
	if offlineReason == "" {
		return m
	}
	m.Unavailable = true
	return m
}

func (c *serverConn) displayName() string {
	if c.label != "" {
		return c.label
	}
	return c.name
}

func synthesizeManifest(server, label string, tool Tool, brandLogo string) core.Manifest {
	if label == "" {
		label = server
	}
	desc := tool.Description
	if desc == "" {
		desc = "Tool " + tool.Name + " from MCP server " + server + "."
	}
	// The tool's own arguments become ports, so an author can wire straight into
	// `title` instead of assembling an object first. Everything the schema declares
	// is still settable as a param.
	inputs := append(toolInputPorts(tool.InputSchema), core.Port{
		Port:       toolOverlayPort,
		Label:      "Extra params",
		InlineOnly: true,
	})
	return core.Manifest{
		ID:             "mcp:" + server + ":" + tool.Name,
		Version:        "1.0",
		Label:          label + " — " + tool.DisplayName(),
		Color:          "#7a5",
		Category:       "external",
		BrandLogo:      brandLogo,
		Provider:       "mcp:" + server,
		Integration:    Integration,
		Tags:           []string{"mcp", server},
		Description:    desc,
		ExecutionModel: core.ExecutionBatch,
		ProcessModel:   core.ProcessLongLived,
		Inputs:         inputs,
		Outputs: []core.Port{{
			Port:  "out",
			Label: "Tool result",
		}},
		ParamsSchema: tool.InputSchema,
		// MCP declares no tool idempotency. False by default, so retry edges targeting
		// unsafe tools fail validation rather than silently double-firing.
		Idempotent: false,
	}
}

// toolOverlayPort stays even now arguments get their own ports: it is the only
// way to supply an argument this synthesis declines to expose — a nested object,
// an array, a name that cannot be a port.
const toolOverlayPort = "input"

// toolInputPorts keeps only the MCP-specific part here: the schema is the tool's
// own inputSchema, and the names already spent are the overlay port, the
// passthrough pin and the single output. Which arguments earn a pin, in what
// order and how many, lives in internal/schemaports, shared with the web-API
// catalog — a policy about the editor, not about MCP.
func toolInputPorts(schema json.RawMessage) []core.Port {
	return schemaports.Build(
		schemaports.FromJSONSchema(schema),
		schemaports.Options{Reserved: []string{toolOverlayPort, "out"}},
	)
}

type serverConn struct {
	name  string
	label string
	// instructions is the server's own handshake guidance, verbatim. Shown to the
	// admin who registered it; never acted on.
	instructions    string
	protocolVersion string
	offlineReason   string
	tools           []Tool
	logos           map[string]string
	tenant          string
	client          session
	closer          func() error
	info            ServerInfo
	// callMu serializes calls over a SHARED stream, and is held only by the stdio
	// transport, whose tools all speak over one pair of pipes. An HTTP server leaves
	// it unlocked: each call is its own round trip, so serializing would queue a
	// flow's parallel MCP steps behind each other for no protocol reason.
	callMu     sync.Mutex
	concurrent bool
}

// lock returns the matching unlock, so callers can defer without branching.
func (s *serverConn) lock() func() {
	if s.concurrent {
		return func() {}
	}
	s.callMu.Lock()
	return s.callMu.Unlock
}
