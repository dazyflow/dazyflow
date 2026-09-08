// SPDX-FileCopyrightText: 2026 Angels' Ware
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

// So an LLM retrying after a network blip hits the gateway's cached 2xx instead
// of firing the action twice. Hashed in CANONICAL form: a host that re-serializes
// the arguments between attempts would otherwise produce a different key for the
// same call, in exactly the scenario the key exists to make safe.
func idempotencyKeyFor(toolName string, args json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0}) // separator so {"name":"x","args":"y"} ≠ {"name":"xy","args":""}
	h.Write(canonicalJSON(args))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// UseNumber keeps numeric literals as source text: decoding into float64 maps two
// DIFFERENT large int64 arguments onto one value, and a false idempotency match
// silently suppresses a distinct action.
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

type Defaults struct {
	Tenant    string
	Workspace string
}

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

// The spec separates "tool couldn't be invoked" from "tool ran and failed".
func errorResultOrErr(err error) (ToolCallResult, error) {
	if err == nil {
		return ToolCallResult{}, nil
	}
	var herr *HTTPError
	// Any HTTP status becomes a tool error the model can read, not a -32603 the host
	// cannot surface. Only genuine transport failures are RPC errors.
	if errors.As(err, &herr) && herr.Status >= 400 {
		payload := herr.ToToolPayload()
		b, mErr := json.MarshalIndent(payload, "", "  ")
		if mErr != nil {
			return ErrorResult(fmt.Sprintf("daemon returned %d: %s", herr.Status, herr.Message)), nil
		}
		return ToolCallResult{
			IsError: true,
			Content: []ContentItem{{Type: "text", Text: string(b)}},
		}, nil
	}
	return ToolCallResult{}, err
}

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

type scopedHandler func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error)

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

func describeTriggerKinds(c *DazydClient) Tool {
	return Tool{
		Name:        "describe_trigger_kinds",
		Description: "Return the schema for every supported GraphTrigger kind (cron, webhook, poll), with per-field descriptions and worked examples. Consult this when composing a flow that needs to fire on a schedule. Inbound HTTP is configured on steps rather than here: webhook_input, request_input (answered by a reply step) and form_input.",
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

// Automatic triggers run the published revision; manual runs keep using HEAD.
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

func connectionSlug(integration string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(integration)), " ", "-")
}

func configureConnection(c *DazydClient) Tool {
	return Tool{
		Name:        "configure_connection",
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

// Refuses while a run is active: the run pins the revision it started with.
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

func saveFlow(c *DazydClient, d Defaults, name, description string) Tool {
	return scopedTool(c, d, name, description,
		`{
			"type":"object",
			"required":["id","nodes"],
			"properties":{
				"id":              {"type":"string","pattern":"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$","description":"Flow ID — stable handle used by run / trigger URLs. Letters, digits, '-', '_' and '.', starting with a letter or digit (e.g. 'chase-overdue-invoices'): it becomes a filename and a git tag, so anything else is refused."},
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
					"description":"Optional cron/webhook triggers — see GraphTrigger. Identical schedules collapse into one, so listing a schedule twice fires once.",
					"maxItems": 32,
					"items": {"type":"object"}
				}
			}
		}`,
		[]string{"id"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			id := stringField(args, "id", "")
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
			// Saving stores a DRAFT; nothing fires until it is published.
			if eps, ok := out["endpoints"].([]any); ok && len(eps) > 0 {
				out["next_step"] = "Saved as a draft. Call publish_flow with this id to make its triggers (schedule/webhook/form) go live — until then they won't fire on their own (run_flow / test_trigger_flow still work)."
			}
			return TextResult(out), nil
		})
}

// RFC 7396 merge semantics: a null value DELETES a key.
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

func validateFlow(c *DazydClient, d Defaults) Tool {
	return flowPathTool(c, d, "validate_flow",
		"Lint a flow (currently saved version) without running it. Returns {ok, issues:[{severity,node,field,message}]}. Use after create_flow / update_flow / patch_flow to verify the saved shape lints clean before triggering a run.",
		`{"type":"object","required":["id"],"properties":{
			"id":        {"type":"string"},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		"POST", "/validate", false)
}

func generateFlowTool(c *DazydClient) Tool {
	return Tool{
		Name:        "generate_flow",
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

func testTriggerFlow(c *DazydClient, d Defaults) Tool {
	// Firing the trigger again is a new action, not a retry.
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
	// An explicit "run it now" is a new action, not a retry to dedupe.
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
					// The last snapshot beats an error: the steps stay described.
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
	v, _ := rec["status"].(string)
	switch v {
	case "succeeded", "failed", "cancelled", "skipped":
		return true
	}
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
