package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/llm"
)

// scriptedProvider returns a pre-baked graph per call, so we can drive the
// validate-and-repair loop deterministically (no real LLM).
type scriptedProvider struct {
	calls  int
	graphs []map[string]any
}

func (s *scriptedProvider) Call(_ context.Context, _ string, _ llm.Request) (llm.Result, *core.JobError) {
	i := s.calls
	s.calls++
	if i >= len(s.graphs) {
		i = len(s.graphs) - 1
	}
	return llm.Result{Tool: s.graphs[i]}, nil
}

func node(id, module string, params map[string]any) map[string]any {
	return map[string]any{"id": id, "module": module, "params": params}
}

// TestGenerateFlow_RepairsInvalidDraft: the first model answer has a
// REPLACE_WITH_ placeholder (a LintError); the loop must feed the error back
// and accept the corrected second answer.
func TestGenerateFlow_RepairsInvalidDraft(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{
		{"name": "t", "nodes": []any{node("a", "text", map[string]any{"text": "REPLACE_WITH_BODY"})}},
		{"name": "t", "nodes": []any{node("a", "text", map[string]any{"text": "hello"})}},
	}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowrepair", Integration: "FakeFlowRepair", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.generateFlow(context.Background(), "fakeflowrepair", "key", "send a thing", nil, "t1", "main", "", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if sp.calls != 2 {
		t.Fatalf("expected one repair (2 calls), got %d", sp.calls)
	}
	if hasLintError(issues) {
		t.Fatalf("final graph still has lint errors: %+v", issues)
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Params["text"] != "hello" {
		t.Fatalf("repaired graph wrong: %+v", g.Nodes)
	}
	if g.Tenant != "t1" || g.Workspace != "main" {
		t.Errorf("scope not stamped: tenant=%q ws=%q", g.Tenant, g.Workspace)
	}
}

