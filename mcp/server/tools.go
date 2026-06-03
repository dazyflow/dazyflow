package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// idempotencyKeyFor derives a deterministic Idempotency-Key from the
// tool name + raw args. The same tool invocation with the same args
// produces the same key — so an LLM retrying a tool after a network
// blip will hit the gateway's cached 2xx response instead of firing
// the action twice. Distinct tool calls (different name or args)
// produce distinct keys and run normally.
//
// We hash both the name (namespacing) and the canonical args bytes.
// The MCP framing passes args as raw JSON, so we use those bytes
// directly — no canonicalization is needed because the LLM sends the
// exact same JSON on retry. Hash is SHA-256 hex-truncated to 32 chars
// (128 bits) — same collision resistance as a UUIDv4 in less space,
// and well under the gateway's 128-char cap.
func idempotencyKeyFor(toolName string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0}) // separator so {"name":"x","args":"y"} ≠ {"name":"xy","args":""}
	h.Write(args)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

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
//
// The catalog tools (list_integrations / describe_integration /
// list_drops / describe_drop) front the daemon's /api/v1/catalog
// surface. They're the LLM's discovery path: list_integrations first
// to see what services are available, describe_integration to learn
// what one of them can do, then describe_drop to get the params
// schema + worked examples for the specific node before composing a
// flow. We expose them as separate tools (rather than one giant
// "search") so the LLM picks the right level of detail per turn.
func BuildTools(c *HazydClient, d Defaults) []Tool {
	return []Tool{
		listIntegrations(c),
		describeIntegration(c),
		listDrops(c),
		describeDrop(c),
		describeTriggerKinds(c),
		listConnections(c),
		startConnection(c),
		listSecrets(c),
		setSecret(c),
		deleteSecret(c),
		validateCron(c),
		listFlows(c, d),
		getFlow(c, d),
		saveFlow(c, d, "create_flow",
			"Create a new flow. Use this for fresh graphs; for in-place edits to an existing flow use update_flow (same wire shape, distinct intent so the LLM doesn't accidentally overwrite something it didn't mean to touch). Note: edits are rejected with HTTP 409 while a run of the flow is active."),
		saveFlow(c, d, "update_flow",
			"Update an existing flow in place. Pass the FULL graph payload (nodes + edges) — the daemon overwrites the prior version. Refuses with HTTP 409 if a run of this flow is currently in flight."),
		patchFlow(c, d),
		deleteFlow(c, d),
		enableFlow(c, d),
		disableFlow(c, d),
		validateFlow(c, d),
		validateGraph(c),
		testTriggerFlow(c, d),
		sampleNode(c, d),
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
//
// 4xx responses from spec-aligned routes come back as structured JSON
// so the LLM can branch on the snake_case `code` enum rather than
// parse English. Shape:
//
//	{"status":409,"code":"flow_locked","message":"...","path":"POST /me/flows/.../run","details":[...],"doc":"/api/v1/openapi.json#..."}
//
// Legacy routes that still emit `{"error":"<string>"}` come back with
// `code` empty and `message` filled from the string — same shape, the
// LLM can ignore the empty code.
func errorResultOrErr(err error) (ToolCallResult, error) {
	if err == nil {
		return ToolCallResult{}, nil
	}
	var herr *HTTPError
	if errors.As(err, &herr) && herr.Status >= 400 && herr.Status < 500 {
		payload := map[string]any{
			"status":  herr.Status,
			"path":    herr.Path,
			"message": herr.Message,
		}
		if herr.Code != "" {
			payload["code"] = herr.Code
		}
		if len(herr.Details) > 0 {
			payload["details"] = herr.Details
		}
		if herr.Doc != "" {
			payload["doc"] = herr.Doc
		}
		b, mErr := json.MarshalIndent(payload, "", "  ")
		if mErr != nil {
			// Falling back to the flat shape keeps a botched marshal
			// from turning into an empty tool-error.
			return ErrorResult(fmt.Sprintf("daemon returned %d: %s", herr.Status, herr.Message)), nil
		}
		return ToolCallResult{
			IsError: true,
			Content: []ContentItem{{Type: "text", Text: string(b)}},
		}, nil
	}
	return ToolCallResult{}, err
}

// ─────────────────────────── tool definitions ───────────────────────────

func listIntegrations(c *HazydClient) Tool {
	return Tool{
		Name:        "list_integrations",
		Description: "List every integration the daemon offers, grouped by vendor (Slack, Gmail, GitHub, ...). Each entry includes a one-sentence summary and how many drops it exposes. Use this FIRST when composing a new flow — narrow by integration before drilling into individual drops.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"q":        {"type":"string","description":"Free-text filter against integration label and summary."},
			"category": {"type":"string","description":"Optional category filter: trigger, transformation, io, ai, network, external, system, flow_control."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			qs := ""
			if q := stringField(args, "q", ""); q != "" {
				qs = "?q=" + pathSegment(q)
			}
			if cat := stringField(args, "category", ""); cat != "" {
				sep := "?"
				if qs != "" {
					sep = "&"
				}
				qs += sep + "category=" + pathSegment(cat)
			}
			var out map[string]any
			if err := c.Get(ctx, "/catalog/integrations"+qs, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func describeIntegration(c *HazydClient) Tool {
	return Tool{
		Name:        "describe_integration",
		Description: "Return one integration's detail page: its auth shape, every drop it exposes with their role (trigger / action / transformation), and example flows. Read this BEFORE describing individual drops — it tells you which drop within the integration to look at.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id": {"type":"string","description":"Integration ID, e.g. 'Slack' or 'standard-library'. Get the canonical IDs from list_integrations."}
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
			var out map[string]any
			if err := c.Get(ctx, "/catalog/integrations/"+pathSegment(id), &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func listDrops(c *HazydClient) Tool {
	return Tool{
		Name:        "list_drops",
		Description: "Search the flat drop catalog. Returns lean per-drop entries (id, label, summary, category, integration). Use the optional filters to narrow — full per-drop detail (params schema, examples) comes from describe_drop.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"q":           {"type":"string","description":"Free-text filter against label, description, tags."},
			"category":    {"type":"string"},
			"integration": {"type":"string","description":"Limit to drops in this integration (e.g. 'Slack')."},
			"tag":         {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			qs := ""
			add := func(k, v string) {
				if v == "" {
					return
				}
				sep := "?"
				if qs != "" {
					sep = "&"
				}
				qs += sep + k + "=" + pathSegment(v)
			}
			add("q", stringField(args, "q", ""))
			add("category", stringField(args, "category", ""))
			add("integration", stringField(args, "integration", ""))
			add("tag", stringField(args, "tag", ""))
			var out map[string]any
			if err := c.Get(ctx, "/catalog/drops"+qs, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func describeDrop(c *HazydClient) Tool {
	return Tool{
		Name:        "describe_drop",
		Description: "Get the full manifest of one drop — params JSON Schema, worked params examples, I/O ports, execution model, retry policy. THIS is the source of truth when composing the node's params; the examples field gives you concrete shapes to crib from.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id"],"properties":{
			"id": {"type":"string","description":"Drop ID (e.g. 'http_request', 'slack_send_message'). Find IDs via list_drops or describe_integration."}
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
			var out map[string]any
			if err := c.Get(ctx, "/catalog/drops/"+pathSegment(id), &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// describeTriggerKinds returns the typed schema for every supported
// trigger kind (cron, webhook, poll) plus worked examples. Use this
// BEFORE composing a flow with a trigger — esp. webhook+public_form,
// which isn't obvious from any single drop's description.
func describeTriggerKinds(c *HazydClient) Tool {
	return Tool{
		Name:        "describe_trigger_kinds",
		Description: "Return the schema for every supported GraphTrigger kind (cron, webhook, poll), with per-field descriptions and worked examples. Consult this when composing a flow that needs to fire on a schedule, accept a webhook, or show a hosted intake form (webhook + public_form:true).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (ToolCallResult, error) {
			var out map[string]any
			if err := c.Get(ctx, "/catalog/trigger-kinds", &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// listConnections returns the OAuth providers the daemon knows about
// plus which accounts the calling principal has already linked. Use
// this BEFORE composing a flow whose drops have non-empty
// requires_connections — if a provider isn't connected, hand the
// authorize URL from start_connection to the user before continuing.
func listConnections(c *HazydClient) Tool {
	return Tool{
		Name:        "list_connections",
		Description: "List OAuth providers the daemon offers and which accounts the caller has linked. Each entry: {name, accounts:[...]}. Empty `accounts` = not connected. Pair with start_connection to begin the auth dance for a not-connected provider.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (ToolCallResult, error) {
			var out map[string]any
			if err := c.Get(ctx, "/me/connections", &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// startConnection mints a provider authorize URL the LLM passes to
// the user. The user opens it in a browser, completes the OAuth
// dance, and the callback finalizes server-side — no further MCP
// interaction is needed. Re-running list_connections after the user
// reports success confirms the link.
func startConnection(c *HazydClient) Tool {
	return Tool{
		Name:        "start_connection",
		Description: "Begin the OAuth flow for a provider. Returns {authorize_url:\"https://...\"} — hand this URL to the user, ask them to open it and complete the consent screen. After they're back, call list_connections to confirm the account now appears under the provider.",
		InputSchema: json.RawMessage(`{"type":"object","required":["provider"],"properties":{
			"provider":  {"type":"string","description":"Provider ID from list_connections (e.g. 'slack','gmail','github')."},
			"account":   {"type":"string","description":"Stable handle for this connection. Defaults to 'default'; use multiple values when one principal has more than one account at the same provider."},
			"return_to": {"type":"string","description":"Same-origin path the user lands on after the OAuth dance completes. Optional; defaults to /integrations."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			provider := stringField(args, "provider", "")
			if provider == "" {
				return ErrorResult("provider is required"), nil
			}
			qs := ""
			if a := stringField(args, "account", ""); a != "" {
				qs = "?account=" + pathSegment(a)
			}
			if rt := stringField(args, "return_to", ""); rt != "" {
				sep := "?"
				if qs != "" {
					sep = "&"
				}
				qs += sep + "return_to=" + pathSegment(rt)
			}
			var out map[string]any
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("start_connection", raw))
			if err := c.Post(ctx, "/me/connections/"+pathSegment(provider)+"/authorize"+qs, nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// listSecrets returns the names of secrets stored for the caller's
// tenant (values never leave the daemon — write-only). LLM use:
// confirm a secret exists by name before building a flow that
// references it via ${tenant:NAME}.
func listSecrets(c *HazydClient) Tool {
	return Tool{
		Name:        "list_secrets",
		Description: "List secret names in the caller's tenant. Returns {secrets:[name,...]}. Values are write-only — there is no read API; the only way to inspect a secret is to use it in a node. Pair with set_secret to add one.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, _ json.RawMessage) (ToolCallResult, error) {
			var out map[string]any
			if err := c.Get(ctx, "/secrets", &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// setSecret stores a secret value for the caller's tenant. Use when
// the user pastes an API key in chat ("here's my Stripe key") — the
// LLM stores it under a stable name and then references it via
// ${tenant:NAME} in any flow node that needs it.
func setSecret(c *HazydClient) Tool {
	return Tool{
		Name:        "set_secret",
		Description: "Store a secret value under the given name in the caller's tenant. Overwrites if the name exists. After calling, reference the secret from a flow node's params as `${tenant:NAME}` (the daemon resolves it at run time). Names must be A-Z 0-9 _ . / - .",
		InputSchema: json.RawMessage(`{"type":"object","required":["name","value"],"properties":{
			"name":  {"type":"string","description":"Stable name. Convention: SCREAMING_SNAKE_CASE. Becomes the key flow nodes reference via ${tenant:NAME}."},
			"value": {"type":"string","description":"The literal secret value. Will be encrypted at rest by the daemon."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			name := stringField(args, "name", "")
			value := stringField(args, "value", "")
			if name == "" || value == "" {
				return ErrorResult("name and value are required"), nil
			}
			body := map[string]string{"value": value}
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("set_secret", raw))
			if err := c.Put(ctx, "/secrets/"+pathSegment(name), body, nil); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(map[string]any{"name": name, "saved": true}), nil
		},
	}
}

// deleteSecret removes a secret. Use when the user explicitly asks
// to remove a key, or when rotating to a new one ("delete the old
// then set the new"). Idempotent: deleting a missing name is a 204.
func deleteSecret(c *HazydClient) Tool {
	return Tool{
		Name:        "delete_secret",
		Description: "Permanently remove a secret. Idempotent: missing names succeed silently. Flows that still reference the deleted secret via ${tenant:NAME} will fail at run time — pair with list_flows / get_flow before deleting if you're unsure.",
		InputSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{
			"name": {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			name := stringField(args, "name", "")
			if name == "" {
				return ErrorResult("name is required"), nil
			}
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("delete_secret", raw))
			if err := c.Delete(ctx, "/secrets/"+pathSegment(name)); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(map[string]any{"name": name, "deleted": true}), nil
		},
	}
}

// validateCron pre-flights a cron expression against the same parser
// the scheduler uses. Useful so the LLM can confirm "0 9 * * 1" is
// valid (and means what it thinks) BEFORE saving a flow with a cron
// trigger — catches mistakes in chat instead of via a 422 from save.
func validateCron(c *HazydClient) Tool {
	return Tool{
		Name:        "validate_cron",
		Description: "Validate a cron expression. Returns {ok:true} on parse success, or {ok:false, error:\"...\"} when the scheduler would reject it. Call this BEFORE create_flow when wiring a cron trigger so a bad expression surfaces in chat instead of at save time.",
		InputSchema: json.RawMessage(`{"type":"object","required":["expr"],"properties":{
			"expr": {"type":"string","description":"Standard 5-field cron (minute hour day month weekday). Example: \"0 9 * * 1\" = every Monday at 09:00."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			expr := stringField(args, "expr", "")
			if expr == "" {
				return ErrorResult("expr is required"), nil
			}
			var out map[string]any
			if err := c.Post(ctx, "/validate/cron", map[string]string{"expr": expr}, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// enableFlow / disableFlow toggle the Disabled flag on a saved flow.
// Disabled flows skip scheduled firings (cron + poll) and reject
// inbound webhooks/forms, but explicit run_flow / test_trigger_flow
// calls still work — those represent intentional triggering.
//
// Use to "pause" a flow without losing the definition: e.g. user
// says "stop the Monday digest for a few weeks" — call disable_flow
// now, then enable_flow when they're ready.
func enableFlow(c *HazydClient, d Defaults) Tool {
	return enableOrDisable(c, d, "enable_flow",
		"Re-enable a previously-disabled flow. Idempotent: enabling an enabled flow is a no-op.",
		"/enable")
}
func disableFlow(c *HazydClient, d Defaults) Tool {
	return enableOrDisable(c, d, "disable_flow",
		"Pause a flow without deleting it. Scheduled firings (cron/poll) and inbound webhooks are suppressed; explicit run_flow / test_trigger_flow calls still work. Idempotent.",
		"/disable")
}
func enableOrDisable(c *HazydClient, d Defaults, name, desc, suffix string) Tool {
	return Tool{
		Name:        name,
		Description: desc,
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
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor(name, raw))
			if err := c.Post(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)+suffix, nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// deleteFlow removes a flow from the workspace. Refuses with 409 if
// a run is currently in flight on this flow (same lock as save/patch).
// Idempotent on the wire: a missing flow returns 204.
func deleteFlow(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "delete_flow",
		Description: "Permanently remove a flow. Use this when the user wants to undo a creation or retire a flow. Refuses (HTTP 409) if a run is currently active on the flow. Idempotent: deleting a missing flow is a no-op.",
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
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("delete_flow", raw))
			if err := c.Delete(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(map[string]any{"id": id, "deleted": true}), nil
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
			path := fmt.Sprintf("/me/flows?tenant=%s&workspace=%s", pathSegment(tenant), pathSegment(workspace))
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
			if err := c.Get(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id), &out); err != nil {
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
			// Key includes the tool name so retried create_flow vs
			// update_flow with the same args don't collide on cache.
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor(name, raw))
			if err := c.Put(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id), body, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// patchFlow exposes the gateway's PATCH /me/flows/{id} (RFC 7396 JSON
// Merge Patch). Use this for incremental edits — adding a node,
// rewiring an edge, changing a label — without re-uploading the whole
// graph. Much cheaper on context than update_flow when only a small
// part of the graph changes.
//
// Merge semantics: keys present in the patch replace target values;
// nulls delete keys; nested objects merge recursively; arrays REPLACE
// wholesale. To change a single node's params, send
// `{"nodes":[<full new nodes list>]}` — there is no per-index array
// merge in RFC 7396.
func patchFlow(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name: "patch_flow",
		Description: "Apply a JSON Merge Patch (RFC 7396) to an existing flow. " +
			"Use for incremental edits — the patch body is a sparse subset of the Graph; " +
			"unspecified keys are left alone, nulls delete, arrays replace wholesale. " +
			"Refuses with HTTP 409 if a run of this flow is currently in flight.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id","patch"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"patch":     {"type":"object","description":"Sparse Graph document. Only keys you want to change. Use null to delete a key."}
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
			patch, ok := args["patch"].(map[string]any)
			if !ok {
				return ErrorResult("patch must be a JSON object"), nil
			}
			var out map[string]any
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("patch_flow", raw))
			if err := c.Patch(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id), patch, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// validateFlow lints the flow at HEAD without saving. Lets the LLM
// sanity-check a graph it just authored before running it — catches
// schema mismatches, orphaned nodes, hardcoded secrets, etc. Returns
// the same lint shape SaveFlow appends after a write.
func validateFlow(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "validate_flow",
		Description: "Lint a flow (currently saved version) without running it. Returns {ok, issues:[{severity,node,field,message}]}. Use after create_flow / update_flow / patch_flow to verify the saved shape lints clean before triggering a run.",
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
			// Read-only operation: no idempotency key needed (the server
			// won't dedupe GET-like calls anyway).
			if err := c.Post(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)+"/validate", nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// validateGraph dry-runs the lint over a Graph JSON document without
// saving. Use this when composing a flow in chat — pass the candidate
// graph, see if it lints clean, then call create_flow only when
// satisfied. Avoids the create-fix-update churn of authoring against
// HEAD-only validate_flow.
func validateGraph(c *HazydClient) Tool {
	return Tool{
		Name:        "validate_graph",
		Description: "Lint a Graph JSON document without saving. Returns {ok, issues}. Use this DURING authoring to catch problems (unknown modules, orphan nodes, hardcoded secrets, port mismatches) before calling create_flow. The body is the same shape create_flow accepts.",
		InputSchema: json.RawMessage(`{"type":"object","required":["graph"],"properties":{
			"graph": {"type":"object","description":"A Graph document — nodes, edges, optional triggers/visibility/etc. See create_flow for shape."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			graph, ok := args["graph"].(map[string]any)
			if !ok {
				return ErrorResult("graph must be a JSON object"), nil
			}
			var out map[string]any
			if err := c.Post(ctx, "/validate/graph", graph, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// testTriggerFlow simulates a webhook/form trigger payload so an LLM
// (or developer) can verify a trigger-shaped flow end-to-end without
// wiring up an external caller. Seeds the trigger node with the
// supplied JSON payload — exactly as a real /trigger or /form POST
// would — and returns the run ID to observe.
func testTriggerFlow(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "test_trigger_flow",
		Description: "Fire a flow as if a webhook/form trigger had received the supplied JSON payload. Returns the run ID. Use this to verify a trigger-driven flow without exposing it to real traffic.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id","payload"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"payload":   {"description":"JSON payload to seed the trigger node with. Object, array, or primitive."}
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
			body := map[string]any{"payload": args["payload"]}
			var out map[string]any
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("test_trigger_flow", raw))
			if err := c.Post(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)+"/test-trigger", body, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

// sampleNode executes a single node with synthetic input so the LLM
// can answer "what does this node actually emit, given X?" without
// running the whole flow. Useful for debugging during authoring or
// for the LLM to reason about a transformation node's behavior.
func sampleNode(c *HazydClient, d Defaults) Tool {
	return Tool{
		Name:        "sample_node",
		Description: "Run a single node in isolation with the supplied input map and return its output. Inputs are keyed by port name. Used for debugging a node mid-flow without running the upstream chain.",
		InputSchema: json.RawMessage(`{"type":"object","required":["id","node_id"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"node_id":   {"type":"string"},
			"inputs":    {"type":"object","description":"Map of input-port-name → value. Optional; defaults to empty."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			id := stringField(args, "id", "")
			nodeID := stringField(args, "node_id", "")
			if id == "" || nodeID == "" {
				return ErrorResult("id and node_id are required"), nil
			}
			tenant, workspace, err := scoped(args, d)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			body := map[string]any{}
			if inputs, ok := args["inputs"].(map[string]any); ok {
				body["inputs"] = inputs
			}
			var out map[string]any
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("sample_node", raw))
			path := "/me/flows/" + composeFlowID(tenant, workspace, id) +
				"/nodes/" + pathSegment(nodeID) + "/sample"
			if err := c.Post(ctx, path, body, &out); err != nil {
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
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("run_flow", raw))
			if err := c.Post(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)+"/run", nil, &out); err != nil {
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
			path := fmt.Sprintf("/me/runs/%s/cancel", pathSegment(runID))
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("cancel_run", raw))
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
			if err := c.Get(ctx, "/me/runs/"+pathSegment(runID), &out); err != nil {
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
				path = fmt.Sprintf("/me/flows/%s/runs?limit=%d",
					composeFlowID(tenant, workspace, flowID), limit)
			} else {
				path = fmt.Sprintf("/me/runs?limit=%d", limit)
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
				if err := c.Get(ctx, "/me/runs/"+pathSegment(runID), &rec); err != nil {
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
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("approve_node", raw))
			if err := c.Post(ctx, path, nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}
