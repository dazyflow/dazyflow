// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/webapi"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// An org's own described API has to reach the flow GENERATOR, not just the
// palette.
//
// Same combination the MCP tests pin (see mcpservers_flowgen_test.go), and for
// the same reason it needs its own test: the generator grounds on SearchDrops →
// listDrops → ManifestsForTenant, nothing in flowgen mentions web APIs, so the
// connection is invisible in the code and would be silently lost by a change to
// any of those three. This is the pitch — describe your own service, then
// describe what you want in a sentence — so losing it is losing the feature
// while every unit test still passes.

// webAPIFlowgenService builds a Service whose resolver carries one org's
// described API, the way a running daemon's does. The operation carries real
// arguments, because the arguments are the half the generator has to get right.
func webAPIFlowgenService(t *testing.T, tenant string) *Service {
	t.Helper()
	cat := webapi.NewCatalog()
	err := cat.Register(webapi.Descriptor{
		Tenant:  tenant,
		Name:    "orders",
		BaseURL: "https://api.example.com/v1",
		Auth:    webapi.Auth{Kind: webapi.AuthBearer},
		Operations: []webapi.Operation{{
			ID:       "create_order",
			Method:   "POST",
			Path:     "/orders",
			Summary:  "Place an order",
			BodyMode: webapi.BodyJSON,
			Args: []webapi.Arg{
				{Name: "sku", In: webapi.InBody, Type: "string", Required: true},
				{Name: "qty", In: webapi.InBody, Type: "integer", Required: true},
				{Name: "gift", In: webapi.InBody, Type: "boolean"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return &Service{Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default, WebAPI: cat},
	}}
}

func TestSearchDrops_IncludesTheOrgsOwnWebAPISteps(t *testing.T) {
	t.Parallel()
	svc := webAPIFlowgenService(t, "acme")
	ctx := context.Background()

	mine, err := svc.SearchDrops(ctx, adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	var found *core.Manifest
	for i := range mine {
		if mine[i].ID == "api:orders:create_order" {
			found = &mine[i]
		}
	}
	if found == nil {
		t.Fatal("the org's own web-API step is missing from the catalog the generator grounds on")
	}
	// It must arrive as a usable step, not a bare id: the generator writes
	// params and wires ports off exactly this.
	if found.Category != "external" || found.Label == "" || found.Summary == "" {
		t.Errorf("manifest is not presentable: %+v", found)
	}
	if len(found.Examples) == 0 {
		t.Error("no params example: the generator learns the shape from these")
	}

	// And it must not leak to another org, whose generator would then compose a
	// flow against a step it cannot resolve.
	theirs, err := svc.SearchDrops(ctx, adminPrincipal("globex"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	for _, m := range theirs {
		if strings.HasPrefix(m.ID, "api:orders:") {
			t.Fatalf("another org's catalog contains %q", m.ID)
		}
	}
}

// The model is grounded on the compact catalog, so an operation whose arguments
// are absent from it cannot be filled in — the generator would be left guessing
// param names, which is exactly what the grounding exists to prevent.
func TestCompactCatalog_DescribesAWebAPIOperationsArguments(t *testing.T) {
	t.Parallel()
	svc := webAPIFlowgenService(t, "acme")
	mans, err := svc.SearchDrops(context.Background(), adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}
	var mine []core.Manifest
	for _, m := range mans {
		if strings.HasPrefix(m.ID, "api:orders:") {
			mine = append(mine, m)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("expected exactly the one operation, got %d", len(mine))
	}
	line := compactCatalog(mine)
	if !strings.Contains(line, "api:orders:create_order") {
		t.Fatalf("the catalog omits the operation entirely:\n%s", line)
	}
	for _, arg := range []string{"sku", "qty", "gift"} {
		if !strings.Contains(line, arg) {
			t.Errorf("argument %q is not in the grounding:\n%s", arg, line)
		}
	}
}

// The real generate loop, with a scripted model answer that uses the org's own
// step: the production gate must accept it. An unknown-module error here would
// mean the AI can see a step it is not allowed to use.
func TestGenerateFlow_ComposesAgainstAWebAPIStep(t *testing.T) {
	t.Parallel()
	svc := webAPIFlowgenService(t, "acme")
	mans, err := svc.SearchDrops(context.Background(), adminPrincipal("acme"), DropSearch{})
	if err != nil {
		t.Fatalf("SearchDrops: %v", err)
	}

	sp := &scriptedProvider{graphs: []map[string]any{{
		"name": "place an order",
		"nodes": []any{node("a", "api:orders:create_order", map[string]any{
			"sku": "ABC-123",
			"qty": 2,
		})},
	}}}
	llm.Register(llm.ProviderInfo{
		Name: "fakeflowwebapi", Integration: "FakeFlowWebAPI", DefaultModel: "m", Provider: sp,
	})

	h := newGatewayHarness(t)
	g, issues, err := h.gw.flowAPI().generateFlow(context.Background(), "fakeflowwebapi", "key",
		"place an order for ABC-123", mans, "acme", "main", "", nil)
	if err != nil {
		t.Fatalf("generateFlow: %v", err)
	}
	if sp.calls != 1 {
		t.Fatalf("the draft needed %d attempts — the web-API step was not accepted first time", sp.calls)
	}
	for _, is := range issues {
		if strings.Contains(is.Message, "unknown module") {
			t.Fatalf("the generator's own gate rejected the org's step: %+v", issues)
		}
	}
	if len(g.Nodes) != 1 || g.Nodes[0].Module != "api:orders:create_order" {
		t.Fatalf("generated graph does not use the step: %+v", g.Nodes)
	}
}

// The Apps page has to be able to connect a described API, and that is the
// ergonomic claim the whole feature rests on: the address and the credential are
// entered once, on a page, instead of being re-typed into every step.
//
// It works because connection fields are found by scanning the tenant's
// manifests for a matching slug (connectionFieldsForSlug) and a synthesized
// manifest declares ConnectionFields like any other — so nothing in the
// connection code mentions web APIs, and this is the only thing that would
// notice if that stopped being true.
func TestConnectionFields_FoundForADescribedAPI(t *testing.T) {
	t.Parallel()
	cat := webapi.NewCatalog()
	err := cat.Register(webapi.Descriptor{
		Tenant:      "t",
		Name:        "orders",
		Integration: "Order service",
		BaseURL:     "https://api.example.com",
		Auth:        webapi.Auth{Kind: webapi.AuthBearer},
		Operations: []webapi.Operation{{
			ID: "get_order", Method: "GET", Path: "/orders/{id}",
			Args: []webapi.Arg{{Name: "id", In: webapi.InPath, Type: "string", Required: true}},
		}},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	h := newGatewayHarness(t)
	h.svc.Engine.Resolver = &engine.NodeResolver{Native: engine.Default, WebAPI: cat}

	integration, fields, err := h.gw.secretsAPI().connectionFieldsForSlug(
		context.Background(), adminPrincipal("t"), core.ConnectionSlug("Order service"))
	if err != nil {
		t.Fatalf("connectionFieldsForSlug: %v", err)
	}
	if integration != "Order service" {
		t.Fatalf("integration = %q — the Apps page cannot connect this API", integration)
	}
	keys := map[string]bool{}
	secret := map[string]bool{}
	for _, f := range fields {
		keys[f.Key] = true
		secret[f.Key] = f.Secret
	}
	// The credential, and only that. The service address is the catalog's and
	// is set in Admin → Web APIs: a connection is writable with secret:write,
	// so offering the address here would let an editor repoint the whole org's
	// calls — and be handed the token, which is sent to whatever address won.
	if !keys["token"] {
		t.Fatalf("fields = %+v, want the credential", fields)
	}
	if keys["base_url"] {
		t.Errorf("the service address is offered on the connection page: %+v", fields)
	}
	if !secret["token"] {
		t.Error("the credential field is not Secret: it would render unmasked and skip redaction")
	}

	// And not for another org, which would be one tenant's connection page
	// offering to fill in another's.
	other, _, err := h.gw.secretsAPI().connectionFieldsForSlug(
		context.Background(), adminPrincipal("globex"), core.ConnectionSlug("Order service"))
	if err != nil {
		t.Fatal(err)
	}
	if other != "" {
		t.Errorf("another org resolved the connection as %q", other)
	}
}
