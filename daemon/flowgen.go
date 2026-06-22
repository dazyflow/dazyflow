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
	maxFlowRepairs   = 2 // up to 3 LLM calls (initial + 2 repairs)
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

func flowGenTool() *llm.Tool {
	return &llm.Tool{
		Name:        "emit_flow",
		Description: "Return the flow as a graph of steps (nodes) wired together (edges), with an optional schedule.",
		Schema: map[string]any{
			"type": "object",
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
		"- Answer ONLY by calling the emit_flow tool.\n\n" +
		"PATTERNS (compose these from catalog steps — they are not single steps):\n" +
		"- Process a list one item at a time (each email, each row): wire the list into for_each.items; " +
		"wire for_each.body into the per-item step's input; that step's output is collected on for_each.results; " +
		"then add unwrap_results {node:<per-item step id>, port:<that step's output port>} to flatten results into rows.\n" +
		"- gmail_search_messages returns only message IDs on its `messages` port. To get sender/subject/body you " +
		"MUST fetch each id with gmail_get_message inside a for_each (the loop pattern above).\n" +
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
		Description string `json:"description"`
		Provider    string `json:"provider"`
		TZ          string `json:"tz"`
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
		Description string `json:"description"`
		Provider    string `json:"provider"`
		TZ          string `json:"tz"`
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
	userText := "Build a flow for this request:\n" + desc
	tool := flowGenTool()

	manifestByID := make(map[string]core.Manifest, len(mans))
	for _, m := range mans {
		manifestByID[m.ID] = m
	}

	var best core.Graph
	var issues []core.LintIssue
	for attempt := 0; attempt <= maxFlowRepairs; attempt++ {
		if attempt == 0 {
			emit("drafting", "Choosing steps and wiring them together…")
		} else {
			emit("repairing", fmt.Sprintf("Fixing a couple of issues… (pass %d)", attempt+1))
		}
		res, err := llm.Generate(ctx, provider, key, llm.Request{
			System: sys, UserText: userText, Tool: tool,
			MaxTokens: flowGenMaxTokens, TimeoutMS: flowGenTimeoutMS,
		})
		if err != nil {
			return core.Graph{}, nil, err
		}
		cand, perr := graphFromResult(res)
		if perr != nil {
			if attempt == maxFlowRepairs {
				return core.Graph{}, nil, fmt.Errorf("the model didn't return a usable flow — try rephrasing")
			}
			userText = fmt.Sprintf("Build a flow for this request:\n%s\n\nYour previous answer could not be read (%v). Answer ONLY via emit_flow with valid nodes and edges.", desc, perr)
			continue
		}
		stampGraph(&cand, tenant, workspace)
		emit("validating", "Checking the flow is valid…")
		// Validate the schedule with the real cron parser and stamp the
		// timezone; a bad schedule is stripped (the draft must always save)
		// and surfaced as a warning rather than silently shipping a trigger
		// that never fires.
		cand, issues = finalizeTriggers(cand, tz)
		// Two checks feed the repair loop: the security/placeholder linter
		// (core.LintGraph) AND the manifest-level structural validator
		// (unknown modules, nonexistent ports, MIME-incompatible wiring,
		// unconnected required inputs, fan-in/variadic bounds). The latter is
		// exactly what the engine runs before execution — running it HERE means
		// the model's most common mistakes (a guessed port, a mis-wired
		// for_each) are fed back for repair instead of surfacing as a cryptic
		// error the first time the user presses Run.
		checks := core.LintGraph(cand)
		checks = append(checks, manifestValidationIssues(cand, manifestByID)...)
		issues = append(issues, checks...)
		best = cand
		if !hasLintError(checks) {
			return best, issues, nil
		}
		if attempt == maxFlowRepairs {
			break
		}
		userText = repairPrompt(desc, cand, checks)
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

// manifestValidationIssues runs the SAME manifest-level validator the engine
// runs before execution (unknown module, nonexistent ports, MIME mismatch,
// unconnected required inputs, fan-in/variadic bounds) and converts each
// finding into a repairable LintError so the generate loop can feed it back to
// the model. Returns nil when no catalog was supplied — manifest validation is
// impossible without it, and the unit tests exercise the loop with none.
func manifestValidationIssues(g core.Graph, manifests map[string]core.Manifest) []core.LintIssue {
	if len(manifests) == 0 {
		return nil
	}
	err := core.ValidateWithManifests(g, manifests)
	if err == nil {
		return nil
	}
	// errors.Join exposes the individual problems via Unwrap() []error — surface
	// each as its own issue so the repair prompt lists them one per line.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		out := make([]core.LintIssue, 0, len(joined.Unwrap()))
		for _, e := range joined.Unwrap() {
			out = append(out, core.LintIssue{Code: "invalid_structure", Severity: core.LintError, Message: e.Error()})
		}
		return out
	}
	return []core.LintIssue{{Code: "invalid_structure", Severity: core.LintError, Message: err.Error()}}
}

func repairPrompt(desc string, g core.Graph, issues []core.LintIssue) string {
	draft, _ := json.Marshal(generatedFromGraph(g))
	var errs strings.Builder
	for _, is := range issues {
		if is.Severity != core.LintError {
			continue
		}
		errs.WriteString("- " + is.Message)
		if len(is.NodeIDs) > 0 {
			errs.WriteString(" (nodes: " + strings.Join(is.NodeIDs, ", ") + ")")
		}
		errs.WriteByte('\n')
	}
	return fmt.Sprintf(
		"Build a flow for this request:\n%s\n\nYour previous draft was:\n%s\n\n"+
			"It has these problems — FIX them and return a corrected flow via emit_flow:\n%s",
		desc, draft, errs.String())
}

func graphFromResult(res llm.Result) (core.Graph, error) {
	var raw []byte
	if len(res.Tool) > 0 {
		raw, _ = json.Marshal(res.Tool)
	} else {
		txt := stripCodeFences(strings.TrimSpace(res.Text))
		if txt == "" {
			return core.Graph{}, fmt.Errorf("empty response")
		}
		raw = []byte(txt)
	}
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
