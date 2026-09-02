// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/workspace"
)

// usageService builds a service over two real workspace stores for one org.
func usageService(t *testing.T) (*Service, *workspace.Store, *workspace.Store) {
	t.Helper()
	ws1, err := workspace.OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS ws1: %v", err)
	}
	ws2, err := workspace.OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS ws2: %v", err)
	}
	return &Service{Workspaces: MapWorkspaces{
		"acme/ws1": ws1,
		"acme/ws2": ws2,
	}}, ws1, ws2
}

func saveGraph(t *testing.T, store *workspace.Store, g core.Graph) {
	t.Helper()
	if _, err := store.Save(g, "tester"); err != nil {
		t.Fatalf("save %q: %v", g.ID, err)
	}
}

// TestFlowsUsingMCPServer_FindsReferencesAcrossWorkspaces is the fact the
// delete warning rests on: every flow in the org that names the server's
// steps, and nothing else.
func TestFlowsUsingMCPServer_FindsReferencesAcrossWorkspaces(t *testing.T) {
	t.Parallel()
	svc, ws1, ws2 := usageService(t)

	saveGraph(t, ws1, core.Graph{
		ID: "uses-it", Name: "Nightly sync", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "a", Module: "mcp:mcp-test:search"},
			// A second node on the same tool must not be reported twice.
			{ID: "b", Module: "mcp:mcp-test:search"},
			{ID: "c", Module: "mcp:mcp-test:create"},
			{ID: "d", Module: "http_request"},
		},
	})
	// A different server whose name is a PREFIX of ours must not match: the
	// colon is what makes "mcp-test" and "mcp-test-2" different servers.
	saveGraph(t, ws1, core.Graph{
		ID: "other-server", Name: "Unrelated", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "mcp:mcp-test-2:search"}},
	})
	saveGraph(t, ws2, core.Graph{
		ID: "also-uses-it", Name: "Alerts", Tenant: "acme", Workspace: "ws2",
		Nodes: []core.Node{{ID: "a", Module: "mcp:mcp-test:search"}},
	})

	usage, err := svc.FlowsUsingMCPServer(context.Background(), adminPrincipal("acme"), "acme", "mcp-test")
	if err != nil {
		t.Fatalf("FlowsUsingMCPServer: %v", err)
	}
	if !usage.InUse() {
		t.Fatal("InUse is false with two flows referencing the server")
	}
	if len(usage.Flows) != 2 {
		t.Fatalf("flows = %+v, want the two that reference it", usage.Flows)
	}
	if usage.Hidden != 0 {
		t.Errorf("hidden = %d, want 0 for an admin", usage.Hidden)
	}
	byID := map[string]StepSourceUse{}
	for _, f := range usage.Flows {
		byID[f.FlowID] = f
	}
	one, ok := byID["uses-it"]
	if !ok {
		t.Fatalf("the referencing flow is missing: %+v", usage.Flows)
	}
	if one.Workspace != "ws1" || one.Name != "Nightly sync" {
		t.Errorf("flow = %+v, want it attributed to ws1 by name", one)
	}
	if len(one.Steps) != 2 {
		t.Errorf("steps = %v, want the two distinct tools", one.Steps)
	}
	if two := byID["also-uses-it"]; two.Workspace != "ws2" {
		t.Errorf("the second workspace was not scanned: %+v", usage.Flows)
	}
	if _, found := byID["other-server"]; found {
		t.Error("a flow using a different server whose name shares a prefix was reported")
	}
}

// TestFlowsUsingMCPServer_NothingUsesIt is the case the old warning could not
// express, and the reason this lookup exists.
func TestFlowsUsingMCPServer_NothingUsesIt(t *testing.T) {
	t.Parallel()
	svc, ws1, _ := usageService(t)
	saveGraph(t, ws1, core.Graph{
		ID: "unrelated", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "http_request"}},
	})

	usage, err := svc.FlowsUsingMCPServer(context.Background(), adminPrincipal("acme"), "acme", "mcp-test")
	if err != nil {
		t.Fatalf("FlowsUsingMCPServer: %v", err)
	}
	if usage.InUse() || len(usage.Flows) != 0 || usage.Hidden != 0 {
		t.Fatalf("usage = %+v, want nothing in use", usage)
	}
}

