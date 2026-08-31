// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
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

// TestPickProvider_Cov covers pickProvider: no connected providers, the
// default-to-first choice, and an explicit want that matches a connected one.
func TestPickProvider_Cov(t *testing.T) {
	h := newSecretsHarness(t)
	ctx := core.WithTenant(context.Background(), "t")

	// No providers connected yet -> empty.
	if chosen, conn := h.gw.pickProvider(ctx, ""); conn != nil || chosen.info.Name != "" {
		t.Fatalf("no-connection pick = %+v / %v, want empty", chosen, conn)
	}

	// Register two test providers and store an api_key for each so both count
	// as connected.
	llm.Register(llm.ProviderInfo{Name: "testprov_a", Integration: "TestProvA"})
	llm.Register(llm.ProviderInfo{Name: "testprov_b", Integration: "TestProvB"})
	for _, p := range []struct{ integ, key string }{
		{"TestProvA", "key-a"}, {"TestProvB", "key-b"},
	} {
		if err := h.gw.EncryptedSecrets.PutScoped(ctx, "t", "", ScopeTenant,
			core.ConnectionSecretKey(p.integ, "api_key"), p.key); err != nil {
			t.Fatalf("seed %s: %v", p.integ, err)
		}
	}

	// At least our two providers are connected; default picks the first
	// connected one in registration order.
	chosen, conn := h.gw.pickProvider(ctx, "")
	if len(conn) < 2 {
		t.Fatalf("connected providers = %d, want >=2", len(conn))
	}
	if chosen.info.Name == "" || chosen.key == "" {
		t.Fatalf("default pick is empty: %+v", chosen)
	}

	// Explicit want selects the matching provider.
	want, _ := h.gw.pickProvider(ctx, "testprov_b")
	if want.info.Name != "testprov_b" || want.key != "key-b" {
		t.Fatalf("want=testprov_b pick = %+v", want)
	}
}

func TestGeneratedFromGraph_Cov(t *testing.T) {
	g := core.Graph{
		Name:  "My Flow",
		Nodes: []core.Node{{ID: "a", Module: "noop", Params: map[string]any{"x": 1}}, {ID: "b", Module: "noop"}},
		Edges: []core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "0 9 * * *"},
		},
	}
	out := generatedFromGraph(g)
	if out.Name != "My Flow" || len(out.Nodes) != 2 || len(out.Edges) != 1 {
		t.Fatalf("generated = %+v", out)
	}
	if out.Edges[0].From != "a" || out.Edges[0].ToPort != "in" {
		t.Fatalf("edge = %+v", out.Edges[0])
	}
	if out.Trigger == nil || out.Trigger.Type != "cron" || out.Trigger.Cron != "0 9 * * *" {
		t.Fatalf("trigger = %+v", out.Trigger)
	}

	// No cron trigger -> nil trigger.
	g2 := core.Graph{Name: "n", Triggers: []core.GraphTrigger{{Type: "webhook"}}}
	if out := generatedFromGraph(g2); out.Trigger != nil {
		t.Fatalf("webhook graph trigger = %+v, want nil", out.Trigger)
	}
}

func TestStampGraph_Cov(t *testing.T) {
	g := core.Graph{Nodes: []core.Node{{Module: "noop"}, {ID: "named", Module: "noop"}}}
	stampGraph(&g, "tenantX", "wsX")
	if g.Tenant != "tenantX" || g.Workspace != "wsX" {
		t.Fatalf("stamp tenant/ws = %q/%q", g.Tenant, g.Workspace)
	}
	if g.Name != "AI-generated flow" {
		t.Fatalf("default name = %q", g.Name)
	}
	if g.Nodes[0].ID != "step_1" || g.Nodes[1].ID != "named" {
		t.Fatalf("node ids = %q, %q", g.Nodes[0].ID, g.Nodes[1].ID)
	}

	// Existing name is kept.
	g2 := core.Graph{Name: "Keep Me"}
	stampGraph(&g2, "t", "w")
	if g2.Name != "Keep Me" {
		t.Fatalf("name = %q, want kept", g2.Name)
	}
}
