// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// idempotencyKeyFor derives a deterministic Idempotency-Key from the
// tool name + raw args. The same tool invocation with the same args
// produces the same key — so an LLM retrying a tool after a network
// blip will hit the gateway's cached 2xx response instead of firing
// the action twice. Distinct tool calls (different name or args)
// produce distinct keys and run normally.
//
// We hash both the name (namespacing) and the args in a CANONICAL form.
// Hashing the raw bytes assumed the retry carries byte-identical JSON, which
// is not something the protocol guarantees: an MCP host that re-serializes
// the arguments between attempts (different key order, different whitespace,
// a re-encoded nested object) produces a different key for the same call —
// so the gateway sees a fresh request and the side effect fires twice, in
// exactly the retry scenario the key exists to make safe.
//
// Hash is SHA-256 hex-truncated to 32 chars (128 bits) — same collision
// resistance as a UUIDv4 in less space, and well under the gateway's
// 128-char cap.
func idempotencyKeyFor(toolName string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0}) // separator so {"name":"x","args":"y"} ≠ {"name":"xy","args":""}
	h.Write(canonicalJSON(args))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// canonicalJSON re-encodes a JSON value so semantically identical arguments
// produce identical bytes: encoding/json emits object keys sorted, and drops
// insignificant whitespace, so key order and formatting stop mattering.
//
// UseNumber keeps numeric literals as their exact source text instead of
// round-tripping through float64. That matters here: decoding into float64
// would map two DIFFERENT large int64 arguments onto the same value, and for
// an idempotency key a false match is worse than a missed one — it would
// silently suppress a distinct action.
//
// Input that isn't valid JSON is hashed verbatim; it can't be canonicalized,
// and it's the gateway's job to reject it.
func canonicalJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// Defaults captures the tenant/workspace the MCP server falls back to
// when a tool call omits them. Populated once at startup from
// /whoami (or env overrides). Keeps the LLM from having to repeat
// scope on every call when it's working inside a single workspace.
type Defaults struct {
	Tenant    string
	Workspace string
}

