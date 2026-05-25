package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Defaults captures the tenant/workspace the MCP server falls back to
// when a tool call omits them. Populated once at startup from
// /whoami (or env overrides). Keeps the LLM from having to repeat
// scope on every call when it's working inside a single workspace.
type Defaults struct {
	Tenant    string
	Workspace string
}

// BuildTools wires the full set of MCP tools against an HazydClient.
// Returned in a stable order; the framing layer registers them via
// Server.Register in the order they appear here.
func BuildTools(c *HazydClient, d Defaults) []Tool {
	return []Tool{
		listDrops(c),
		listFlows(c, d),
		getFlow(c, d),
		saveFlow(c, d, "create_flow",
			"Create a new flow. Use this for fresh graphs; for in-place edits to an existing flow use update_flow (same wire shape, distinct intent so the LLM doesn't accidentally overwrite something it didn't mean to touch). Note: edits are rejected with HTTP 409 while a run of the flow is active."),
		saveFlow(c, d, "update_flow",
			"Update an existing flow in place. Pass the FULL graph payload (nodes + edges) — the daemon overwrites the prior version. Refuses with HTTP 409 if a run of this flow is currently in flight."),
		runFlow(c, d),
		cancelRun(c),
		getRun(c),
		listRuns(c, d),
		waitForRun(c),
		listPendingApprovals(c, d),
		approveNode(c),
	}
}

// scoped returns the (tenant, workspace) pair for a call, honoring
// explicit args over the server-wide defaults. Tools call this
// instead of indexing args directly so the precedence is consistent.
func scoped(args map[string]any, d Defaults) (string, string, error) {
	tenant := stringField(args, "tenant", d.Tenant)
	workspace := stringField(args, "workspace", d.Workspace)
	if tenant == "" || workspace == "" {
		return "", "", errors.New("tenant and workspace must be supplied or set on the server defaults")
	}
	return tenant, workspace, nil
}

func stringField(args map[string]any, key, fallback string) string {
	if args == nil {
		return fallback
	}
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return fallback
}

func intField(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case float64:
			return int(x)
		case int:
			return x
		case json.Number:
			n, err := x.Int64()
			if err == nil {
				return int(n)
			}
		}
	}
	return fallback
}

// decodeArgs is a small wrapper that turns json.RawMessage args into
// a map. Tools that need typed args unmarshal directly into a
// concrete struct instead.
func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode arguments: %w", err)
	}
	return out, nil
}

// errorResultOrErr maps an HTTP failure into the right MCP shape. The
// spec separates "tool couldn't be invoked" (RPC error) from "tool
// ran but the operation failed" (ToolCallResult.IsError). Most hzd
// 4xx responses are the latter — the LLM asked for something the
// user can fix (bad ID, locked flow). 5xx and network failures are
// the former.
func errorResultOrErr(err error) (ToolCallResult, error) {
	if err == nil {
		return ToolCallResult{}, nil
	}
	var herr *HTTPError
	if errors.As(err, &herr) && herr.Status >= 400 && herr.Status < 500 {
		return ErrorResult(fmt.Sprintf("daemon returned %d: %s", herr.Status, herr.Body)), nil
	}
	return ToolCallResult{}, err
}

// ─────────────────────────── tool definitions ───────────────────────────

