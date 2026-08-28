// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

func mcpGateway(t *testing.T) (*HTTPGateway, *fakeMCPEndpoint, string) {
	t.Helper()
	svc, _ := newTestMCPServers(t)
	fake := &fakeMCPEndpoint{toolNames: []string{"search", "create"}}
	srv := fake.start(t)
	return &HTTPGateway{MCPServers: svc}, fake, srv.URL
}

func mcpPost(t *testing.T, h *HTTPGateway, p core.Principal, body string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/admin/mcp-servers", strings.NewReader(body))
	h.saveMCPServer(rw, req, p)
	return rw
}

func mcpList(t *testing.T, h *HTTPGateway, p core.Principal) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/admin/mcp-servers", nil)
	h.listMCPServers(rw, req, p)
	return rw
}

// TestMCPServersEndpoints_UnconfiguredDeployment: no store wired means the
// feature is absent, not broken.
func TestMCPServersEndpoints_UnconfiguredDeployment(t *testing.T) {
	h := &HTTPGateway{}
	if rw := mcpList(t, h, adminPrincipal("acme")); rw.Code != 501 {
		t.Errorf("code %d, want 501 when MCP servers are not configured", rw.Code)
	}
}

// TestMCPServersEndpoints_RequiresStepSourceAdmin: adding a server is not
// something an editor may do — it points the daemon at a new endpoint.
func TestMCPServersEndpoints_RequiresStepSourceAdmin(t *testing.T) {
	h, _, url := mcpGateway(t)
	rw := mcpPost(t, h, editorPrincipal("acme"), `{"name":"vendor","url":"`+url+`"}`)
	if rw.Code != 403 {
		t.Fatalf("code %d, want 403 for an editor", rw.Code)
	}
	if rw := mcpList(t, h, editorPrincipal("acme")); rw.Code != 403 {
		t.Errorf("list code %d, want 403 for an editor", rw.Code)
	}
}