// BuildTools wires the full set of MCP tools against an DazydClient.
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
func BuildTools(c *DazydClient, d Defaults) []Tool {
	return []Tool{
		listIntegrations(c),
		describeIntegration(c),
		listDrops(c),
		describeDrop(c),
		describeTriggerKinds(c),
		listConnections(c),
		startConnection(c),
		configureConnection(c),
		listSecrets(c),
		setSecret(c),
		deleteSecret(c),
		validateCron(c),
		listFlows(c, d),
		getFlow(c, d),
		flowReferences(c, d),
		saveFlow(c, d, "create_flow",
			"Create a new flow. Use this for fresh graphs; for in-place edits to an existing flow use update_flow (same wire shape, distinct intent so the LLM doesn't accidentally overwrite something it didn't mean to touch). Note: edits are rejected with HTTP 409 while a run of the flow is active."),
		saveFlow(c, d, "update_flow",
			"Update an existing flow in place. Pass the FULL graph payload (nodes + edges) — the daemon overwrites the prior version. Refuses with HTTP 409 if a run of this flow is currently in flight."),
		patchFlow(c, d),
		deleteFlow(c, d),
		enableFlow(c, d),
		disableFlow(c, d),
		publishFlow(c, d),
		unpublishFlow(c, d),
		validateFlow(c, d),
		validateGraph(c),
		generateFlowTool(c),
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
// ran but the operation failed" (ToolCallResult.IsError). Most dzd
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
	// Any HTTP status response (4xx OR 5xx) comes back as a tool error the
	// model can read and explain, rather than a JSON-RPC -32603 the host can't
	// gracefully surface. A 501 ("OAuth not configured"), 502, or 503 is a
	// condition the user can act on ("that integration isn't set up on this
	// server"), not a protocol fault — so it belongs in ToolCallResult.IsError
	// alongside the 4xx cases. Genuine transport failures (no *HTTPError: DNS,
	// connection refused, timeouts) still surface as an RPC error.
	if errors.As(err, &herr) && herr.Status >= 400 {
		payload := herr.ToToolPayload()
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

// ─────────────────────────── handler scaffolds ───────────────────────────

// requireStrings extracts the named string args, returning an
// "<a>, <b>, and <c> are required" error result (matching the legacy
// per-tool messages) when any is empty. The returned slice is aligned
// with keys so callers can index positionally.
//
// The message phrasing reproduces the prior hand-written errors
// exactly: a single missing field reads "id is required", two read
// "name and value are required", three+ use the Oxford-comma "a, b,
// and c are required" form. Behaviour-preserving — the LLM still sees
// the same strings.
func requireStrings(args map[string]any, keys ...string) ([]string, *ToolCallResult) {
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = stringField(args, k, "")
	}
	for _, v := range vals {
		if v == "" {
			res := ErrorResult(requiredFieldsMessage(keys))
			return nil, &res
		}
	}
	return vals, nil
}

// requiredFieldsMessage renders the "<fields> are required" phrasing
// shared by every required-field guard, matching the originals:
//   - 1 field:  "id is required"
//   - 2 fields: "name and value are required"
//   - 3+ fields:"run_id, node_id, and decision are required"
func requiredFieldsMessage(keys []string) string {
	switch len(keys) {
	case 0:
		return "required fields missing"
	case 1:
		return keys[0] + " is required"
	case 2:
		return keys[0] + " and " + keys[1] + " are required"
	default:
		head := keys[:len(keys)-1]
		return strings.Join(head, ", ") + ", and " + keys[len(keys)-1] + " are required"
	}
}

// scopedHandler is the inner body of a scoped tool. It receives the
// decoded args plus the resolved tenant/workspace; the scaffold has
// already validated required fields and (when requested) stamped the
// idempotency key onto ctx. Returning a non-nil error routes through
// errorResultOrErr just like the hand-written tools did.
type scopedHandler func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error)

// scopedTool builds a Tool whose handler decodes args, validates the
// declared required string fields, resolves tenant/workspace scope,
// optionally stamps an Idempotency-Key derived from the tool name +
// raw args, and maps client errors through errorResultOrErr. The inner
// func owns the actual request and the success epilogue.
//
// This collapses the decode → required-fields → scoped → idempotency →
// errorResultOrErr boilerplate that every scoped flow tool repeated.
func scopedTool(c *DazydClient, d Defaults, name, description, schema string, required []string, idempotent bool, fn scopedHandler) Tool {
	return Tool{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(schema),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			if _, bad := requireStrings(args, required...); bad != nil {
				return *bad, nil
			}
			tenant, workspace, err := scoped(args, d)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			if idempotent {
				ctx = withIdempotencyKey(ctx, idempotencyKeyFor(name, raw))
			}
			res, err := fn(ctx, c, args, tenant, workspace)
			if err != nil {
				return errorResultOrErr(err)
			}
			return res, nil
		},
	}
}

// flowPathTool covers the most common scoped shape: a single required
// "id", a fixed HTTP verb against /me/flows/{flow_id}{suffix}, and a
// TextResult(out) epilogue. run_flow, get_flow, enable/disable, and
// validate_flow all reduce to this.
//
// verb is one of "GET"/"POST". POST tools default to sending an
// idempotency key; pass idempotent=false for read-only POSTs (e.g.
// validate_flow) that the legacy code deliberately left un-keyed.
func flowPathTool(c *DazydClient, d Defaults, name, description, schema, verb, suffix string, idempotent bool) Tool {
	return scopedTool(c, d, name, description, schema, []string{"id"}, idempotent,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
			path := "/me/flows/" + composeFlowID(tenant, workspace, id) + suffix
			var out map[string]any
			var err error
			switch verb {
			case "GET":
				err = c.Get(ctx, path, &out)
			default: // "POST"
				err = c.Post(ctx, path, nil, &out)
			}
			if err != nil {
				return ToolCallResult{}, err
			}
			return TextResult(out), nil
		})
}

// ─────────────────────────── tool definitions ───────────────────────────

