// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// flowgen_sim_test evaluates the AI flow-generator WITHOUT a real LLM/API key.
// It has two halves:
//
//   - Regression tests (always run): they drive the real generateFlow loop with
//     a scripted provider and the REAL drop catalog, asserting that the
//     manifest-level structural gate now feeds errors back for repair, and that
//     the enriched grounding (examples + required-input markers + patterns)
//     stays in the catalog/system prompt.
//
//   - A manual eval (TestFlowGenEval, skipped unless FLOWGEN_DUMP=1): dumps the
//     exact catalog the model is grounded on and scores hand-authored
//     "what the model would emit" graphs through all three production gates.
//     Run it with:  FLOWGEN_DUMP=1 go test ./daemon -run TestFlowGenEval -v

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/llm"

	_ "github.com/dazyflow/dazyflow/drops" // register every built-in drop
)

func allManifests() []core.Manifest {
	m := engine.Default.Manifests()
	out := make([]core.Manifest, 0, len(m))
	for _, man := range m {
		out = append(out, man)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// manifestMap mirrors what production hands the generator and the validator:
// SearchDrops → listDrops → the resolver's ManifestsForTenant, which stamps
// every processing drop with the universal `pass` pin and marks the
// list-carrying ports. Validating against the bare registry instead would
// reject a graph that legitimately sequences a step through its pass pin —
// which the engine resolves fine at run time.
func manifestMap() map[string]core.Manifest {
	out := map[string]core.Manifest{}
	for id, m := range engine.Default.Manifests() {
		out[id] = core.MarkListPorts(core.WithPassthrough(m))
	}
	return out
}

// describeDrop delegates to the production renderer (describeDropForModel) so
// the manual dump shows exactly what the agentic loop hands the model.
func describeDrop(id string) string { return describeDropForModel(manifestMap(), id) }

// TestShippedTemplatesValidate guards every template the gallery ships: each
// must pass the manifest-level validator (the gate the engine runs before a
// run). This is what would have caught the redundant/mis-shaped gmail
// templates. The template_placeholder LINT (REPLACE_WITH_…) is intentional on
// templates and is not checked here — that's a fill-me marker, not a wiring bug.
func TestShippedTemplatesValidate(t *testing.T) {
	dir := "../web/public/templates"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("templates dir not found (%v)", err)
	}
	mm := manifestMap()
	seen := 0
	for _, e := range entries {
		if e.IsDir() || e.Name() == "index.json" || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var g core.Graph
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Errorf("%s: not valid graph JSON: %v", e.Name(), err)
			continue
		}
		if err := core.ValidateWithManifests(g, mm); err != nil {
			t.Errorf("%s: fails manifest validation (won't run):\n%v", e.Name(), err)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no template files validated")
	}
	t.Logf("validated %d shipped templates", seen)
}

// TestAgenticDescribe dumps describe_drop for the drops the scenario set
// touches — the real grounding I read to drive the loop by hand.
// FLOWGEN_DUMP=1 go test ./daemon -run TestAgenticDescribe -v
func TestAgenticDescribe(t *testing.T) {
	if os.Getenv("FLOWGEN_DUMP") == "" {
		t.Skip("set FLOWGEN_DUMP=1 to dump describe_drop output")
	}
	for _, id := range []string{
		"sheets_read_range", "render_text", "gmail_send_email", "email_send",
		"gmail_search_messages", "map_rows", "sheets_append_row",
		"webhook_input", "slack_send_message",
		"stripe_on_payment_failed", "twilio_send_sms",
		"postgres_upsert_rows", "postgres_query",
	} {
		fmt.Printf("\n==== describe_drop(%q) ====\n%s", id, describeDrop(id))
	}
}

func edge(from, fromPort, to, toPort string) map[string]any {
	return map[string]any{"from": from, "from_port": fromPort, "to": to, "to_port": toPort}
}

// miswiredForEach is the most-likely "model reached for for_each but mis-wired
// it" graph: it wires the loop body/results straight on without unwrap_results,
// producing MIME-incompatible edges. core.LintGraph alone calls it clean; only
// the manifest validator catches it.
func miswiredForEach() map[string]any {
	return map[string]any{
		"name": "log emails to sheet",
		"nodes": []any{
			node("search", "gmail_search_messages", map[string]any{"query": "is:unread"}),
			node("loop", "for_each", map[string]any{}),
			node("get", "gmail_get_message", map[string]any{"id": "${item.id}"}),
			node("append", "sheets_append_row", map[string]any{"spreadsheet_id": "abc", "range": "Inbox"}),
		},
		"edges": []any{
			edge("search", "messages", "loop", "items"),
			edge("loop", "body", "get", "id"),
			edge("loop", "results", "append", "rows"),
		},
	}
}

// TestFlowGen_StructuralGateRepairs: a mis-wired draft (clean to LintGraph, but
// rejected by the manifest validator) must now trigger a repair, and the loop
// must accept the corrected second answer. On the OLD loop (LintGraph only)
// this draft was returned as-is after a single call — so this pins the fix.
func TestFlowGen_StructuralGateRepairs(t *testing.T) {
	good := map[string]any{"name": "ok", "nodes": []any{node("a", "text", map[string]any{"text": "hello"})}}
	sp := &scriptedProvider{graphs: []map[string]any{miswiredForEach(), good}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowstruct", Integration: "FakeFlowStruct", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.flowAPI().generateFlow(context.Background(), "fakeflowstruct", "key",
		"log my new emails to a google sheet", allManifests(), "t1", "main", "", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if sp.calls != 2 {
		t.Fatalf("expected one structural repair (2 calls), got %d", sp.calls)
	}
	if hasLintError(issues) {
		t.Fatalf("final graph still has errors: %+v", issues)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Module != "text" {
		t.Fatalf("expected the repaired (good) graph, got %+v", g.Nodes)
	}
}

// TestFlowGen_StructuralGateSurfaces: when the model never fixes the structural
// problem, the loop exhausts its repairs and returns the best-effort graph with
// the manifest errors surfaced (so the UI/editor can show them) — instead of
// the old behaviour where the broken draft sailed through and only blew up at
// run time.
func TestFlowGen_StructuralGateSurfaces(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{miswiredForEach()}} // always mis-wired
	llm.Register(llm.ProviderInfo{Name: "fakeflowstruckstuck", Integration: "FakeFlowStuckStruct", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	_, issues, err := h.gw.flowAPI().generateFlow(context.Background(), "fakeflowstruckstuck", "key",
		"log my emails", allManifests(), "t1", "main", "", nil)
	if err != nil {
		t.Fatalf("should return best-effort, not error: %v", err)
	}
	if sp.calls != maxFlowRepairs+1 {
		t.Fatalf("expected %d calls (initial + repairs), got %d", maxFlowRepairs+1, sp.calls)
	}
	var sawStructural bool
	for _, is := range issues {
		if is.Code == "invalid_structure" {
			sawStructural = true
		}
	}
	if !sawStructural || !hasLintError(issues) {
		t.Fatalf("expected surfaced invalid_structure error, got %+v", issues)
	}
}

// goodEmailFlow is the simple, correct gmail→sheet flow (search returns full
// records, so no for_each) wrapped as an emit action.
func goodEmailFlow() map[string]any {
	return map[string]any{
		"name": "log emails to sheet",
		"nodes": []any{
			node("poll", "poll_trigger", map[string]any{"interval_seconds": 300}),
			node("search", "gmail_search_messages", map[string]any{"query": "is:unread"}),
			node("shape", "map_rows", map[string]any{"select": []any{"from", "subject", "date"}}),
			node("append", "sheets_append_row", map[string]any{"spreadsheet_id": "abc", "range": "Inbox"}),
		},
		"edges": []any{
			edge("search", "messages", "shape", "rows"),
			edge("shape", "rows", "append", "rows"),
		},
	}
}

// TestFlowGen_AgentExploresThenEmits: the model calls describe_drop (a helper
// turn that must NOT count as an emit attempt), then emits a clean flow. Proves
// the agentic dispatch works and exploring is free of the repair budget.
func TestFlowGen_AgentExploresThenEmits(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{
		{"action": "describe_drop", "drop_id": "gmail_search_messages"},
		{"action": "search_drops", "query": "google sheet"},
		{"action": "emit", "flow": goodEmailFlow()},
	}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowagent", Integration: "FakeFlowAgent", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.flowAPI().generateFlow(context.Background(), "fakeflowagent", "key",
		"log my new emails to a google sheet", allManifests(), "t1", "main", "", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if sp.calls != 3 {
		t.Fatalf("expected describe + search + emit (3 calls), got %d", sp.calls)
	}
	if hasLintError(issues) {
		t.Fatalf("final graph has errors: %+v", issues)
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("expected the emitted 4-node flow, got %d nodes", len(g.Nodes))
	}
}

// TestFlowGen_AgentValidatesBeforeEmit: the model validates a mis-wired draft
// (gets the structural errors back), then emits a corrected flow. The validate
// turn isn't an emit, so it doesn't burn the repair budget.
func TestFlowGen_AgentValidatesBeforeEmit(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{
		{"action": "validate", "flow": miswiredForEach()},
		{"action": "emit", "flow": goodEmailFlow()},
	}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowvalidate", Integration: "FakeFlowValidate", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.flowAPI().generateFlow(context.Background(), "fakeflowvalidate", "key",
		"log my emails", allManifests(), "t1", "main", "", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if sp.calls != 2 {
		t.Fatalf("expected validate + emit (2 calls), got %d", sp.calls)
	}
	if hasLintError(issues) || len(g.Nodes) != 4 {
		t.Fatalf("expected clean 4-node flow, got %d nodes, issues=%+v", len(g.Nodes), issues)
	}
}

// TestRefineDesc: conversational refine seeds the prompt with the current flow
// only when a base is supplied; otherwise the description passes through.
func TestRefineDesc(t *testing.T) {
	if got := refineDesc(nil, "do x"); got != "do x" {
		t.Errorf("nil base should pass through, got %q", got)
	}
	if got := refineDesc([]byte("null"), "do x"); got != "do x" {
		t.Errorf("null base should pass through, got %q", got)
	}
	out := refineDesc([]byte(`{"nodes":[{"id":"a","module":"text"}]}`), "post to #sales instead")
	for _, want := range []string{"CURRENT flow", "post to #sales instead", "\"module\":\"text\""} {
		if !strings.Contains(out, want) {
			t.Errorf("refine prompt missing %q in:\n%s", want, out)
		}
	}
}

// TestFlowGen_GroundingEnriched pins the grounding improvements: the catalog
// carries worked examples and required-input markers, and the system prompt
// teaches the compose-only patterns (for_each/unwrap_results) and the markers.
func TestFlowGen_GroundingEnriched(t *testing.T) {
	cat := compactCatalog(allManifests())
	if !strings.Contains(cat, "| e.g. ") {
		t.Error("catalog is missing worked examples (| e.g. …)")
	}
	if !strings.Contains(cat, "to!") { // gmail_send_email / twilio etc. required input
		t.Error("catalog is missing required-input markers (port!)")
	}
	if strings.Contains(cat, "REPLACE_WITH_") {
		t.Error("catalog examples still contain REPLACE_WITH_ markers the linter rejects")
	}
	sys := flowGenSystemPrompt(cat)
	for _, want := range []string{"unwrap_results", "for_each.items", "render_text", "name! = a required input"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing pattern/marker guidance: %q", want)
		}
	}
}

// TestFlowGen_WorkspaceGrounding: the generator grounds on the tenant's
// connected apps and existing secret names, so it stops inventing flows for
// unconnected apps and made-up ${secret.NAME}s.
func TestFlowGen_WorkspaceGrounding(t *testing.T) {
	h := newGatewayHarness(t)
	es := newMemSecrets(t)
	h.gw.EncryptedSecrets = es
	ctx := context.Background()
	// A connected Slack account (stored as the oauth.<provider>.<account> name
	// connectedAccountsByProvider scans for) and a user-defined org secret.
	if err := es.Put(ctx, "t1", "oauth.slack.default", "xoxb-test"); err != nil {
		t.Fatal(err)
	}
	if err := es.PutScoped(ctx, "t1", "", ScopeTenant, "OPENAI_KEY", "sk-test"); err != nil {
		t.Fatal(err)
	}

	g := h.gw.flowAPI().workspaceGrounding(ctx, "t1")
	if !strings.Contains(g, "CONNECTED APPS") || !strings.Contains(g, "slack") {
		t.Errorf("expected connected app slack in grounding, got: %q", g)
	}
	if !strings.Contains(g, "OPENAI_KEY") {
		t.Errorf("expected existing secret OPENAI_KEY in grounding, got: %q", g)
	}
	// A tenant with no store wired must not error or block generation.
	if got := (&HTTPGateway{}).flowAPI().workspaceGrounding(ctx, "t1"); got != "" {
		t.Errorf("no-store grounding should be empty, got %q", got)
	}
}

// ---- manual eval (skipped unless FLOWGEN_DUMP=1) -------------------------

func TestFlowGenEval(t *testing.T) {
	if os.Getenv("FLOWGEN_DUMP") == "" {
		t.Skip("set FLOWGEN_DUMP=1 to dump the catalog + score scenarios")
	}
	mans := allManifests()
	fmt.Printf("\n===== COMPACT CATALOG (%d drops) =====\n%s\n===== END CATALOG =====\n", len(mans), compactCatalog(mans))

	// Happy path.
	scoreGraph(t, "A: weekday 8am — email me a summary of my sheet", core.Graph{
		Name: "Daily sheet summary",
		Nodes: []core.Node{
			{ID: "read", Module: "sheets_read_range", Params: map[string]any{"spreadsheet_id": "abc", "range": "Sheet1"}},
			{ID: "summary", Module: "render_text", Params: map[string]any{"template": "row.name + ': ' + string(row.value)", "prefix": "Today's rows:\n", "separator": "\n"}},
			{ID: "mail", Module: "gmail_send_email", Params: map[string]any{"to": "me@example.com", "subject": "Daily sheet summary"}},
		},
		Edges: []core.Edge{
			{From: "read", FromPort: "rows", To: "summary", ToPort: "rows"},
			{From: "read", FromPort: "headers", To: "summary", ToPort: "headers"},
			{From: "summary", FromPort: "text", To: "mail", ToPort: "body"},
		},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "0 8 * * 1-5"}},
	})

	// What an agentic model builds AFTER calling describe_drop on
	// gmail_search_messages and learning `messages` is already full email
	// records: search -> map_rows -> sheets. No for_each needed.
	scoreGraph(t, "B-simple: new email -> sheet (search returns full records)", core.Graph{
		Name: "Log emails to sheet",
		Nodes: []core.Node{
			{ID: "poll", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 300}},
			{ID: "search", Module: "gmail_search_messages", Params: map[string]any{"query": "is:unread", "max_results": 20}},
			{ID: "shape", Module: "map_rows", Params: map[string]any{"select": []any{"from", "subject", "date"}}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "abc", "range": "Inbox"}},
		},
		Edges: []core.Edge{
			{From: "search", FromPort: "messages", To: "shape", ToPort: "rows"},
			{From: "shape", FromPort: "rows", To: "append", ToPort: "rows"},
		},
	})

	// C: contact form -> Slack, formatted via render_text (the example tells
	// the model to wire render_text.text into slack's body).
	scoreGraph(t, "C: contact form -> Slack (render_text)", core.Graph{
		Name: "Form to Slack",
		Nodes: []core.Node{
			{ID: "form", Module: "webhook_input", Params: map[string]any{"public_form": true, "form_fields": []any{"name", "email", "message"}}},
			{ID: "render", Module: "render_text", Params: map[string]any{"template": "'New enquiry from ' + row.name + ' <' + row.email + '>: ' + row.message"}},
			{ID: "notify", Module: "slack_send_message", Params: map[string]any{"channel": "#sales"}},
		},
		Edges: []core.Edge{
			{From: "form", FromPort: "body", To: "render", ToPort: "rows"},
			{From: "render", FromPort: "text", To: "notify", ToPort: "text"},
		},
	})

	// D: Stripe payment failed -> SMS. describe_drop taught the model that
	// account_sid/auth_token DEFAULT to the TWILIO_* secrets, so it omits them
	// (no hardcoding, no invented ${secret} name).
	scoreGraph(t, "D: Stripe failed -> SMS (secrets auto-default)", core.Graph{
		Name: "Failed payment SMS",
		Nodes: []core.Node{
			{ID: "trig", Module: "stripe_on_payment_failed"},
			{ID: "sms", Module: "twilio_send_sms", Params: map[string]any{"to": "+15551230000", "from": "+15559876543"}},
		},
		Edges: []core.Edge{
			{From: "trig", FromPort: "failure_message", To: "sms", ToPort: "body"},
		},
	})

	// E: weekly Leads sheet -> Postgres upsert (no dupes). describe_drop's note
	// resolves the connection ("set once under Apps") so no DSN param invented.
	scoreGraph(t, "E: weekly sheet -> Postgres upsert", core.Graph{
		Name: "Leads to CRM",
		Nodes: []core.Node{
			{ID: "read", Module: "sheets_read_range", Params: map[string]any{"spreadsheet_id": "abc", "range": "Leads"}},
			{ID: "load", Module: "postgres_upsert_rows", Params: map[string]any{"table": "customers", "conflict_columns": []any{"email"}}},
		},
		Edges: []core.Edge{
			{From: "read", FromPort: "rows", To: "load", ToPort: "rows"},
			{From: "read", FromPort: "headers", To: "load", ToPort: "headers"},
		},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "0 9 * * 1"}},
	})

	// The for_each trap (mis-wired) — caught only by the manifest validator.
	bad := miswiredForEach()
	g := core.Graph{Name: bad["name"].(string)}
	for _, n := range bad["nodes"].([]any) {
		m := n.(map[string]any)
		g.Nodes = append(g.Nodes, core.Node{ID: m["id"].(string), Module: m["module"].(string), Params: m["params"].(map[string]any)})
	}
	for _, e := range bad["edges"].([]any) {
		m := e.(map[string]any)
		g.Edges = append(g.Edges, core.Edge{From: m["from"].(string), FromPort: m["from_port"].(string), To: m["to"].(string), ToPort: m["to_port"].(string)})
	}
	scoreGraph(t, "B-foreach: new email -> sheet (mis-wired for_each)", g)
}

