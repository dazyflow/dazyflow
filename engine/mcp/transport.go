// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
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
// Three layers, least specific first:
//
//	params        — what the author typed on the step
//	overlay port  — a whole object wired into `input`
//	argument port — a value wired into that one argument
//
// Most-specific-last is the rule the rest of the product already states: "a
// connected input, when present, overrides the typed setting". A value wired
// into `title` is a statement about `title` and beats an object that merely
// happens to contain one.
func buildArguments(job core.Job, manifest core.Manifest) (map[string]any, error) {
	args := make(map[string]any, len(job.Params))
	for k, v := range job.Params {
		args[k] = v
	}
	if input, ok := job.Input[toolOverlayPort]; ok {
		overlay, err := inlineToObject(input.Inline)
		if err != nil {
			return nil, fmt.Errorf("input port: %w", err)
		}
		for k, v := range overlay {
			args[k] = v
		}
	}
	// Driven by the MANIFEST rather than by whatever the job happens to carry:
	// the ports this drop declared are exactly the argument names it may set,
	// so a stray input key cannot introduce an argument the tool never
	// declared.
	for _, port := range manifest.Inputs {
		if port.Port == toolOverlayPort || port.Port == core.PassPort {
			continue
		}
		ref, ok := job.Input[port.Port]
		if !ok || ref.Inline == nil {
			continue
		}
		args[port.Port] = ref.Inline
	}
	return args, nil
}

func inlineToObject(v any) (map[string]any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return x, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return nil, fmt.Errorf("input is a string but not a JSON object")
		}
		return m, nil
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(x, &m); err != nil {
			return nil, fmt.Errorf("input is bytes but not a JSON object")
		}
		return m, nil
	default:
		// Try a generic marshal-then-unmarshal in case it's a struct
		// the engine happened to pass through.
		data, err := json.Marshal(x)
		if err != nil {
			return nil, fmt.Errorf("input not convertible: %T", x)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("input not a JSON object")
		}
		return m, nil
	}
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

// toolSchema is the slice of JSON Schema this package reads. Everything else a
// schema may declare is left to the params form, which renders the raw schema.
type toolSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

// toolProperty is the slice of one property's schema that decides its port.
type toolProperty struct {
	// Type is a string, or an array for a union like ["string","null"].
	Type        any    `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// toolInputPorts turns a tool's argument schema into ports.
//
// Deliberately shallow: a top-level property of scalar type gets a port, and
// nothing else does. An object or array argument keeps its structure, and a
// port per leaf would either flatten that structure into invented names or
// produce a node shaped like the schema rather than like a step — so those stay
// params (or arrive through the overlay port). This maps the common case fully
// and refuses to guess at the rest.
//
// Order is required-then-optional, alphabetical within each. It has to be
// deterministic and independent of map iteration: ports are identified by
// position in the editor's layout, so a set that reshuffled between restarts
// would move every wire on the canvas.
func toolInputPorts(schema json.RawMessage) []core.Port {
	if len(schema) == 0 {
		return nil
	}
	var s toolSchema
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties) == 0 {
		// A schema we cannot read is not an error: the tool still works
		// through params and the overlay port. Reporting it here would fail a
		// whole server's registration over one tool's unusual schema.
		return nil
	}
	required := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		required[r] = true
	}

	// Resolve every property once, then order. Two passes over the map with a
	// re-parse in each was the obvious shape and the wrong one: the schema
	// would be decoded twice per port, and the two passes could disagree about
	// what qualifies if either rule were ever edited alone.
	type arg struct {
		name  string
		label string
		mime  []string
	}
	var req, opt []arg
	for name, raw := range s.Properties {
		if !portableArgName(name) {
			continue
		}
		var p toolProperty
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		mime, ok := scalarMIME(p.Type)
		if !ok {
			continue
		}
		label := p.Title
		if label == "" {
			label = name
		}
		a := arg{name: name, label: label, mime: mime}
		if required[name] {
			req = append(req, a)
		} else {
			opt = append(opt, a)
		}
	}
	byName := func(s []arg) func(i, j int) bool {
		return func(i, j int) bool { return s[i].name < s[j].name }
	}
	sort.Slice(req, byName(req))
	sort.Slice(opt, byName(opt))

	ports := make([]core.Port, 0, maxToolPorts)
	for _, a := range append(req, opt...) {
		if len(ports) == maxToolPorts {
			break
		}
		ports = append(ports, core.Port{
			Port:     a.name,
			Label:    a.label,
			MIME:     a.mime,
			Required: required[a.name],
			// See synthesizeManifest: an MCP server cannot read the daemon's
			// disk, so no MCP input may take a file reference.
			InlineOnly: true,
		})
	}
	return ports
}

// portableArgName reports whether an argument name can be a port id.
//
// Two rules. It must be spellable as an id — a property called "user name" or
// "a/b" would produce an edge nothing can address. And it must not be one of
// the names this manifest already uses: an argument called "input" would
// shadow the overlay port, and one called "pass" the universal passthrough
// pin, in both cases silently. Such an argument stays a param, which still
// reaches the tool.
func portableArgName(name string) bool {
	if name == "" || name == toolOverlayPort || name == core.PassPort || name == "out" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// scalarMIME maps a JSON Schema type to a port type, and reports whether it is
// one this synthesis exposes at all.
//
// A union is read through its first non-null member, so the ["string","null"]
// that generators emit for an optional argument lands as text rather than
// being skipped for not being a plain string.
func scalarMIME(declared any) ([]string, bool) {
	switch t := declared.(type) {
	case string:
		switch t {
		case "string":
			return []string{"text/plain"}, true
		case "number", "integer":
			// Numbers travel as text on a port, the same as every built-in
			// numeric input (a Twilio amount, a Gmail count).
			return []string{"text/plain"}, true
		case "boolean":
			return []string{core.MIMEBool}, true
		default:
			// object, array, null, or something unknown: params only.
			return nil, false
		}
	case []any:
		for _, one := range t {
			s, ok := one.(string)
			if !ok || s == "null" {
				continue
			}
			return scalarMIME(s)
		}
		return nil, false
	default:
		// No declared type at all. The value could be anything, so the port
		// would have to be untyped — and an untyped pin next to typed ones
		// reads as a mistake. Params only.
		return nil, false
	}
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
