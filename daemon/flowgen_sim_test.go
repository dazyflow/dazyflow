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
	"sort"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/llm"

	_ "git.sr.ht/~klahr/dazyflow/drops" // register every built-in drop
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

func manifestMap() map[string]core.Manifest { return engine.Default.Manifests() }

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
	g, issues, err := h.gw.generateFlow(context.Background(), "fakeflowstruct", "key",
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
	_, issues, err := h.gw.generateFlow(context.Background(), "fakeflowstruckstuck", "key",
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
	mi := manifestValidationIssues(g, manifestMap())
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
