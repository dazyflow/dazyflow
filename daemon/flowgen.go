// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/llm"
)

// Flow generation: "describe a flow in plain English → a draft flow graph".
// The flagship AI feature. It runs server-side through the shared LLM layer
// (internal/llm) using the tenant's connected provider, built around:
//
//  1. Grounding — the model gets a COMPACT CATALOG of the steps that exist
//     (ids, params, ports) and is told to use only those.
//  2. Structured output — it answers via a forced tool whose schema is the
//     flow-graph shape, so we decode JSON, not scrape prose.
//  3. Validate-and-repair — the candidate is linted with the SAME linter the
//     save path uses (core.LintGraph); errors are fed back for the model to
//     fix, bounded by a retry cap.
//  4. Triggers — for scheduled requests it emits a graph-level cron trigger,
//     which we validate with the real cron parser (event/webhook flows use a
//     webhook_input node from the catalog instead — graph triggers are cron
//     only).
//
// Safety: the result is a DRAFT. It is never saved and never run — the editor
// opens it for review.

const (
	flowGenMaxTokens = 4000
	flowGenTimeoutMS = 60000
	maxFlowRepairs   = 2  // up to 3 EMIT attempts (initial + 2 repairs)
	maxAgentTurns    = 12 // hard cap on total agent turns (explore + validate + emit)
)

type genNode struct {
	ID     string         `json:"id"`
	Module string         `json:"module"`
	Params map[string]any `json:"params,omitempty"`
}
type genEdge struct {
	From     string `json:"from"`
	FromPort string `json:"from_port"`
	To       string `json:"to"`
	ToPort   string `json:"to_port"`
}
type genTrigger struct {
	Type string `json:"type"` // "cron" or "none"
	Cron string `json:"cron,omitempty"`
}
type generatedGraph struct {
	Name    string      `json:"name"`
	Nodes   []genNode   `json:"nodes"`
	Edges   []genEdge   `json:"edges"`
	Trigger *genTrigger `json:"trigger,omitempty"`
}

// flowShapeSchema is the JSON-schema for a flow graph (name + nodes + edges +
// trigger). Shared by the agent tool's validate/emit payloads so the model
// only learns one shape.
func flowShapeSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "The flow graph: steps (nodes) wired together (edges), with an optional schedule.",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "A short human title for the flow."},
			"nodes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "string", "description": "Unique id within the flow, e.g. \"fetch\", \"notify\"."},
						"module": map[string]any{"type": "string", "description": "A step id from the catalog. Required."},
						"params": map[string]any{"type": "object", "description": "Parameter values for this step, per its catalog params."},
					},
					"required": []any{"id", "module"},
				},
			},
			"edges": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"from":      map[string]any{"type": "string"},
						"from_port": map[string]any{"type": "string"},
						"to":        map[string]any{"type": "string"},
						"to_port":   map[string]any{"type": "string"},
					},
					"required": []any{"from", "from_port", "to", "to_port"},
				},
			},
			"trigger": map[string]any{
				"type":        "object",
				"description": "How the flow starts. Use type \"cron\" with a 5-field expression for a SCHEDULE (e.g. \"0 9 * * 1-5\" = 9am weekdays). Use \"none\" when the flow starts from an event step (e.g. webhook_input) or runs manually.",
				"properties": map[string]any{
					"type": map[string]any{"type": "string", "enum": []any{"cron", "none"}},
					"cron": map[string]any{"type": "string", "description": "5-field cron (minute hour day-of-month month day-of-week), for type=cron."},
				},
			},
		},
		"required": []any{"nodes"},
	}
}