// TestFlowsUsingMCPServer_CountsWhatItMayNotName: a caller who cannot view a
// private flow still needs the blast radius. Counted, never titled.
func TestFlowsUsingMCPServer_CountsWhatItMayNotName(t *testing.T) {
	t.Parallel()
	svc, ws1, _ := usageService(t)
	saveGraph(t, ws1, core.Graph{
		ID: "secret", Name: "Payroll", Tenant: "acme", Workspace: "ws1",
		Owner: "someone-else", Visibility: core.VisibilityPrivate,
		Nodes: []core.Node{{ID: "a", Module: "mcp:mcp-test:search"}},
	})

	p := core.Principal{
		Subject:   "editor-1",
		Tenant:    "acme",
		Workspace: "ws1",
		Roles:     []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphEdit}}},
	}
	usage, err := svc.FlowsUsingMCPServer(context.Background(), p, "acme", "mcp-test")
	if err != nil {
		t.Fatalf("FlowsUsingMCPServer: %v", err)
	}
	if len(usage.Flows) != 0 {
		t.Fatalf("flows = %+v, want a private flow left unnamed", usage.Flows)
	}
	if usage.Hidden != 1 {
		t.Fatalf("hidden = %d, want the private flow counted", usage.Hidden)
	}
	if !usage.InUse() {
		t.Error("InUse is false when the only user is one the caller cannot see")
	}
}

// TestFlowsUsingMCPServer_RefusesAnotherOrg: the scan is tenant-scoped, and
// the tenant comes from the principal's own authorization.
func TestFlowsUsingMCPServer_RefusesAnotherOrg(t *testing.T) {
	t.Parallel()
	svc, _, _ := usageService(t)
	if _, err := svc.FlowsUsingMCPServer(context.Background(), adminPrincipal("acme"), "globex", "mcp-test"); err == nil {
		t.Fatal("an admin read another org's flow usage")
	}
}

// TestFlowsUsingWebAPI covers the other step source through the same scan: a
// web API catalog's steps carry api:<name>:<operation>, and deleting one has
// the same consequence a disabled MCP server does.
func TestFlowsUsingWebAPI(t *testing.T) {
	t.Parallel()
	svc, ws1, _ := usageService(t)

	saveGraph(t, ws1, core.Graph{
		ID: "billing", Name: "Nightly invoices", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "a", Module: "api:order-service:create_order"},
			{ID: "b", Module: "api:order-service:get_order"},
			{ID: "c", Module: "http_request"},
		},
	})
	// A neighbouring catalog whose name shares a prefix must not match, and
	// neither must an MCP server that happens to share the name.
	saveGraph(t, ws1, core.Graph{
		ID: "other", Name: "Unrelated", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "a", Module: "api:order-service-2:get_order"},
			{ID: "b", Module: "mcp:order-service:get_order"},
		},
	})

	usage, err := svc.FlowsUsingWebAPI(context.Background(), adminPrincipal("acme"), "acme", "order-service")
	if err != nil {
		t.Fatalf("FlowsUsingWebAPI: %v", err)
	}
	if len(usage.Flows) != 1 || usage.Flows[0].FlowID != "billing" {
		t.Fatalf("flows = %+v, want only the referencing flow", usage.Flows)
	}
	if len(usage.Flows[0].Steps) != 2 {
		t.Errorf("steps = %v, want both operations", usage.Flows[0].Steps)
	}

	// And the two schemes do not see each other's flows.
	mcpUsage, err := svc.FlowsUsingMCPServer(context.Background(), adminPrincipal("acme"), "acme", "order-service")
	if err != nil {
		t.Fatalf("FlowsUsingMCPServer: %v", err)
	}
	if len(mcpUsage.Flows) != 1 || mcpUsage.Flows[0].FlowID != "other" {
		t.Fatalf("mcp flows = %+v, want only the flow using the MCP step", mcpUsage.Flows)
	}
}
