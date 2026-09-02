// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/mcp"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// An org's own MCP tools have to reach the flow GENERATOR, not just the
// palette.
//
// This is the combination the product is actually selling: bring the catalogue
// nobody wrote a connector for, then describe what you want in a sentence and
// have it composed against those tools. It works because the generator grounds
// on SearchDrops → listDrops → ManifestsForTenant, which is tenant-scoped — but
// nothing in flowgen mentions MCP, so the connection is invisible in the code
// and would be silently lost by a future change to any of those three.

// mcpFlowgenService builds a Service whose resolver carries one org's MCP
// server, the way a running daemon's does. The tool carries a real argument
// schema, because the arguments are the half the generator has to get right.
func mcpFlowgenService(t *testing.T, tenant string) *Service {
	t.Helper()
	srv := (&fakeMCPEndpoint{
		toolNames: []string{"create_issue"},
		schemas: map[string]string{"create_issue": `{
			"type": "object",
			"properties": {
				"repo":  {"type": "string"},
				"title": {"type": "string"},
				"draft": {"type": "boolean"}
			},
			"required": ["repo", "title"]
		}`},
	}).start(t)
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "vendor", Tenant: tenant, URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	return &Service{Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default, MCP: cat},
	}}
}

func TestSearchDrops_IncludesTheOrgsOwnMCPTools(t *testing.T) {
	svc := mcpFlowgenService(t, "acme")
	ctx := context.Background()

	mine, err := svc.SearchDrops(ctx, adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	var found *core.Manifest
	for i := range mine {
		if mine[i].ID == "mcp:vendor:create_issue" {
			found = &mine[i]
		}
	}
	if found == nil {
		t.Fatal("the org's own MCP tool is missing from the catalog the generator grounds on")
	}
	// It must arrive as a usable step, not a bare id: the generator writes
	// params and wires ports off exactly this.
	if found.Category != "external" || found.Label == "" {
		t.Errorf("manifest is not presentable: %+v", found)
	}

	// And it must not leak to another org, whose generator would then compose
	// a flow against a step it cannot resolve.
	theirs, err := svc.SearchDrops(ctx, adminPrincipal("globex"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	for _, m := range theirs {
		if strings.HasPrefix(m.ID, "mcp:vendor:") {
			t.Fatalf("another org's catalog contains %q", m.ID)
		}
	}
}

// TestGenerateFlow_ComposesAgainstAnOrgsMCPTool drives the real generate loop
// with a scripted model answer that uses the org's MCP step, and asserts the
// production gate accepts it — an unknown-module error here would mean the AI
// can see a step it is not allowed to use.
func TestGenerateFlow_ComposesAgainstAnOrgsMCPTool(t *testing.T) {
	svc := mcpFlowgenService(t, "acme")
	mans, err := svc.SearchDrops(context.Background(), adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}

	sp := &scriptedProvider{graphs: []map[string]any{{
		"name": "file a bug",
		// Both required arguments set as params — the alternative the system
		// prompt offers is wiring them, and either satisfies the gate.
		"nodes": []any{node("a", "mcp:vendor:create_issue", map[string]any{
			"repo":  "acme/widgets",
			"title": "Something broke",
		})},
	}}}
	llm.Register(llm.ProviderInfo{
		Name: "fakeflowmcp", Integration: "FakeFlowMCP", DefaultModel: "m", Provider: sp,
	})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.flowAPI().generateFlow(context.Background(), "fakeflowmcp", "key",
		"file a bug in our tracker", mans, "acme", "main", "", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if sp.calls != 1 {
		t.Fatalf("the draft needed %d attempts — the MCP step was not accepted first time", sp.calls)
	}
	for _, is := range issues {
		if strings.Contains(is.Message, "unknown module") {
			t.Fatalf("the generator's own gate rejected the org's MCP step: %+v", issues)
		}
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Module != "mcp:vendor:create_issue" {
		t.Fatalf("generated graph does not use the MCP step: %+v", g.Nodes)
	}
}

// TestCompactCatalog_DescribesAnMCPToolsArguments: the model is grounded on the
// compact catalog, so a tool whose arguments are absent from it cannot be
// filled in — the generator would be left guessing param names, which is
// exactly the failure the grounding exists to prevent.
func TestCompactCatalog_DescribesAnMCPToolsArguments(t *testing.T) {
	svc := mcpFlowgenService(t, "acme")
	mans, err := svc.SearchDrops(context.Background(), adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	var mine []core.Manifest
	for _, m := range mans {
		if strings.HasPrefix(m.ID, "mcp:vendor:") {
			mine = append(mine, m)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("expected exactly the one MCP tool, got %d", len(mine))
	}
	line := compactCatalog(mine)
	if !strings.Contains(line, "mcp:vendor:create_issue") {
		t.Fatalf("the catalog omits the tool entirely:\n%s", line)
	}
	// Both required arguments must be nameable by the model, and the optional
	// one too — they are ports now, so they appear under "in:".
	for _, arg := range []string{"repo", "title", "draft"} {
		if !strings.Contains(line, arg) {
			t.Errorf("argument %q is not in the grounding:\n%s", arg, line)
		}
	}
}

// TestMCPTool_RequiredArgumentIsEnforced: a draft that omits a required
// argument must be REJECTED by the same gate the save path uses, so the repair
// loop gets a chance to fix it rather than the flow failing at run time.
func TestMCPTool_RequiredArgumentIsEnforced(t *testing.T) {
	svc := mcpFlowgenService(t, "acme")
	mans, err := svc.SearchDrops(context.Background(), adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	byID := map[string]core.Manifest{}
	for _, m := range mans {
		byID[m.ID] = m
	}
	// "repo" is required and neither wired nor set as a param.
	g := core.Graph{
		Name:   "incomplete",
		Tenant: "acme",
		Nodes: []core.Node{{
			ID: "a", Module: "mcp:vendor:create_issue",
			Params: map[string]any{"title": "Something broke"},
		}},
	}
	err = core.ValidateWithManifests(g, byID)
	if err == nil {
		t.Fatal("a node missing a required MCP argument passed validation")
	}
	if !strings.Contains(err.Error(), "repo") {
		t.Fatalf("the error does not name the missing argument: %v", err)
	}
}

// TestSearchDrops_HidesAnUnreachableServersTools: a step that cannot run today
// is not a step to build something new on, so it is kept out of search and out
// of flow generation — while the EDITOR still gets it (include_disabled), or a
// flow already using it would render with no ports.
func TestSearchDrops_HidesAnUnreachableServersTools(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"create_issue"}}).start(t)
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "vendor", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	svc := &Service{Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default, MCP: cat},
	}}
	ctx := context.Background()
	p := adminPrincipal("acme")

	// While connected it is offered like any other step.
	if !hasDrop(t, svc, ctx, p, "mcp:vendor:create_issue") {
		t.Fatal("a connected server's tool is missing from search")
	}

	// Now describe it from cache, as a failed handshake does.
	tools := []mcp.Tool{{Name: "create_issue"}}
	if err := cat.RegisterOffline(mcp.OfflineDescriptor{
		Tenant: "acme", Name: "vendor", Tools: tools, Reason: "HTTP 401",
	}); err != nil {
		t.Fatalf("RegisterOffline: %v", err)
	}
	if hasDrop(t, svc, ctx, p, "mcp:vendor:create_issue") {
		t.Error("an unreachable server's tool is still offered for new work")
	}
	// The editor's listing keeps it, stamped, so an existing flow renders. This
	// is the request FlowEditor actually makes (include_disabled=1).
	mans, err := svc.SearchDrops(ctx, p, DropSearch{IncludeDisabled: true})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	var man *core.Manifest
	for i := range mans {
		if mans[i].ID == "mcp:vendor:create_issue" {
			man = &mans[i]
		}
	}
	if man == nil {
		t.Fatal("the editor's catalog lost the step — this is what breaks a flow's wiring")
	}
	if !man.Unavailable {
		t.Error("the step is not stamped unavailable")
	}
	// Still fully described, which is the reason to keep it at all.
	if len(man.Inputs) == 0 || len(man.Outputs) == 0 {
		t.Errorf("the step arrived without ports: %+v", man)
	}
}

// hasDrop reports whether SearchDrops offers an id.
func hasDrop(t *testing.T, svc *Service, ctx context.Context, p core.Principal, id string) bool {
	t.Helper()
	mans, err := svc.SearchDrops(ctx, p, DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	for _, m := range mans {
		if m.ID == id {
			return true
		}
	}
	return false
}