// TestGenerateFlow_ExhaustsRepairsReturnsBestEffort: when the model never
// fixes the error, we stop after the cap and return the best graph + the
// issues (the UI shows them) — we don't loop forever or error out.
func TestGenerateFlow_ExhaustsRepairsReturnsBestEffort(t *testing.T) {
	bad := map[string]any{"name": "t", "nodes": []any{node("a", "text", map[string]any{"text": "REPLACE_WITH_X"})}}
	sp := &scriptedProvider{graphs: []map[string]any{bad}} // always returns the bad one
	llm.Register(llm.ProviderInfo{Name: "fakeflowstuck", Integration: "FakeFlowStuck", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.generateFlow(context.Background(), "fakeflowstuck", "key", "x", nil, "t1", "main", "", nil)
	if err != nil {
		t.Fatalf("should return best-effort, not error: %v", err)
	}
	if sp.calls != maxFlowRepairs+1 {
		t.Fatalf("expected %d calls (initial + repairs), got %d", maxFlowRepairs+1, sp.calls)
	}
	if !hasLintError(issues) || len(g.Nodes) == 0 {
		t.Fatalf("expected best-effort graph + surfaced errors, got %d nodes, issues=%+v", len(g.Nodes), issues)
	}
}

// TestGenerateFlow_UnparseableErrors: if the model never returns usable JSON,
// generateFlow errors (rather than returning an empty graph).
func TestGenerateFlow_UnparseableErrors(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{{"not": "a graph"}}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowjunk", Integration: "FakeFlowJunk", DefaultModel: "m", Provider: sp})
	h := newGatewayHarness(t)
	if _, _, err := h.gw.generateFlow(context.Background(), "fakeflowjunk", "key", "x", nil, "t1", "main", "", nil); err == nil {
		t.Fatal("expected an error when the model never returns a usable flow")
	}
}

// TestGenerateFlow_CronTrigger: a valid cron schedule is kept and stamped
// with the caller's timezone.
func TestGenerateFlow_CronTrigger(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{{
		"name":    "daily",
		"nodes":   []any{node("a", "text", map[string]any{"text": "hi"})},
		"trigger": map[string]any{"type": "cron", "cron": "0 9 * * *"},
	}}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowcron", Integration: "FakeFlowCron", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.generateFlow(context.Background(), "fakeflowcron", "key", "every day at 9", nil, "t1", "main", "Europe/Stockholm", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if hasLintError(issues) {
		t.Fatalf("unexpected lint errors: %+v", issues)
	}
	if len(g.Triggers) != 1 || g.Triggers[0].Type != "cron" || g.Triggers[0].Cron != "0 9 * * *" {
		t.Fatalf("cron trigger not kept: %+v", g.Triggers)
	}
	if g.Triggers[0].TZ != "Europe/Stockholm" {
		t.Errorf("timezone not stamped: %q", g.Triggers[0].TZ)
	}
}

// TestGenerateFlow_BadCronStripped: an unparseable schedule is dropped (so the
// draft still saves) and surfaced as a warning rather than shipped broken.
func TestGenerateFlow_BadCronStripped(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{{
		"name":    "x",
		"nodes":   []any{node("a", "text", map[string]any{"text": "hi"})},
		"trigger": map[string]any{"type": "cron", "cron": "every morning please"},
	}}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowbadcron", Integration: "FakeFlowBadCron", DefaultModel: "m", Provider: sp})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.generateFlow(context.Background(), "fakeflowbadcron", "key", "x", nil, "t1", "main", "UTC", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if len(g.Triggers) != 0 {
		t.Fatalf("bad cron should have been stripped, got %+v", g.Triggers)
	}
	var warned bool
	for _, is := range issues {
		if is.Code == "trigger_dropped" {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected a trigger_dropped warning, got %+v", issues)
	}
	if len(g.Nodes) != 1 {
		t.Errorf("nodes should be preserved when a bad trigger is stripped")
	}
}

// TestGenerateFlow_ProgressPhases: the streaming callback fires the expected
// phases in order (understanding → drafting → validating).
func TestGenerateFlow_ProgressPhases(t *testing.T) {
	sp := &scriptedProvider{graphs: []map[string]any{{
		"name": "x", "nodes": []any{node("a", "text", map[string]any{"text": "hi"})},
	}}}
	llm.Register(llm.ProviderInfo{Name: "fakeflowprog", Integration: "FakeFlowProg", DefaultModel: "m", Provider: sp})

	var phases []string
	h := newGatewayHarness(t)
	_, _, err := h.gw.generateFlow(context.Background(), "fakeflowprog", "key", "x", nil, "t1", "main", "", func(phase, _ string) {
		phases = append(phases, phase)
	})
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	want := []string{"understanding", "drafting", "validating"}
	if len(phases) < len(want) {
		t.Fatalf("phases = %v, want at least %v", phases, want)
	}
	for i, w := range want {
		if phases[i] != w {
			t.Fatalf("phase[%d] = %q, want %q (all: %v)", i, phases[i], w, phases)
		}
	}
}

func TestCompactCatalog(t *testing.T) {
	mans := []core.Manifest{
		{
			ID: "http_request", Category: "network", Summary: "Make an HTTP request.",
			ParamsSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"method":{"type":"string"}},"required":["url"]}`),
			Inputs:       []core.Port{{Port: "body"}},
			Outputs:      []core.Port{{Port: "response"}},
		},
	}
	got := compactCatalog(mans)
	for _, want := range []string{"http_request", "[network]", "Make an HTTP request.", "url(string)*", "in: body", "out: response"} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog missing %q\n got: %s", want, got)
		}
	}
}

// TestFlowGenerate_NeedsProvider: the endpoint asks to connect a provider
// when none is connected (no secret store in the harness).
func TestFlowGenerate_NeedsProvider(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/tools/flow/generate", map[string]any{"description": "email me daily"})
	if rw.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rw.Code)
	}
	var resp struct {
		NeedConnect bool `json:"need_connect"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if !resp.NeedConnect {
		t.Fatalf("want need_connect, got %s", rw.Body.String())
	}
}

func TestFlowGenerate_EmptyDescription(t *testing.T) {
	h := newGatewayHarness(t)
	if rw := h.do(t, "POST", "/api/v1/tools/flow/generate", map[string]any{"description": "  "}); rw.Code != http.StatusBadRequest {
		t.Fatalf("empty description: want 400, got %d", rw.Code)
	}
}
