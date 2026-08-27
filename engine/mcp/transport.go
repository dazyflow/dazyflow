// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/schemaports"
)

// Transport is the per-tool core.Transport implementation. The same
// underlying server connection is shared across all of that server's
// tools; calls are serialized through the server's callMu.
type Transport struct {
	serverName string
	toolName   string
	manifest   core.Manifest
	server     *serverConn
}

func (t *Transport) Manifest() core.Manifest { return t.manifest }

func (t *Transport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// Checked before the arguments are even built: a step whose server is
	// down must report the connection, not a complaint about a param. An
	// author looking at this failure needs to be sent to the admin page, and
	// "missing required argument" would send them into the step instead.
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

// buildArguments assembles the tool call's arguments.
//
// The three-layer precedence (params, then the overlay port, then a per-argument
// port) lives in internal/schemaports beside the port synthesis that decides
// which arguments HAVE a port — one policy, read from both ends. What is
// MCP-specific is only which port is the overlay.
func buildArguments(job core.Job, manifest core.Manifest) (map[string]any, error) {
	return schemaports.Assemble(job.Params, job.Input, manifest.Inputs, toolOverlayPort)
}

// contentToOutput maps MCP's result content array into a single output
// ref. The common case is a single text item, which becomes the inline
// string. Mixed results (text + image) become the raw array tagged as
// application/json so downstream nodes can decode if needed.
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

// Integration is what every MCP-provided step reports as its app.
//
// Without one the palette badges these steps "Built-in" (its fallback for a
// manifest with no integration) and the Apps page files them under the
// standard library — which is the opposite of true: they come from someone
// else's server, over a protocol, and an org added them deliberately.
//
// One shared value rather than one per server, because the step's LABEL
// already names the server it came from ("Vendor Tools — Create an issue").
// A badge repeating that would carry no information; a badge saying how the
// step got here does. It matches the vocabulary the product already uses in
// Admin → MCP servers.
//
// Safe to set despite Integration being the connection machinery's key: that
// machinery gates on ConnectionFields (which these manifests do not have) and
// on an explicit OAuth allowlist (which "MCP" is not in), so an MCP step is
// never asked to be "connected" like a vendor app. Its credential lives on the
// server row.
const Integration = "MCP"

// offlineAware stamps a manifest that describes a server with no connection.
//
// The manifest is otherwise COMPLETE — ports, params schema, icon — because a
// flow already using the step needs its shape. Only the flag changes, and the
// flag is what the editor renders the "needs connection" banner from.
func offlineAware(m core.Manifest, offlineReason string) core.Manifest {
	if offlineReason == "" {
		return m
	}
	m.Unavailable = true
	return m
}

// displayName is the server's label when it has one, else its id — for an
// error message a human reads.
func (c *serverConn) displayName() string {
	if c.label != "" {
		return c.label
	}
	return c.name
}

// synthesizeManifest converts an MCP tool descriptor into a Dazyflow
// manifest the engine can validate against. Tool IDs follow the
// "mcp:<server>:<tool>" convention so graph authors see clearly where
// the node lives.
//
// The label is what a human called the server and the name is what its ids are
// built from, so "MCP Test" captions a step whose id is mcp:mcp-test:search.
// The tool half of the caption is the server's own title when it offers one:
// "MCP Test — Weather Information Provider" over "MCP Test — get_weather".
// Neither half is an identifier, so a server that starts sending titles
// re-captions its steps without moving anything a flow holds.
func synthesizeManifest(server, label string, tool Tool, brandLogo string) core.Manifest {
	if label == "" {
		label = server
	}
	desc := tool.Description
	if desc == "" {
		desc = "Tool " + tool.Name + " from MCP server " + server + "."
	}
	// The tool's own arguments become ports, so an author can wire a value
	// straight into `title` instead of assembling an object first. Everything
	// the schema declares is still settable as a param — see toolInputPorts for
	// what earns a port and what does not.
	inputs := append(toolInputPorts(tool.InputSchema), core.Port{
		Port:  toolOverlayPort,
		Label: "Optional JSON object merged with params before the tool call",
		// Every MCP input is inline-only: the server is another process (and
		// over HTTP, another machine), while a Ref's path is on the DAEMON's
		// disk. A job carrying one is refused before the step runs, with that
		// as the reason, rather than the tool receiving a path into a
		// filesystem it cannot see. Same rule, same reason, as a runner drop.
		InlineOnly: true,
	})
	return core.Manifest{
		ID:       "mcp:" + server + ":" + tool.Name,
		Version:  "1.0",
		Label:    label + " — " + tool.DisplayName(),
		Color:    "#7a5",
		Category: "external",
		// BrandLogo is already a data: URI when it is set at all — see
		// icons.go for why nothing else may reach here.
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
		// MCP doesn't declare tool idempotency. Default to false so
		// retry edges that target unsafe tools (POST-style) fail
		// validation rather than silently double-firing. Authors who
		// know a tool is safe can wrap with a relaxed manifest.
		Idempotent: false,
	}
}

// toolOverlayPort is the catch-all input: a whole JSON object merged over the
// params. It stays even now that arguments get their own ports, because it is
// the only way to supply an argument this synthesis declines to expose — a
// nested object, an array, a name that cannot be a port.
const toolOverlayPort = "input"

// maxToolPorts caps how many arguments become ports.
//
// A node is a box on a canvas. A tool declaring forty arguments would produce
// one nobody can read, and the arguments past the first handful are nearly
// always the optional long tail — still settable as params, just not worth a
// pin each. Required arguments are taken first, so the cap never hides one
// that must be supplied.
const maxToolPorts = 12

// toolInputPorts turns a tool's argument schema into ports.
//
// The rules — which arguments earn a pin, in what order, how many — live in
// internal/schemaports, shared with the web-API catalog: they are a policy
// about the editor, not about MCP, and two copies would drift. What stays here
// is the part that IS about MCP: the schema is the tool's own inputSchema, and
// the names this manifest has already spent are the overlay port, the
// passthrough pin, and the single output.
func toolInputPorts(schema json.RawMessage) []core.Port {
	return schemaports.Build(
		schemaports.FromJSONSchema(schema),
		schemaports.Options{Reserved: []string{toolOverlayPort, "out"}},
	)
}

// serverConn holds one long-lived connection to an MCP server. All of
// its tools share this connection.
type serverConn struct {
	name string
	// label is the display name, already defaulted to name at attach time.
	label string
	// instructions is the server's own guidance from the handshake, verbatim.
	// Shown to the admin who registered it; never acted on.
	instructions string
	// protocolVersion is the MCP revision the server answered with.
	protocolVersion string
	// offlineReason is set when this registration describes cached tools with
	// no live session. See offline.go.
	offlineReason string
	// tools and logos are what this registration was built from, retained so
	// the daemon can persist them as the snapshot that keeps a disconnected
	// server's steps described. Not read on any execution path.
	tools []Tool
	logos map[string]string
	// tenant owns this server; "" is an operator's instance-wide one.
	tenant string
	client session
	closer func() error
	info   ServerInfo
	// callMu serializes calls over a SHARED stream. It is held only by the
	// stdio transport, whose tools all speak over one pair of pipes.
	//
	// An HTTP server leaves it unlocked (see serialized): each call there is
	// its own request/response round trip, so serializing would queue a flow's
	// parallel MCP steps behind each other for no protocol reason — a for_each
	// fanned over ten rows would call the tool ten times in sequence.
	callMu     sync.Mutex
	concurrent bool
}

// lock serializes the call unless the transport can take concurrent ones.
// Returns the matching unlock so callers can defer it without branching.
func (s *serverConn) lock() func() {
	if s.concurrent {
		return func() {}
	}
	s.callMu.Lock()
	return s.callMu.Unlock
}