// flowAgentTool is the single forced tool that drives the build LOOP. Each
// turn the model calls `act` with one action: explore the catalog
// (search_drops / describe_drop), check a draft (validate), or return the
// finished flow (emit). Riding ONE forced tool keeps the loop working on the
// existing provider adapters (single tool_choice + multi-turn messages) — no
// adapter changes needed.
func flowAgentTool() *llm.Tool {
	return &llm.Tool{
		Name:        "act",
		Description: "Take ONE step toward building the flow. Explore steps before wiring them, validate your draft, then emit it.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"search_drops", "describe_drop", "validate", "emit"},
					"description": "search_drops: find steps by keyword. describe_drop: get a step's exact params/ports/examples. validate: check a draft against the real validator. emit: return the finished flow.",
				},
				"query":   map[string]any{"type": "string", "description": "For search_drops: keywords, e.g. \"send email\" or \"google sheet\"."},
				"drop_id": map[string]any{"type": "string", "description": "For describe_drop: the catalog step id to inspect, e.g. \"gmail_search_messages\"."},
				"flow":    flowShapeSchema(),
			},
			"required": []any{"action"},
		},
	}
}

func flowGenSystemPrompt(catalog string) string {
	return "You are a flow architect for an automation tool. A flow is a graph of STEPS " +
		"(nodes) wired by EDGES from an output port to an input port.\n\n" +
		"Build the smallest flow that satisfies the user's request, using ONLY the steps in " +
		"this catalog. Never invent a step id, a param, or a port that isn't listed.\n\n" +
		"Rules:\n" +
		"- Every node.module MUST be an id from the catalog.\n" +
		"- Put only params the step lists; use ${secret.NAME} for credentials — NEVER paste a real key or token.\n" +
		"- Wire edges output→input using exact port names; types must be compatible.\n" +
		"- Satisfy every REQUIRED input (marked ! in the catalog): either wire it, or set a param of the same name.\n" +
		"- Give each node a short unique id. Keep the flow minimal and correct.\n" +
		"- TRIGGER: if the request implies a SCHEDULE (\"every morning\", \"daily\", \"each Monday\"), " +
		"set trigger.type=\"cron\" with the matching 5-field expression. If it starts from an external " +
		"EVENT (a form submission, an incoming email), DON'T set a cron trigger — instead make the first " +
		"step the matching trigger node from the catalog (e.g. webhook_input). Otherwise use trigger.type=\"none\".\n" +
		"- Every turn, answer by calling the `act` tool (see HOW TO WORK below).\n\n" +
		"PATTERNS (compose these from catalog steps — they are not single steps):\n" +
		"- Process a list one item at a time (each email, each row): wire the list into for_each.items; " +
		"wire for_each.body into the per-item step's input; that step's output is collected on for_each.results; " +
		"then add unwrap_results {node:<per-item step id>, port:<that step's output port>} to flatten results into rows.\n" +
		"- gmail_search_messages already returns full email records (from, subject, date, body) on its `messages` " +
		"port — wire that straight into map_rows / render_text. Only reach for gmail_get_message + a for_each when " +
		"you have a bare message id from somewhere else.\n" +
		"- Before sending rows to Slack / SMS / email, turn them into a string with render_text (rows→text). " +
		"Don't wire raw rows or JSON straight into a text/message field.\n\n" +
		"MARKERS — params: NAME(type)* = required param. Ports: name[] = a list/table of rows, " +
		"name* = variadic (accepts multiple wires), name! = a required input you must satisfy.\n\n" +
		"CATALOG (id [category]: what it does | params | in→out ports | e.g. example params):\n" + catalog
}