func listDrops(c *HazydClient) Tool {
	return Tool{
		Name: "list_drops",
		Description: "List every flow node ('drop') the daemon knows about. The returned map keys are module IDs (e.g. 'http_request', 'await_approval', 'slack_post'); the values are the full manifest including label, description, inputs/outputs, and params_schema. Call this BEFORE create_flow so you know what modules exist and what params each one accepts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (ToolCallResult, error) {
			var out map[string]any
			if err := c.Get(ctx, "/drops", &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func listFlows(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "list_flows",
		Description: "List flow IDs in a workspace. Use to discover what already exists before creating a new flow.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"tenant":    {"type":"string","description":"Tenant slug. Defaults to the bearer's tenant."},
			"workspace": {"type":"string","description":"Workspace name. Defaults to the bearer's workspace."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			tenant, workspace, err := scoped(args, d)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			var out map[string]any
			path := fmt.Sprintf("/graphs?tenant=%s&workspace=%s", pathSegment(tenant), pathSegment(workspace))
			if err := c.Get(ctx, path, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func getFlow(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "get_flow",
		Description: "Fetch a flow's full graph payload (nodes, edges, triggers, settings) so you can show the user what's there or build an updated version off it.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			id := stringField(args, "id", "")
			if id == "" {
				return ErrorResult("id is required"), nil
			}
			tenant, workspace, err := scoped(args, d)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			var out map[string]any
			path := fmt.Sprintf("/graphs/%s/%s/%s", pathSegment(tenant), pathSegment(workspace), pathSegment(id))
			if err := c.Get(ctx, path, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// saveFlow backs both create_flow and update_flow. The wire shape is
// identical — PUT /graphs/{t}/{w}/{id} is idempotent on the server —
// but exposing two distinct tool names lets the LLM (and the user
// watching it) signal intent.
func saveFlow(c *HazydClient, d Defaults, name, description string) Tool {
	return Tool{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"required":["id","nodes"],
			"properties":{
				"id":              {"type":"string","description":"Flow ID — stable handle used by run / trigger URLs."},
				"tenant":          {"type":"string"},
				"workspace":       {"type":"string"},
				"name":            {"type":"string","description":"Human-friendly display name."},
				"description":     {"type":"string"},
				"icon":            {"type":"string"},
				"visibility":      {"type":"string","enum":["org","private"]},
				"timeout_seconds": {"type":"integer","minimum":0,"description":"Wall-time cap; 0 / omitted leaves it unbounded (the daemon default may still apply)."},
				"nodes": {
					"type":"array",
					"description":"Every node in the graph. Use list_drops to discover legal module IDs.",
					"items":{
						"type":"object",
						"required":["id","module"],
						"properties":{
							"id":              {"type":"string"},
							"module":          {"type":"string"},
							"params":          {"type":"object"},
							"timeout_seconds": {"type":"integer","minimum":0}
						}
					}
				},
				"edges": {
					"type":"array",
					"items":{
						"type":"object",
						"required":["from","to"],
						"properties":{
							"from":      {"type":"string"},
							"from_port": {"type":"string","default":"out"},
							"to":        {"type":"string"},
							"to_port":   {"type":"string","default":"in"},
							"on_error":  {"type":"string","enum":["","abort","skip","retry","fallback"]}
						}
					}
				},
				"triggers": {
					"type":"array",
					"description":"Optional cron/webhook triggers — see GraphTrigger.",
					"items": {"type":"object"}
				}
			}
		}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			id := stringField(args, "id", "")
			if id == "" {
				return ErrorResult("id is required"), nil
			}
			tenant, workspace, err := scoped(args, d)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			// Build the body. Path values overwrite anything passed in
			// args so the resource at /graphs/{t}/{w}/{id} matches the
			// URL — same rule the gateway already enforces.
			body := map[string]any{}
			for k, v := range args {
				body[k] = v
			}
			body["id"] = id
			body["tenant"] = tenant
			body["workspace"] = workspace
			if _, ok := body["nodes"]; !ok {
				body["nodes"] = []any{}
			}
			if _, ok := body["edges"]; !ok {
				body["edges"] = []any{}
			}
			var out map[string]any
			path := fmt.Sprintf("/graphs/%s/%s/%s", pathSegment(tenant), pathSegment(workspace), pathSegment(id))
			if err := c.Put(ctx, path, body, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func runFlow(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "run_flow",
		Description: "Submit a run of an existing flow. Returns the run ID; pair with wait_for_run or get_run to observe outcome.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			id := stringField(args, "id", "")
			if id == "" {
				return ErrorResult("id is required"), nil
			}
			tenant, workspace, err := scoped(args, d)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			var out map[string]any
			path := fmt.Sprintf("/graphs/%s/%s/%s/run", pathSegment(tenant), pathSegment(workspace), pathSegment(id))
			if err := c.Post(ctx, path, nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func cancelRun(c *HazydClient) Tool {
	return Tool{
		Name:        "cancel_run",
		Description: "Abort an in-flight run. Graceful: already-running nodes finish, but no further downstream work dispatches.",
		InputSchema: json.RawMessage(`{"type":"object","required":["run_id"],"properties":{
			"run_id": {"type":"string"},
			"reason": {"type":"string","description":"Free-text recorded with the cancellation for audit."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			runID := stringField(args, "run_id", "")
			if runID == "" {
				return ErrorResult("run_id is required"), nil
			}
			body := map[string]string{}
			if reason := stringField(args, "reason", ""); reason != "" {
				body["reason"] = reason
			}
			var out map[string]any
			path := fmt.Sprintf("/runs/%s/cancel", pathSegment(runID))
			if err := c.Post(ctx, path, body, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func getRun(c *HazydClient) Tool {
	return Tool{
		Name:        "get_run",
		Description: "Fetch the current state of a run — status (queued / running / awaiting / succeeded / failed / cancelled), error (if any), timing.",
		InputSchema: json.RawMessage(`{"type":"object","required":["run_id"],"properties":{
			"run_id": {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			runID := stringField(args, "run_id", "")
			if runID == "" {
				return ErrorResult("run_id is required"), nil
			}
			var out map[string]any
			if err := c.Get(ctx, "/jobs/"+pathSegment(runID), &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func listRuns(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "list_runs",
		Description: "List recent runs. Pass flow_id to scope to one flow, status to filter (e.g. only 'failed'), limit to cap the result count.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"flow_id":   {"type":"string","description":"When set, returns runs of this flow only."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"status":    {"type":"string","enum":["","queued","running","awaiting","succeeded","failed","cancelled","skipped"]},
			"limit":     {"type":"integer","minimum":1,"maximum":200,"default":50}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			flowID := stringField(args, "flow_id", "")
			status := stringField(args, "status", "")
			limit := intField(args, "limit", 50)

			var path string
			if flowID != "" {
				tenant, workspace, err := scoped(args, d)
				if err != nil {
					return ErrorResult(err.Error()), nil
				}
				path = fmt.Sprintf("/graphs/%s/%s/%s/runs?limit=%d",
					pathSegment(tenant), pathSegment(workspace), pathSegment(flowID), limit)
			} else {
				path = fmt.Sprintf("/runs?limit=%d", limit)
			}
			if status != "" {
				path += "&status=" + pathSegment(status)
			}
			var out map[string]any
			if err := c.Get(ctx, path, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// waitForRun polls the run record until it reaches a terminal state
// or the deadline passes. MCP has no native streaming primitive for
// arbitrary durations, so a polling tool is the right shape — the
// LLM calls it and the server holds the line until something
// concrete is reportable.
func waitForRun(c *HazydClient) Tool {
	return Tool{
		Name:        "wait_for_run",
		Description: "Block until a run reaches a terminal state (succeeded/failed/cancelled/skipped) or timeout_seconds elapses, then return the final record. Polls every second under the hood. Useful right after run_flow.",
		InputSchema: json.RawMessage(`{"type":"object","required":["run_id"],"properties":{
			"run_id":          {"type":"string"},
			"timeout_seconds": {"type":"integer","minimum":1,"maximum":600,"default":60,"description":"How long to wait before returning the most recent (likely-non-terminal) snapshot."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			runID := stringField(args, "run_id", "")
			if runID == "" {
				return ErrorResult("run_id is required"), nil
			}
			timeout := intField(args, "timeout_seconds", 60)
			if timeout < 1 {
				timeout = 1
			}
			if timeout > 600 {
				timeout = 600
			}
			deadline := time.Now().Add(time.Duration(timeout) * time.Second)

			var last map[string]any
			for {
				var rec map[string]any
				if err := c.Get(ctx, "/jobs/"+pathSegment(runID), &rec); err != nil {
					return errorResultOrErr(err)
				}
				last = rec
				if isTerminal(rec) {
					return TextResult(rec), nil
				}
				if time.Now().After(deadline) {
					// Hand back the last snapshot rather than an error
					// — the LLM may want to call wait_for_run again
					// with a longer budget.
					last["wait_timed_out"] = true
					return TextResult(last), nil
				}
				select {
				case <-ctx.Done():
					return ErrorResult("cancelled"), nil
				case <-time.After(1 * time.Second):
				}
			}
		},
	}
}

func isTerminal(rec map[string]any) bool {
	// The daemon records status either as a string ("succeeded") or
	// as core.JobStatus which JSON-encodes to the same value, so a
	// plain string compare is enough.
	v, _ := rec["status"].(string)
	switch v {
	case "succeeded", "failed", "cancelled", "skipped":
		return true
	}
	// The graph-record uses Status; some endpoints capitalize the
	// field. Belt-and-braces.
	if v2, _ := rec["Status"].(string); v2 != "" {
		switch v2 {
		case "succeeded", "failed", "cancelled", "skipped":
			return true
		}
	}
	return false
}

func listPendingApprovals(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "list_pending_approvals",
		Description: "List await_approval nodes parked across the workspace. Pair with approve_node to resume them.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			tenant := stringField(args, "tenant", d.Tenant)
			workspace := stringField(args, "workspace", d.Workspace)
			path := "/approvals/pending"
			sep := "?"
			if tenant != "" {
				path += sep + "tenant=" + pathSegment(tenant)
				sep = "&"
			}
			if workspace != "" {
				path += sep + "workspace=" + pathSegment(workspace)
			}
			var out map[string]any
			if err := c.Get(ctx, path, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func approveNode(c *HazydClient) Tool {
	return Tool{
		Name:        "approve_node",
		Description: "Resume an await_approval node. decision must be 'approve' or 'reject'; approver and comment are recorded in the resume Result and visible to downstream nodes.",
		InputSchema: json.RawMessage(`{"type":"object","required":["run_id","node_id","decision"],"properties":{
			"run_id":   {"type":"string"},
			"node_id":  {"type":"string"},
			"decision": {"type":"string","enum":["approve","reject"]},
			"comment":  {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			runID := stringField(args, "run_id", "")
			nodeID := stringField(args, "node_id", "")
			decision := stringField(args, "decision", "")
			if runID == "" || nodeID == "" || decision == "" {
				return ErrorResult("run_id, node_id, and decision are required"), nil
			}
			path := fmt.Sprintf("/approvals/%s/%s?decision=%s",
				pathSegment(runID), pathSegment(nodeID), pathSegment(decision))
			if c := stringField(args, "comment", ""); c != "" {
				path += "&comment=" + pathSegment(c)
			}
			var out map[string]any
			if err := c.Post(ctx, path, nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}