// scoreGraph runs all three production gates and prints the verdict.
func scoreGraph(t *testing.T, name string, g core.Graph) {
	t.Helper()
	g.Tenant, g.Workspace = "sim", "main"
	pretty, _ := json.MarshalIndent(generatedFromGraph(g), "", "  ")
	fmt.Printf("\n########## SCENARIO: %s ##########\n%s\n", name, pretty)

	fmt.Printf("\n-- LintGraph (generate-loop gate, security/placeholder) --\n")
	lint := core.LintGraph(g)
	if len(lint) == 0 {
		fmt.Println("  (clean)")
	}
	for _, is := range lint {
		fmt.Printf("  [%s] %s %v\n", is.Severity, is.Message, is.NodeIDs)
	}

	fmt.Printf("\n-- manifest validation (NOW also in the loop) --\n")
	mi := core.ManifestLintIssues(g, manifestMap())
	if len(mi) == 0 {
		fmt.Println("  (clean — loop would ACCEPT)")
	}
	for _, is := range mi {
		fmt.Printf("  [%s] %s\n", is.Severity, is.Message)
	}

	fmt.Printf("\n-- core.Validate (save gate) --\n")
	if err := core.Validate(g); err != nil {
		fmt.Printf("  REJECT:\n%s\n", err)
	} else {
		fmt.Println("  (passes — draft saves)")
	}
	fmt.Println("##########################################")
}