func listIntegrations(c *DazydClient) Tool {
	return Tool{
		Name:        "list_integrations",
		Description: "List every integration the daemon offers, grouped by vendor (Slack, Gmail, GitHub, ...). Each entry includes a one-sentence summary and how many steps it exposes. Use this FIRST when composing a new flow — narrow by integration before drilling into individual steps.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"q":        {"type":"string","description":"Free-text filter against integration label and summary."},
			"category": {"type":"string","description":"Optional category filter: trigger, transformation, io, ai, network, external, system, flow_control."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			qs := buildQuery(map[string]string{
				"q":        stringField(args, "q", ""),
				"category": stringField(args, "category", ""),
			})
			var out map[string]any
			if err := c.Get(ctx, "/catalog/integrations"+qs, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func describeIntegration(c *DazydClient) Tool {
	return Tool{
		Name:        "describe_integration",
		Description: "Return one integration's detail page: its auth shape, every step it exposes with their role (trigger / action / transformation), and example flows. Read this BEFORE describing individual steps — it tells you which step within the integration to look at.",
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

func listDrops(c *DazydClient) Tool {
	return Tool{
		Name:        "list_drops",
		Description: "Search the flat step catalog. Returns lean per-step entries (id, label, summary, category, integration). Use the optional filters to narrow — full per-step detail (params schema, examples) comes from describe_drop. NOTE: the product calls these STEPS everywhere a person can see them; \"drop\" survives only in these tool names and in API field names, so say \"step\" when you talk to the user.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{
			"q":           {"type":"string","description":"Free-text filter against label, description, tags."},
			"category":    {"type":"string"},
			"integration": {"type":"string","description":"Limit to steps in this integration (e.g. 'Slack')."},
			"tag":         {"type":"string"}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			qs := buildQuery(map[string]string{
				"q":           stringField(args, "q", ""),
				"category":    stringField(args, "category", ""),
				"integration": stringField(args, "integration", ""),
				"tag":         stringField(args, "tag", ""),
			})
			var out map[string]any
			if err := c.Get(ctx, "/catalog/drops"+qs, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func describeDrop(c *DazydClient) Tool {
	return Tool{
		Name:        "describe_drop",
		Description: "Get the full manifest of one step — params JSON Schema, worked params examples, I/O ports, execution model, retry policy. THIS is the source of truth when composing the node's params; the examples field gives you concrete shapes to crib from.",
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
func describeTriggerKinds(c *DazydClient) Tool {
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
func listConnections(c *DazydClient) Tool {
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
func startConnection(c *DazydClient) Tool {
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
			qs := buildQuery(map[string]string{
				"account":   stringField(args, "account", ""),
				"return_to": stringField(args, "return_to", ""),
			})
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
// references it via ${secret.NAME}.
func listSecrets(c *DazydClient) Tool {
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
// ${secret.NAME} in any flow node that needs it.
func setSecret(c *DazydClient) Tool {
	return Tool{
		Name:        "set_secret",
		Description: "Store a secret value under the given name in the caller's tenant. Overwrites if the name exists. After calling, reference the secret from a flow node's params as `${secret.NAME}` (the daemon resolves it at run time). Names must be A-Z 0-9 _ . / - .",
		InputSchema: json.RawMessage(`{"type":"object","required":["name","value"],"properties":{
			"name":  {"type":"string","description":"Stable name. Convention: SCREAMING_SNAKE_CASE. Becomes the key flow nodes reference via ${secret.NAME}."},
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
func deleteSecret(c *DazydClient) Tool {
	return Tool{
		Name:        "delete_secret",
		Description: "Permanently remove a secret. Idempotent: missing names succeed silently. Flows that still reference the deleted secret via ${secret.NAME} will fail at run time — pair with list_flows / get_flow before deleting if you're unsure.",
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
func validateCron(c *DazydClient) Tool {
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
func enableFlow(c *DazydClient, d Defaults) Tool {
	return enableOrDisable(c, d, "enable_flow",
		"Re-enable a previously-disabled flow. Idempotent: enabling an enabled flow is a no-op.",
		"/enable")
}
func disableFlow(c *DazydClient, d Defaults) Tool {
	return enableOrDisable(c, d, "disable_flow",
		"Pause a flow without deleting it. Scheduled firings (cron/poll) and inbound webhooks are suppressed; explicit run_flow / test_trigger_flow calls still work. Idempotent.",
		"/disable")
}
func enableOrDisable(c *DazydClient, d Defaults, name, desc, suffix string) Tool {
	return flowPathTool(c, d, name, desc,
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"POST", suffix, true)
}

// publishFlow / unpublishFlow control which version of a flow is LIVE.
// create_flow / update_flow save a DRAFT — the schedule (cron) and inbound
// webhooks/forms fire the most-recently-PUBLISHED version, so a freshly
// created or edited flow does nothing on its triggers until it's published.
// (run_flow / test_trigger_flow run the draft immediately, which is why a flow
// can run on demand yet never fire on its schedule.) These are the missing
// step between "saved" and "actually running on its own".
//
// idempotent=false: re-publishing AFTER an edit is a distinct, intentional act
// (the draft changed). An args-derived key would dedupe the second publish onto
// the first and silently skip activating the new version.
func publishFlow(c *DazydClient, d Defaults) Tool {
	return flowPathTool(c, d, "publish_flow",
		"Publish a flow: make its CURRENT draft the live version, so its schedule (cron) and webhook/form triggers start firing. Call this after create_flow / update_flow whenever the flow has triggers — saving alone leaves it as a draft that only runs via run_flow / test_trigger_flow. Re-publish after each edit you want to go live.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"POST", "/publish", false)
}

func unpublishFlow(c *DazydClient, d Defaults) Tool {
	return flowPathTool(c, d, "unpublish_flow",
		"Unpublish a flow: retire the live version so its schedule and webhook/form triggers stop firing, without deleting the flow or its draft. Use to pause a flow's automatic triggering; re-activate later with publish_flow. (To pause scheduled firings while keeping the published version, disable_flow is the lighter touch.)",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"POST", "/unpublish", false)
}

// connectionSlug mirrors core.ConnectionSlug: lower-case, trim, spaces→dashes.
// Kept local so the MCP server stays a thin HTTP client with no core import.
func connectionSlug(integration string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(integration)), " ", "-")
}

// configureConnection sets up a connection-based integration — the ones that
// use connection FIELDS (SMTP email, ntfy, a custom server endpoint + token)
// rather than the OAuth dance that start_connection drives. It PUTs the field
// values to the daemon, which VERIFIES them against the live service before
// storing (when the integration has a verifier), so bad credentials are
// rejected with the real error instead of silently saved as "connected".
//
// This is the missing counterpart to start_connection: without it, a flow that
// fails at run time with "<X> isn't connected" had no in-chat fix — the user
// had to leave for the web Apps page. Find an integration's field keys via
// describe_integration / list_drops (the `connection_fields` array).
func configureConnection(c *DazydClient) Tool {
	return Tool{
		Name: "configure_connection",
		Description: "Set up a connection-based integration (e.g. Email/SMTP, ntfy) by storing its connection-field values. The daemon verifies the values against the live service before saving when a verifier exists, so bad credentials surface here, not at run time. Use this when a flow fails with '<X> isn't connected'. For OAuth providers (Google, Slack, GitHub) use start_connection instead. Field keys come from the integration's connection_fields (see describe_integration).",
		InputSchema: json.RawMessage(`{"type":"object","required":["integration","values"],"properties":{
			"integration": {"type":"string","description":"Integration name or slug, e.g. 'Email' or 'ntfy'. Case-insensitive; spaces become dashes."},
			"values":      {"type":"object","description":"Connection field values keyed by field key, e.g. {\"host\":\"smtp.example.com\",\"port\":\"587\",\"username\":\"me@example.com\",\"password\":\"...\",\"from\":\"me@example.com\"} or {\"server\":\"https://ntfy.example.com\"}. Omitted secret fields keep their stored value.","additionalProperties":{"type":"string"}}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			integration := stringField(args, "integration", "")
			if strings.TrimSpace(integration) == "" {
				return ErrorResult("integration is required"), nil
			}
			vals, ok := args["values"].(map[string]any)
			if !ok || len(vals) == 0 {
				return ErrorResult("values must be a non-empty object of field key → value"), nil
			}
			// Connection field values are strings; coerce non-strings (a number
			// port, a bool) so the caller can be loose with types.
			sv := make(map[string]string, len(vals))
			keys := make([]string, 0, len(vals))
			for k, v := range vals {
				switch t := v.(type) {
				case string:
					sv[k] = t
				default:
					b, _ := json.Marshal(t)
					sv[k] = string(b)
				}
				keys = append(keys, k)
			}
			slug := connectionSlug(integration)
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("configure_connection", raw))
			if err := c.Put(ctx, "/catalog/integrations/"+pathSegment(slug)+"/connection", map[string]any{"values": sv}, nil); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(map[string]any{"integration": integration, "configured": true, "fields": keys}), nil
		},
	}
}

// deleteFlow removes a flow from the workspace. Refuses with 409 if
// a run is currently in flight on this flow (same lock as save/patch).
// Idempotent on the wire: a missing flow returns 204.
//
// Needs a key with graph:admin — the default `claude-mcp` key is scoped to
// graph:run + graph:edit, so this tool answers 403 admin_scope_required for
// the common setup. That is deliberate (deleting drops the flow's git
// history), and the description says so rather than letting the model
// discover it by failing.
func deleteFlow(c *DazydClient, d Defaults) Tool {
	return scopedTool(c, d, "delete_flow",
		"Permanently remove a flow (this also drops its version history). Use this when the user wants to undo a creation or retire a flow. "+
			"Requires an API key with the graph:admin permission: the default MCP key has graph:run + graph:edit only and gets 403 admin_scope_required — "+
			"when that happens, don't retry, tell the user to delete the flow in the web UI (or to mint a key with graph:admin). "+
			"Refuses (HTTP 409) if a run is currently active on the flow. Idempotent: deleting a missing flow is a no-op.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		[]string{"id"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
			if err := c.Delete(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)); err != nil {
				return ToolCallResult{}, err
			}
			return TextResult(map[string]any{"id": id, "deleted": true}), nil
		})
}

func listFlows(c *DazydClient, d Defaults) Tool {
	return scopedTool(c, d, "list_flows",
		"List flow IDs in a workspace. Use to discover what already exists before creating a new flow.",
		`{"type":"object","properties":{
			"tenant":    {"type":"string","description":"Tenant slug. Defaults to the bearer's tenant."},
			"workspace": {"type":"string","description":"Workspace name. Defaults to the bearer's workspace."}
		}}`,
		nil, false,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			var out map[string]any
			path := "/me/flows" + buildQuery(map[string]string{"tenant": tenant, "workspace": workspace})
			if err := c.Get(ctx, path, &out); err != nil {
				return ToolCallResult{}, err
			}
			return TextResult(out), nil
		})
}

func getFlow(c *DazydClient, d Defaults) Tool {
	return flowPathTool(c, d, "get_flow",
		"Fetch a flow's full graph payload (nodes, edges, triggers, settings) so you can show the user what's there or build an updated version off it.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"GET", "", false)
}

// flowReferences exposes GET /me/flows/{flow_id}/references — the catalogue of
// ${…} placeholder tokens a flow's params can use: every upstream node output
// (e.g. ${upstream.search.messages[0].id}), the trigger body fields
// (${trigger.body.name}), and available secrets/resources. THIS is how to learn
// the exact path to a field before templating it into a param — the shapes are
// non-obvious (a webhook body arrives as a list, fields live under trigger.body,
// etc.), so guessing leads to runtime "field not present" failures that pass
// validation. Call this after wiring nodes, before referencing their data.
func flowReferences(c *DazydClient, d Defaults) Tool {
	return flowPathTool(c, d, "flow_references",
		"List the valid ${…} placeholder tokens for a flow's params: upstream node outputs (e.g. ${upstream.<node>.<port>[0].<field>}), trigger body fields (${trigger.body.<field>}), secrets and resources. Use this to find the EXACT path to a field before putting it in a param — field shapes are non-obvious (a form/webhook body arrives as a list; form fields live under ${trigger.body.…}), and a wrong path passes validation but fails at run time.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"GET", "/references", false)
}

// saveFlow backs both create_flow and update_flow. The wire shape is
// identical — PUT /graphs/{t}/{w}/{id} is idempotent on the server —
// but exposing two distinct tool names lets the LLM (and the user
// watching it) signal intent.
func saveFlow(c *DazydClient, d Defaults, name, description string) Tool {
	return scopedTool(c, d, name, description,
		`{
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
		}`,
		[]string{"id"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
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
			if err := c.Put(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id), body, &out); err != nil {
				return ToolCallResult{}, err
			}
			// Saving stores a DRAFT. If the flow has triggers (cron/webhook/form),
			// they won't fire until it's published — surface that next step so the
			// model doesn't leave a "scheduled" flow silently inactive. run_flow /
			// test_trigger_flow still exercise the draft on demand.
			if eps, ok := out["endpoints"].([]any); ok && len(eps) > 0 {
				out["next_step"] = "Saved as a draft. Call publish_flow with this id to make its triggers (schedule/webhook/form) go live — until then they won't fire on their own (run_flow / test_trigger_flow still work)."
			}
			return TextResult(out), nil
		})
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
func patchFlow(c *DazydClient, d Defaults) Tool {
	return scopedTool(c, d, "patch_flow",
		"Apply a JSON Merge Patch (RFC 7396) to an existing flow. "+
			"Use for incremental edits — the patch body is a sparse subset of the Graph; "+
			"unspecified keys are left alone, nulls delete, arrays replace wholesale. "+
			"Refuses with HTTP 409 if a run of this flow is currently in flight.",
		`{"type":"object","required":["id","patch"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"patch":     {"type":"object","description":"Sparse Graph document. Only keys you want to change. Use null to delete a key."}
		}}`,
		[]string{"id"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
			patch, ok := args["patch"].(map[string]any)
			if !ok {
				return ErrorResult("patch must be a JSON object"), nil
			}
			var out map[string]any
			if err := c.Patch(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id), patch, &out); err != nil {
				return ToolCallResult{}, err
			}
			return TextResult(out), nil
		})
}

// validateFlow lints the flow at HEAD without saving. Lets the LLM
// sanity-check a graph it just authored before running it — catches
// schema mismatches, orphaned nodes, hardcoded secrets, etc. Returns
// the same lint shape SaveFlow appends after a write.
func validateFlow(c *DazydClient, d Defaults) Tool {
	// Read-only operation: no idempotency key needed (the server won't
	// dedupe GET-like calls anyway), hence idempotent=false.
	return flowPathTool(c, d, "validate_flow",
		"Lint a flow (currently saved version) without running it. Returns {ok, issues:[{severity,node,field,message}]}. Use after create_flow / update_flow / patch_flow to verify the saved shape lints clean before triggering a run.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"POST", "/validate", false)
}

// validateGraph dry-runs the lint over a Graph JSON document without
// saving. Use this when composing a flow in chat — pass the candidate
// graph, see if it lints clean, then call create_flow only when
// satisfied. Avoids the create-fix-update churn of authoring against
// HEAD-only validate_flow.
// generateFlowTool wraps the server-side AI generator: plain-English →
// validated DRAFT graph. It's the agentic loop's front door for MCP clients —
// instead of hand-authoring nodes/edges, ask for a draft, then refine it with
// validate_graph / describe_drop / update_flow or persist it with create_flow.
// The draft is NOT saved or run; the server grounds on the live catalog and
// repairs structural problems before returning.
func generateFlowTool(c *DazydClient) Tool {
	return Tool{
		Name: "generate_flow",
		Description: "Draft a flow from a plain-English description using the server-side AI generator (grounded on the live catalog, validated and repaired). Returns {graph, issues} — a DRAFT to refine (validate_graph / describe_drop / update_flow) or persist (create_flow). Requires an AI provider connected on the server; the graph is not saved or run.",
		InputSchema: json.RawMessage(`{"type":"object","required":["description"],"properties":{
			"description": {"type":"string","description":"What the flow should do, in plain English. e.g. 'every weekday at 9am email me yesterday's new signups'."},
			"provider": {"type":"string","description":"Optional AI provider id (e.g. claude, openai). Defaults to the first connected provider."},
			"tz": {"type":"string","description":"Optional IANA timezone for any schedule (e.g. Europe/Stockholm). Defaults to UTC."}
		}}`),
		Handler: func(ctx context.Context, raw json.RawMessage) (ToolCallResult, error) {
			args, err := decodeArgs(raw)
			if err != nil {
				return ErrorResult(err.Error()), nil
			}
			desc := stringField(args, "description", "")
			if strings.TrimSpace(desc) == "" {
				return ErrorResult("description is required"), nil
			}
			body := map[string]any{"description": desc}
			if p := stringField(args, "provider", ""); p != "" {
				body["provider"] = p
			}
			if tz := stringField(args, "tz", ""); tz != "" {
				body["tz"] = tz
			}
			var out map[string]any
			if err := c.Post(ctx, "/tools/flow/generate", body, &out); err != nil {
				return errorResultOrErr(err)
			}
			// The endpoint returns 200 with {error, need_connect} when no provider
			// is connected — surface that as a tool error, not a "successful" blank.
			if msg, ok := out["error"].(string); ok && msg != "" {
				return ErrorResult(msg), nil
			}
			return TextResult(out), nil
		},
	}
}

func validateGraph(c *DazydClient) Tool {
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
func testTriggerFlow(c *DazydClient, d Defaults) Tool {
	// idempotent=false, same reasoning as run_flow: firing the trigger again
	// (even with the SAME payload) is an intentional "test it again", not a
	// retry. A deterministic args-derived key would dedupe the second call onto
	// the first, handing back the prior run's ID — so a re-test silently returns
	// the old (possibly failed) run instead of executing.
	return scopedTool(c, d, "test_trigger_flow",
		"Fire a flow as if a webhook/form trigger had received the supplied JSON payload. Each call starts a NEW run (call it again to re-test, even with the same payload). Returns the run ID. Use this to verify a trigger-driven flow without exposing it to real traffic.",
		`{"type":"object","required":["id","payload"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"payload":   {"description":"JSON payload to seed the trigger node with. Object, array, or primitive."}
		}}`,
		[]string{"id"}, false,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
			body := map[string]any{"payload": args["payload"]}
			var out map[string]any
			if err := c.Post(ctx, "/me/flows/"+composeFlowID(tenant, workspace, id)+"/test-trigger", body, &out); err != nil {
				return ToolCallResult{}, err
			}
			return TextResult(out), nil
		})
}

// sampleNode executes a single node with synthetic input so the LLM
// can answer "what does this node actually emit, given X?" without
// running the whole flow. Useful for debugging during authoring or
// for the LLM to reason about a transformation node's behavior.
func sampleNode(c *DazydClient, d Defaults) Tool {
	return scopedTool(c, d, "sample_node",
		"Run a node plus the chain of nodes feeding it, and return what that node emits — to debug a transformation mid-flow. NOTE: it re-executes the upstream nodes, so if the node depends on a TRIGGER (webhook/form/cron), use test_trigger_flow instead — sampling alone can't synthesize trigger data and will fail with no_trigger_data.",
		`{"type":"object","required":["id","node_id"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"},
			"node_id":   {"type":"string"}
		}}`,
		[]string{"id", "node_id"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
			nodeID := stringField(args, "node_id", "")
			body := map[string]any{}
			if inputs, ok := args["inputs"].(map[string]any); ok {
				body["inputs"] = inputs
			}
			var out map[string]any
			path := "/me/flows/" + composeFlowID(tenant, workspace, id) +
				"/nodes/" + pathSegment(nodeID) + "/sample"
			if err := c.Post(ctx, path, body, &out); err != nil {
				return ToolCallResult{}, err
			}
			return TextResult(out), nil
		})
}

func runFlow(c *DazydClient, d Defaults) Tool {
	// idempotent=false on purpose: run_flow is an explicit "run it now" action,
	// so a SECOND call means "run it AGAIN", not "retry the first call". With a
	// deterministic args-derived key, two identical run_flow calls collapse onto
	// one cached run — the user asks to re-run and silently gets the prior run's
	// ID back. Submitting each call lets re-runs actually fire. (A run is cheap
	// to repeat and the daemon records each separately, so the lost transport-
	// retry dedup is an acceptable trade for correct re-run behaviour.)
	return flowPathTool(c, d, "run_flow",
		"Submit a run of an existing flow. Each call starts a NEW run (call it again to run again). Returns the run ID; pair with wait_for_run or get_run to observe outcome.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"POST", "/run", false)
}

func cancelRun(c *DazydClient) Tool {
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

func getRun(c *DazydClient) Tool {
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

func listRuns(c *DazydClient, d Defaults) Tool {
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

			qs := buildQuery(map[string]string{
				"limit":  strconv.Itoa(limit),
				"status": status,
			})
			var path string
			if flowID != "" {
				tenant, workspace, err := scoped(args, d)
				if err != nil {
					return ErrorResult(err.Error()), nil
				}
				path = "/me/flows/" + composeFlowID(tenant, workspace, flowID) + "/runs" + qs
			} else {
				path = "/me/runs" + qs
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
func waitForRun(c *DazydClient) Tool {
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

func listPendingApprovals(c *DazydClient, d Defaults) Tool {
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
			path := "/approvals/pending" + buildQuery(map[string]string{
				"tenant":    stringField(args, "tenant", d.Tenant),
				"workspace": stringField(args, "workspace", d.Workspace),
			})
			var out map[string]any
			if err := c.Get(ctx, path, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}

func approveNode(c *DazydClient) Tool {
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
			path := "/approvals/" + pathSegment(runID) + "/" + pathSegment(nodeID) +
				buildQuery(map[string]string{
					"decision": decision,
					"comment":  stringField(args, "comment", ""),
				})
			var out map[string]any
			ctx = withIdempotencyKey(ctx, idempotencyKeyFor("approve_node", raw))
			if err := c.Post(ctx, path, nil, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(out), nil
		},
	}
}