// renderFlowGenerate is POST /api/v1/tools/flow/generate — the non-streaming
// variant (single JSON response). The editor uses the streaming sibling.
func (h *HTTPGateway) renderFlowGenerate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	var body struct {
		Description string          `json:"description"`
		Provider    string          `json:"provider"`
		TZ          string          `json:"tz"`
		Base        json.RawMessage `json:"base"` // optional: refine this existing flow
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	desc := strings.TrimSpace(body.Description)
	if desc == "" {
		writeJSONError(rw, http.StatusBadRequest, "describe the flow you want")
		return
	}
	desc = refineDesc(body.Base, desc)
	ctx := core.WithTenant(r.Context(), p.Tenant)
	chosen, conn := h.pickProvider(ctx, body.Provider)
	if len(conn) == 0 {
		writeJSON(rw, http.StatusOK, map[string]any{
			"error":        "Connect an AI provider (Claude or ChatGPT) on the Apps page to use this.",
			"need_connect": true,
		})
		return
	}
	mans, err := h.svc.SearchDrops(r.Context(), p, DropSearch{})
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "could not load the step catalog: "+err.Error())
		return
	}
	graph, issues, gerr := h.generateFlow(ctx, chosen.info.Name, chosen.key, desc, mans, p.Tenant, p.Workspace, body.TZ, nil)
	if gerr != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"error": gerr.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"graph": graph, "issues": issues, "provider": chosen.info.Name})
}