func TestMCPServersEndpoints_SaveThenList(t *testing.T) {
	h, _, url := mcpGateway(t)
	audit := NewMemAuditLog()
	h.Audit = audit

	rw := mcpPost(t, h, adminPrincipal("acme"),
		`{"name":"vendor","url":"`+url+`","auth_kind":"bearer","token":"tok"}`)
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var saved mcpServerRow
	if err := json.Unmarshal(rw.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !saved.Connected || saved.LastError != "" {
		t.Fatalf("row = %+v, want a connected server", saved)
	}
	if len(saved.ToolIDs) != 2 {
		t.Fatalf("tool ids = %v, want both tools named", saved.ToolIDs)
	}
	if !saved.HasToken {
		t.Error("has_token is false for a bearer server")
	}
	// The response must not carry the credential back.
	if strings.Contains(rw.Body.String(), "tok\"") && strings.Contains(rw.Body.String(), "token\":\"tok") {
		t.Fatalf("the token came back in the response: %s", rw.Body)
	}

	rw = mcpList(t, h, adminPrincipal("acme"))
	if rw.Code != 200 {
		t.Fatalf("list code %d body %s", rw.Code, rw.Body)
	}
	var listed struct{ Servers []mcpServerRow }
	if err := json.Unmarshal(rw.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Servers) != 1 || listed.Servers[0].Name != "vendor" {
		t.Fatalf("servers = %+v", listed.Servers)
	}
}

// TestMCPServersEndpoints_ReportsWhatTheServerSaidAboutItself: the handshake
// note reaches the admin page, and does so as a live fact rather than a stored
// one — nothing persists a paragraph a third party can change at will.
func TestMCPServersEndpoints_ReportsWhatTheServerSaidAboutItself(t *testing.T) {
	svc, _ := newTestMCPServers(t)
	fake := &fakeMCPEndpoint{
		toolNames:    []string{"search"},
		instructions: "Ask in English; the index is English-only.",
	}
	srv := fake.start(t)
	h := &HTTPGateway{MCPServers: svc}

	rw := mcpPost(t, h, adminPrincipal("acme"), `{"label":"Vendor","url":"`+srv.URL+`"}`)
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var saved mcpServerRow
	if err := json.Unmarshal(rw.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if saved.Instructions != "Ask in English; the index is English-only." {
		t.Fatalf("instructions = %q", saved.Instructions)
	}
	// A server that says nothing leaves the field off the wire entirely.
	quiet := &fakeMCPEndpoint{toolNames: []string{"search"}}
	quietSrv := quiet.start(t)
	rw = mcpPost(t, h, adminPrincipal("acme"), `{"label":"Quiet","url":"`+quietSrv.URL+`"}`)
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	if strings.Contains(rw.Body.String(), "instructions") {
		t.Fatalf("a silent server still sent an instructions field: %s", rw.Body)
	}
}

// TestMCPServersEndpoints_ListIsTenantScoped: the page shows the caller's org
// and nobody else's.
func TestMCPServersEndpoints_ListIsTenantScoped(t *testing.T) {
	h, _, url := mcpGateway(t)
	if rw := mcpPost(t, h, adminPrincipal("acme"), `{"name":"vendor","url":"`+url+`"}`); rw.Code != 200 {
		t.Fatalf("seed: code %d body %s", rw.Code, rw.Body)
	}
	rw := mcpList(t, h, adminPrincipal("globex"))
	var listed struct{ Servers []mcpServerRow }
	_ = json.Unmarshal(rw.Body.Bytes(), &listed)
	if len(listed.Servers) != 0 {
		t.Fatalf("another org sees %+v", listed.Servers)
	}
}

// TestMCPServersEndpoints_ListOmitsInstanceWideServers: an operator's server
// works for the org but is not the org's to edit, so it must not appear on a
// page whose every control would edit or delete it.
func TestMCPServersEndpoints_ListOmitsInstanceWideServers(t *testing.T) {
	h, _, url := mcpGateway(t)
	if err := h.MCPServers.Catalog.RegisterHTTP(mcp.HTTPDescriptor{Name: "operator", URL: url}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	rw := mcpList(t, h, adminPrincipal("acme"))
	var listed struct{ Servers []mcpServerRow }
	_ = json.Unmarshal(rw.Body.Bytes(), &listed)
	if len(listed.Servers) != 0 {
		t.Fatalf("the operator's server is listed as the org's: %+v", listed.Servers)
	}
}

// TestMCPServersEndpoints_PutIgnoresABodyRename: the path names the server, so
// a mismatched body cannot re-key it behind the caller's back.
func TestMCPServersEndpoints_PutIgnoresABodyRename(t *testing.T) {
	h, _, url := mcpGateway(t)
	if rw := mcpPost(t, h, adminPrincipal("acme"), `{"name":"vendor","url":"`+url+`"}`); rw.Code != 200 {
		t.Fatalf("seed: code %d body %s", rw.Code, rw.Body)
	}
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/admin/mcp-servers/vendor",
		strings.NewReader(`{"name":"somethingelse","url":"`+url+`"}`))
	req.SetPathValue("name", "vendor")
	h.saveMCPServer(rw, req, adminPrincipal("acme"))
	if rw.Code != 200 {
		t.Fatalf("code %d body %s", rw.Code, rw.Body)
	}
	var saved mcpServerRow
	_ = json.Unmarshal(rw.Body.Bytes(), &saved)
	if saved.Name != "vendor" {
		t.Fatalf("name = %q, want the path's name to win", saved.Name)
	}
	rw = mcpList(t, h, adminPrincipal("acme"))
	var listed struct{ Servers []mcpServerRow }
	_ = json.Unmarshal(rw.Body.Bytes(), &listed)
	if len(listed.Servers) != 1 {
		t.Fatalf("the body's name created a second server: %+v", listed.Servers)
	}
}

// TestMCPServersEndpoints_PutOmittingEnabledKeepsItOn: Enabled is a pointer so
// an edit that does not mention it cannot silently switch a working server off.
func TestMCPServersEndpoints_PutOmittingEnabledKeepsItOn(t *testing.T) {
	h, _, url := mcpGateway(t)
	if rw := mcpPost(t, h, adminPrincipal("acme"), `{"name":"vendor","url":"`+url+`"}`); rw.Code != 200 {
		t.Fatalf("seed: code %d body %s", rw.Code, rw.Body)
	}
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/admin/mcp-servers/vendor",
		strings.NewReader(`{"url":"`+url+`"}`))
	req.SetPathValue("name", "vendor")
	h.saveMCPServer(rw, req, adminPrincipal("acme"))
	var saved mcpServerRow
	_ = json.Unmarshal(rw.Body.Bytes(), &saved)
	if !saved.Enabled {
		t.Fatal("an edit that omitted `enabled` switched the server off")
	}
}

// TestMCPServersEndpoints_Usage covers the lookup behind the delete warning:
// scoped to a server that exists, and answered for the caller's own org.
func TestMCPServersEndpoints_Usage(t *testing.T) {
	svc, _ := newTestMCPServers(t)
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	ws, err := workspace.OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	h := &HTTPGateway{MCPServers: svc, svc: &Service{Workspaces: MapWorkspaces{"acme/ws1": ws}}}

	if rw := mcpPost(t, h, adminPrincipal("acme"), `{"label":"MCP Test","url":"`+srv.URL+`"}`); rw.Code != 200 {
		t.Fatalf("save code %d body %s", rw.Code, rw.Body)
	}
	if _, err := ws.Save(core.Graph{
		ID: "nightly", Name: "Nightly sync", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "mcp:mcp-test:search"}},
	}, "tester"); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	usage := mcpUsage(t, h, adminPrincipal("acme"), "mcp-test")
	if usage.Code != 200 {
		t.Fatalf("usage code %d body %s", usage.Code, usage.Body)
	}
	var got StepSourceUsage
	if err := json.Unmarshal(usage.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Flows) != 1 || got.Flows[0].FlowID != "nightly" {
		t.Fatalf("flows = %+v, want the one referencing flow", got.Flows)
	}

	// A name that is not a server of this org gets a 404, not a confident
	// "nothing uses this".
	if rw := mcpUsage(t, h, adminPrincipal("acme"), "typo"); rw.Code != 404 {
		t.Errorf("unknown server usage code %d, want 404", rw.Code)
	}
	// And it is admin-only, like every other route on this page.
	if rw := mcpUsage(t, h, editorPrincipal("acme"), "mcp-test"); rw.Code != 403 {
		t.Errorf("editor usage code %d, want 403", rw.Code)
	}
}

func mcpUsage(t *testing.T, h *HTTPGateway, p core.Principal, name string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/admin/mcp-servers/"+name+"/usage", nil)
	req.SetPathValue("name", name)
	h.mcpServerUsage(rw, req, p)
	return rw
}

func TestMCPServersEndpoints_DeleteUnknownIs404(t *testing.T) {
	h, _, _ := mcpGateway(t)
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/admin/mcp-servers/nope", nil)
	req.SetPathValue("name", "nope")
	h.deleteMCPServer(rw, req, adminPrincipal("acme"))
	if rw.Code != 404 {
		t.Fatalf("code %d, want 404", rw.Code)
	}
}

func TestMCPServersEndpoints_BadInputIs400(t *testing.T) {
	h, _, _ := mcpGateway(t)
	rw := mcpPost(t, h, adminPrincipal("acme"), `{"name":"Vendor Ltd","url":"https://x.test/mcp"}`)
	if rw.Code != 400 {
		t.Fatalf("code %d body %s, want 400", rw.Code, rw.Body)
	}
	rw = mcpPost(t, h, adminPrincipal("acme"), `not json`)
	if rw.Code != 400 {
		t.Fatalf("malformed body code %d, want 400", rw.Code)
	}
}
