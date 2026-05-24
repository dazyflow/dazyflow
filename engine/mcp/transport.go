package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
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
	args, err := buildArguments(job)
	if err != nil {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusError,
			Error:  &core.JobError{Code: "bad_input", Message: err.Error()},
		}, nil
	}

	t.server.callMu.Lock()
	defer t.server.callMu.Unlock()

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

// buildArguments assembles the tool's arguments map. It starts from
// job.Params (the graph-author-supplied defaults) and overlays any
// JSON object provided via the "input" port. This lets one node's
// output feed another node's tool call without the author hardcoding
// values.
func buildArguments(job core.Job) (map[string]any, error) {
	args := make(map[string]any, len(job.Params))
	for k, v := range job.Params {
		args[k] = v
	}
	input, ok := job.Input["input"]
	if !ok {
		return args, nil
	}
	overlay, err := inlineToObject(input.Inline)
	if err != nil {
		return nil, fmt.Errorf("input port: %w", err)
	}
	for k, v := range overlay {
		args[k] = v
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

// synthesizeManifest converts an MCP tool descriptor into a Hazy Flow
// manifest the engine can validate against. Tool IDs follow the
// "mcp:<server>:<tool>" convention so graph authors see clearly where
// the node lives.
func synthesizeManifest(server string, tool Tool) core.Manifest {
	return core.Manifest{
		ID:             "mcp:" + server + ":" + tool.Name,
		Version:        "1.0",
		Label:          server + " — " + tool.Name,
		Color:          "#7a5",
		ExecutionModel: core.ExecutionBatch,
		ProcessModel:   core.ProcessLongLived,
		Inputs: []core.Port{{
			Port:  "input",
			Label: "Optional JSON object merged with params before the tool call",
		}},
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

// serverConn holds one long-lived connection to an MCP server. All of
// its tools share this connection; calls are serialized through callMu.
type serverConn struct {
	name   string
	client *Client
	closer func() error
	info   ServerInfo
	callMu sync.Mutex
}