// renderFlowGenerateStream is POST /api/v1/tools/flow/generate/stream — the
// editor's experience. It streams progress events (text/event-stream) as the
// generation moves through its phases, then a final "done" (with the graph)
// or "error" frame. Streaming the validate-and-repair phases is what makes
// the feature feel alive instead of a long spinner.
func (h *HTTPGateway) renderFlowGenerateStream(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	var body struct {
		Description string          `json:"description"`
		Provider    string          `json:"provider"`
		TZ          string          `json:"tz"`
		Base        json.RawMessage `json:"base"` // optional: refine this existing flow
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	desc := strings.TrimSpace(body.Description)
	if desc == "" {
		writeJSONError(rw, http.StatusBadRequest, "describe the flow you want")
		return
	}
	desc = refineDesc(body.Base, desc)
	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no")
	rw.WriteHeader(http.StatusOK)
	emit := func(event string, payload any) {
		writeSSE(rw, event, payload)
		flusher.Flush()
	}

	ctx := core.WithTenant(r.Context(), p.Tenant)
	chosen, conn := h.pickProvider(ctx, body.Provider)
	if len(conn) == 0 {
		emit("error", map[string]any{
			"message":      "Connect an AI provider (Claude or ChatGPT) on the Apps page to use this.",
			"need_connect": true,
		})
		return
	}
	mans, err := h.svc.SearchDrops(r.Context(), p, DropSearch{})
	if err != nil {
		emit("error", map[string]any{"message": "could not load the step catalog"})
		return
	}
	onProgress := func(phase, msg string) {
		emit("progress", map[string]any{"phase": phase, "message": msg})
	}
	graph, issues, gerr := h.generateFlow(ctx, chosen.info.Name, chosen.key, desc, mans, p.Tenant, p.Workspace, body.TZ, onProgress)
	if gerr != nil {
		emit("error", map[string]any{"message": gerr.Error()})
		return
	}
	emit("done", map[string]any{"graph": graph, "issues": issues, "provider": chosen.info.Name})
}

// workspaceGrounding builds a short, tenant-specific grounding block so the
// generator drafts against what the user ALREADY has: the apps connected
// (prefer them; anything else needs a connect step) and the secret names that
// already exist (reference an existing ${secret.NAME} rather than invent one).
// Best-effort and nil-safe — returns "" when the secret store isn't wired (the
// unit harness), so it never blocks generation.
func (h *HTTPGateway) workspaceGrounding(ctx context.Context, tenant string) string {
	if tenant == "" || h.EncryptedSecrets == nil {
		return ""
	}
	var b strings.Builder

	connected := make([]string, 0)
	for prov, accts := range h.connectedAccountsByProvider(ctx, tenant) {
		if len(accts) > 0 {
			connected = append(connected, prov)
		}
	}
	sort.Strings(connected)
	if len(connected) > 0 {
		b.WriteString("CONNECTED APPS (ready to use — prefer these; if the flow needs an app NOT listed, still use it but tell the user they'll need to connect it): ")
		b.WriteString(strings.Join(connected, ", "))
		for _, p := range connected {
			if p == "google" {
				b.WriteString(" (google covers Gmail, Google Sheets, Calendar and Drive)")
				break
			}
		}
		b.WriteByte('\n')
	}

	// ListScoped at tenant scope hides reserved namespaces (oauth./conn./ws./
	// flow./cfg:), so this is just the user's own org secrets — exactly the
	// names worth reusing.
	if names, err := h.EncryptedSecrets.ListScoped(ctx, tenant, "", ScopeTenant); err == nil && len(names) > 0 {
		sort.Strings(names)
		const maxSecrets = 25
		if len(names) > maxSecrets {
			names = names[:maxSecrets]
		}
		b.WriteString("EXISTING SECRETS (reference one as ${secret.NAME} when it fits, instead of inventing a new name): ")
		b.WriteString(strings.Join(names, ", "))
		b.WriteByte('\n')
	}

	return strings.TrimRight(b.String(), "\n")
}

// refineDesc turns a plain-English change request into a generation prompt
// that modifies an existing flow rather than rebuilding from scratch. When no
// base flow is supplied it returns the description unchanged. This is what
// powers conversational refine: "make it post to #sales instead" against the
// draft the user just saw.
func refineDesc(base json.RawMessage, desc string) string {
	b := strings.TrimSpace(string(base))
	if b == "" || b == "null" {
		return desc
	}
	return "Here is the CURRENT flow as JSON:\n" + b +
		"\n\nModify it to satisfy this change, keeping everything else intact:\n" + desc
}

// pickProvider resolves the connected providers and selects one (the
// requested name if connected, else the first). Returns the choice + the
// full connected list (len 0 ⇒ none connected).
func (h *HTTPGateway) pickProvider(ctx context.Context, want string) (connectedProvider, []connectedProvider) {
	conn := h.connectedProviders(ctx)
	if len(conn) == 0 {
		return connectedProvider{}, nil
	}
	chosen := conn[0]
	if want != "" {
		for _, c := range conn {
			if c.info.Name == want {
				chosen = c
				break
			}
		}
	}
	return chosen, conn
}

// generateFlow runs the grounded, structured, validate-and-repair loop and
// returns the best graph plus any remaining lint issues. onProgress (nil-safe)
// receives phase updates for the streaming UI.
func (h *HTTPGateway) generateFlow(ctx context.Context, provider, key, desc string, mans []core.Manifest, tenant, workspace, tz string, onProgress func(phase, msg string)) (core.Graph, []core.LintIssue, error) {
	emit := func(phase, msg string) {
		if onProgress != nil {
			onProgress(phase, msg)
		}
	}
	emit("understanding", "Reading your request…")
	sys := flowGenSystemPrompt(compactCatalog(mans))
	// Ground the draft in the user's REALITY: the apps already connected and
	// the secret names that already exist. Without this the model invents
	// flows for unconnected apps and makes up ${secret.NAME}s — the two most
	// common "it generated something I can't run" dead-ends.
	if ws := h.workspaceGrounding(ctx, tenant); ws != "" {
		sys += "\n\nYOUR WORKSPACE — ground the flow in what this user already has:\n" + ws
	}
	manifestByID := make(map[string]core.Manifest, len(mans))
	for _, m := range mans {
		manifestByID[m.ID] = m
	}

	tool := flowAgentTool()
	// The system prompt + loop instructions + task all ride the first user turn:
	// OpenAI ignores Request.System once Messages is set, so the transcript must
	// be self-contained for both adapters.
	messages := []any{map[string]any{
		"role":    "user",
		"content": sys + "\n\n" + flowAgentInstructions() + "\n\nBuild a flow for this request:\n" + desc,
	}}

	var best core.Graph
	var issues []core.LintIssue
	emitAttempts := 0  // emit turns that failed validation — bounded by maxFlowRepairs
	parsedAny := false // did the model ever return a parseable flow?
	emit("drafting", "Choosing steps and wiring them together…")

	for turn := 0; turn < maxAgentTurns; turn++ {
		res, err := llm.Generate(ctx, provider, key, llm.Request{
			Messages: messages, Tool: tool,
			MaxTokens: flowGenMaxTokens, TimeoutMS: flowGenTimeoutMS,
		})
		if err != nil {
			return core.Graph{}, nil, err
		}
		act := res.Tool
		action, _ := act["action"].(string)
		flowMap := actFlow(act)
		// Dual-mode: a bare flow object (has "nodes", no "action") is an emit.
		// Keeps single-shot providers — and every existing test — working.
		if action == "" {
			action = "emit"
			if act["nodes"] != nil {
				flowMap = act
			}
		}

		switch action {
		case "search_drops":
			q := strFromMap(act, "query")
			if q != "" {
				emit("exploring", fmt.Sprintf("Searching steps for %q…", q))
			} else {
				emit("exploring", "Looking through the available steps…")
			}
			agentTurn(&messages, act, searchDropsForModel(mans, q))

		case "describe_drop":
			id := strFromMap(act, "drop_id")
			if id != "" {
				emit("exploring", "Reading "+id+"…")
			} else {
				emit("exploring", "Reading a step's details…")
			}
			agentTurn(&messages, act, describeDropForModel(manifestByID, id))

		case "validate":
			emit("validating", "Double-checking the draft…")
			g, perr := graphFromMap(flowMap)
			if perr != nil {
				agentTurn(&messages, act, "Could not read the flow: "+perr.Error())
				break
			}
			stampGraph(&g, tenant, workspace)
			g, _ = finalizeTriggers(g, tz)
			v := core.ValidateGraphFull(g, manifestByID)
			if hasLintError(v) {
				agentTurn(&messages, act, "The flow is NOT valid yet — fix these:\n"+formatLintErrors(v))
			} else {
				agentTurn(&messages, act, "The flow is valid. Emit it now.")
			}

		default: // "emit"
			emitAttempts++
			emit("validating", "Checking the flow is valid…")
			cand, perr := graphFromMap(flowMap)
			if perr != nil {
				if emitAttempts > maxFlowRepairs {
					if !parsedAny {
						return core.Graph{}, nil, fmt.Errorf("the model didn't return a usable flow — try rephrasing")
					}
					return best, issues, nil
				}
				agentTurn(&messages, act, "That flow couldn't be read ("+perr.Error()+"). Emit a flow with valid nodes and edges.")
				continue
			}
			parsedAny = true
			stampGraph(&cand, tenant, workspace)
			// A bad schedule is stripped (the draft must always save) and surfaced
			// as a warning rather than shipping a trigger that never fires.
			cand, issues = finalizeTriggers(cand, tz)
			// Same two gates the run-time engine uses: the security/placeholder
			// linter and the manifest-level structural validator. Running them
			// HERE means a guessed port or mis-wired for_each is repaired now,
			// not surfaced as a cryptic error the first time the user hits Run.
			checks := core.ValidateGraphFull(cand, manifestByID)
			issues = append(issues, checks...)
			best = cand
			if !hasLintError(checks) {
				return best, issues, nil
			}
			if emitAttempts > maxFlowRepairs {
				return best, issues, nil // best-effort; issues surfaced to the UI
			}
			emit("repairing", "Fixing a couple of issues…")
			agentTurn(&messages, act, "This flow has problems — FIX them, then emit again:\n"+formatLintErrors(checks))
		}
	}
	return best, issues, nil
}

// finalizeTriggers validates any cron trigger with the real parser, stamps
// the user's timezone, and drops an unparseable schedule (returning a warning
// so the user knows to set it in the editor). Returns the graph and any
// trigger warnings.
func finalizeTriggers(g core.Graph, tz string) (core.Graph, []core.LintIssue) {
	if len(g.Triggers) == 0 {
		return g, nil
	}
	kept := g.Triggers[:0]
	var warns []core.LintIssue
	for _, tr := range g.Triggers {
		if tr.Type != "cron" {
			kept = append(kept, tr)
			continue
		}
		if tr.TZ == "" {
			tr.TZ = tz
		}
		if _, err := parseCronInTZ(cronValidator, strings.TrimSpace(tr.Cron), tr.TZ); err != nil {
			warns = append(warns, core.LintIssue{
				Code:     "trigger_dropped",
				Severity: core.LintWarn,
				Message:  "Couldn't set the schedule automatically — open the flow's trigger settings to add it.",
			})
			continue // drop the bad trigger
		}
		kept = append(kept, tr)
	}
	g.Triggers = kept
	return g, warns
}


// formatLintErrors renders the LintError-severity findings as a bullet list for
// the model's repair turn (warnings are advisory and omitted).
func formatLintErrors(issues []core.LintIssue) string {
	var b strings.Builder
	for _, is := range issues {
		if is.Severity != core.LintError {
			continue
		}
		b.WriteString("- ")
		b.WriteString(is.Message)
		if len(is.NodeIDs) > 0 {
			b.WriteString(" (nodes: ")
			b.WriteString(strings.Join(is.NodeIDs, ", "))
			b.WriteString(")")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// flowAgentInstructions tells the model how the build LOOP works: it calls the
// single `act` tool repeatedly — explore, then validate, then emit.
func flowAgentInstructions() string {
	return "HOW TO WORK — build the flow over several steps, each one a call to the `act` tool:\n" +
		"  • describe_drop {drop_id}: BEFORE wiring a step you're unsure of, read its exact params, ports and examples.\n" +
		"  • search_drops {query}: find steps by keyword if the catalog below isn't enough.\n" +
		"  • validate {flow}: check a draft against the real validator and read back any problems.\n" +
		"  • emit {flow}: return the finished flow. Only emit once validate comes back clean.\n" +
		"Good habit: describe the steps you'll use → build → validate → fix → emit. Don't guess a port name — describe_drop it."
}

func actFlow(act map[string]any) map[string]any {
	if f, ok := act["flow"].(map[string]any); ok {
		return f
	}
	return nil
}

func strFromMap(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// agentTurn appends the model's chosen action + the tool's result to the
// transcript as plain text turns. We deliberately don't replay tool_use/
// tool_result blocks — forcing `act` every turn keeps the provider adapters
// unchanged while still giving the model its own decision history.
func agentTurn(messages *[]any, act map[string]any, result string) {
	ab, _ := json.Marshal(act)
	*messages = append(*messages,
		map[string]any{"role": "assistant", "content": string(ab)},
		map[string]any{"role": "user", "content": result + "\n\nContinue by calling act again."},
	)
}

// searchDropsForModel returns catalog lines whose id/summary/category/integration
// match every query token — the in-loop search_drops result.
func searchDropsForModel(mans []core.Manifest, query string) string {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return "Provide a query, e.g. {\"action\":\"search_drops\",\"query\":\"send email\"}."
	}
	tokens := strings.Fields(q)
	matched := make([]core.Manifest, 0)
	for _, m := range mans {
		hay := strings.ToLower(m.ID + " " + m.Summary + " " + m.Category + " " + m.Integration)
		ok := true
		for _, t := range tokens {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		return "No steps matched \"" + query + "\". Try different keywords, or use the catalog above."
	}
	const maxHits = 20
	if len(matched) > maxHits {
		matched = matched[:maxHits]
	}
	return "Matching steps:\n" + compactCatalog(matched)
}

// describeDropForModel renders one drop's full spec — params (type/required/
// description), in/out ports (flags, MIME, label) and worked examples — the
// in-loop describe_drop result. This is the rich grounding the compact catalog
// can't carry for every step at once.
func describeDropForModel(byID map[string]core.Manifest, id string) string {
	m, ok := byID[strings.TrimSpace(id)]
	if !ok {
		return "No step with id \"" + id + "\". Use search_drops or the catalog to find the right id."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] — %s\n", m.ID, m.Category, m.Summary)

	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(m.ParamsSchema, &schema)
	req := map[string]bool{}
	for _, r := range schema.Required {
		req[r] = true
	}
	if len(schema.Properties) > 0 {
		names := make([]string, 0, len(schema.Properties))
		for n := range schema.Properties {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("  params:\n")
		for _, n := range names {
			p := schema.Properties[n]
			star := ""
			if req[n] {
				star = " (required)"
			}
			fmt.Fprintf(&b, "    - %s: %s%s — %s\n", n, p.Type, star, p.Description)
		}
	}
	renderPort := func(p core.Port) string {
		s := p.Port
		flags := make([]string, 0, 3)
		if p.Required {
			flags = append(flags, "required")
		}
		if p.List {
			flags = append(flags, "list")
		}
		if p.Variadic {
			flags = append(flags, "variadic")
		}
		if len(flags) > 0 {
			s += " (" + strings.Join(flags, ",") + ")"
		}
		if p.Label != "" {
			s += " — " + p.Label
		}
		if len(p.MIME) > 0 {
			s += "  [" + strings.Join(p.MIME, " ") + "]"
		}
		return s
	}
	if len(m.Inputs) > 0 {
		b.WriteString("  inputs:\n")
		for _, p := range m.Inputs {
			fmt.Fprintf(&b, "    < %s\n", renderPort(p))
		}
	}
	if len(m.Outputs) > 0 {
		b.WriteString("  outputs:\n")
		for _, p := range m.Outputs {
			fmt.Fprintf(&b, "    > %s\n", renderPort(p))
		}
	}
	for _, ex := range m.Examples {
		fmt.Fprintf(&b, "  e.g. %s: %s", ex.Title, compactExample([]core.ParamsExample{ex}))
		if ex.Notes != "" {
			fmt.Fprintf(&b, "  // %s", ex.Notes)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// graphFromMap converts a flow-shaped map (the emit/validate payload, or a bare
// single-shot flow) into a core.Graph.
func graphFromMap(m map[string]any) (core.Graph, error) {
	raw, _ := json.Marshal(m)
	var gg generatedGraph
	if err := json.Unmarshal(raw, &gg); err != nil {
		return core.Graph{}, fmt.Errorf("not valid flow JSON: %w", err)
	}
	if len(gg.Nodes) == 0 {
		return core.Graph{}, fmt.Errorf("no steps in the flow")
	}
	g := core.Graph{Name: gg.Name}
	for _, n := range gg.Nodes {
		g.Nodes = append(g.Nodes, core.Node{ID: n.ID, Module: n.Module, Params: n.Params})
	}
	for _, e := range gg.Edges {
		g.Edges = append(g.Edges, core.Edge{From: e.From, FromPort: e.FromPort, To: e.To, ToPort: e.ToPort})
	}
	if gg.Trigger != nil && gg.Trigger.Type == "cron" && strings.TrimSpace(gg.Trigger.Cron) != "" {
		g.Triggers = []core.GraphTrigger{{Type: "cron", Cron: strings.TrimSpace(gg.Trigger.Cron)}}
	}
	return g, nil
}

func generatedFromGraph(g core.Graph) generatedGraph {
	out := generatedGraph{Name: g.Name}
	for _, n := range g.Nodes {
		out.Nodes = append(out.Nodes, genNode{ID: n.ID, Module: n.Module, Params: n.Params})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, genEdge{From: e.From, FromPort: e.FromPort, To: e.To, ToPort: e.ToPort})
	}
	if len(g.Triggers) > 0 && g.Triggers[0].Type == "cron" {
		out.Trigger = &genTrigger{Type: "cron", Cron: g.Triggers[0].Cron}
	}
	return out
}

func stampGraph(g *core.Graph, tenant, workspace string) {
	g.Tenant = tenant
	g.Workspace = workspace
	if strings.TrimSpace(g.Name) == "" {
		g.Name = "AI-generated flow"
	}
	for i := range g.Nodes {
		if strings.TrimSpace(g.Nodes[i].ID) == "" {
			g.Nodes[i].ID = fmt.Sprintf("step_%d", i+1)
		}
	}
}

// compactCatalog renders the registered steps as a token-efficient catalog
// the model grounds on: one line per step with id, category, summary, params
// (name + type, * = required), and input→output ports.
func compactCatalog(mans []core.Manifest) string {
	rows := make([]string, 0, len(mans))
	for _, m := range mans {
		if m.ID == "" {
			continue
		}
		var b strings.Builder
		b.WriteString(m.ID)
		if m.Category != "" {
			b.WriteString(" [" + m.Category + "]")
		}
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(m.Summary))
		if ps := compactParams(m.ParamsSchema); ps != "" {
			b.WriteString(" | params: " + ps)
		}
		if in := compactPorts(m.Inputs); in != "" {
			b.WriteString(" | in: " + in)
		}
		if out := compactPorts(m.Outputs); out != "" {
			b.WriteString(" | out: " + out)
		}
		if ex := compactExample(m.Examples); ex != "" {
			b.WriteString(" | e.g. " + ex)
		}
		rows = append(rows, b.String())
	}
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

func compactPorts(ports []core.Port) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		s := p.Port
		if p.List {
			s += "[]"
		}
		if p.Variadic {
			s += "*"
		}
		// Required marks an input the flow MUST satisfy (wire or same-named
		// param). The model can't see this from the param list alone — many
		// required values arrive only as a wired input — so surface it here.
		if p.Required {
			s += "!"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ",")
}

// compactExample renders the first worked example's params as a single-line
// JSON snippet the model can copy and adjust — the richest grounding signal,
// already mandatory on every manifest at registration. Truncated to keep the
// catalog token-efficient; empty when the params don't parse.
func compactExample(exs []core.ParamsExample) string {
	if len(exs) == 0 || len(exs[0].Params) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(exs[0].Params, &v); err != nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	// Some manifest examples ship REPLACE_WITH_<X> fill-me markers (sheet IDs,
	// Drive folder ids). The generate loop's linter treats that exact token as
	// a hard error, so grounding the model on it would teach it to emit a marker
	// the same pipeline rejects — wasted repair passes it can never resolve.
	// Humanize it to a benign "your-x" hint that still signals "fill this in".
	s := replaceWithMarker.ReplaceAllStringFunc(string(b), func(m string) string {
		sub := replaceWithMarker.FindStringSubmatch(m)
		return strings.ToLower(strings.ReplaceAll(sub[1], "_", "-"))
	})
	const maxExampleLen = 200
	if len(s) > maxExampleLen {
		s = s[:maxExampleLen] + "…"
	}
	return s
}

// replaceWithMarker matches the REPLACE_WITH_<TOKEN> fill-me placeholders that
// ship inside some manifest examples (mirrors core.lint's pattern).
var replaceWithMarker = regexp.MustCompile(`REPLACE_WITH_([A-Z0-9_]+)`)

func compactParams(schema json.RawMessage) string {
	if len(schema) == 0 {
		return ""
	}
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Properties) == 0 {
		return ""
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	names := make([]string, 0, len(s.Properties))
	for n := range s.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	const maxParams = 10
	parts := make([]string, 0, len(names))
	for i, n := range names {
		if i >= maxParams {
			parts = append(parts, "…")
			break
		}
		t := s.Properties[n].Type
		if t == "" {
			t = "any"
		}
		piece := n + "(" + t + ")"
		if req[n] {
			piece += "*"
		}
		parts = append(parts, piece)
	}
	return strings.Join(parts, ", ")
}
