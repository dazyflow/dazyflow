package daemon

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func putResourceBodyJSON(typ string, config map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "config": config})
	return b
}

// saveFlowGraph creates a flow graph owned by the harness principal ("alice")
// so flow-scoped secret/resource CRUD authorizes (authorizeFlowSecretScope
// resolves the flow's graph and gates on AuthorizeGraphEdit/View — a flow that
// doesn't exist is correctly rejected as forbidden).
func saveFlowGraph(t *testing.T, h *gatewayHarness, id string) {
	t.Helper()
	if _, err := h.ws.Save(core.Graph{ID: id, Tenant: "t", Workspace: "ws", Owner: "alice"}, "alice"); err != nil {
		t.Fatalf("save flow %q: %v", id, err)
	}
}

func TestResources_FlowCRUDRoundTrip(t *testing.T) {
	h := newSecretsHarness(t)
	saveFlowGraph(t, h, "f1")
	cfg := map[string]any{"spreadsheet_id": "S1", "range": "Leads", "account": "default"}
	rw := h.do(t, "PUT", "/api/v1/resources/leads?scope=flow&flow=f1",
		json.RawMessage(putResourceBodyJSON("google_sheet", cfg)))
	if rw.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", rw.Code, rw.Body.String())
	}

	rw = h.do(t, "GET", "/api/v1/resources?scope=flow&flow=f1", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Resources []struct {
			Name   string         `json:"name"`
			Type   string         `json:"type"`
			Config map[string]any `json:"config"`
		} `json:"resources"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Resources) != 1 {
		t.Fatalf("want 1 resource, got %+v", resp.Resources)
	}
	r := resp.Resources[0]
	if r.Name != "leads" || r.Type != "google_sheet" || r.Config["spreadsheet_id"] != "S1" {
		t.Errorf("resource = %+v", r)
	}

	// Delete, then it's gone.
	if rw := h.do(t, "DELETE", "/api/v1/resources/leads?scope=flow&flow=f1", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", rw.Code)
	}
	rw = h.do(t, "GET", "/api/v1/resources?scope=flow&flow=f1", nil)
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Resources) != 0 {
		t.Errorf("resource should be gone, got %+v", resp.Resources)
	}
}

func TestResources_HiddenFromSecretsListing(t *testing.T) {
	h := newSecretsHarness(t)
	saveFlowGraph(t, h, "f1")
	h.do(t, "PUT", "/api/v1/resources/leads?scope=flow&flow=f1",
		json.RawMessage(putResourceBodyJSON("google_sheet", map[string]any{"spreadsheet_id": "S1"})))
	// A flow secret too, to be sure the listing works at all.
	h.do(t, "PUT", "/api/v1/secrets/MY_KEY?scope=flow&flow=f1", json.RawMessage(putBody("v")))

	rw := h.do(t, "GET", "/api/v1/secrets?scope=flow&flow=f1", nil)
	var resp struct {
		Secrets []string `json:"secrets"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	for _, n := range resp.Secrets {
		if strings.HasPrefix(n, "res.") || n == "leads" {
			t.Errorf("resource leaked into secrets listing: %v", resp.Secrets)
		}
	}
}

func TestResources_RejectsBadInput(t *testing.T) {
	h := newSecretsHarness(t)
	saveFlowGraph(t, h, "f1")
	// Empty type.
	rw := h.do(t, "PUT", "/api/v1/resources/leads?scope=flow&flow=f1",
		json.RawMessage(putResourceBodyJSON("", map[string]any{})))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("empty type: status=%d, want 400", rw.Code)
	}
	// Dotted name (unreferenceable — ${resource.a.b} would split wrong).
	rw = h.do(t, "PUT", "/api/v1/resources/a.b?scope=flow&flow=f1",
		json.RawMessage(putResourceBodyJSON("google_sheet", map[string]any{})))
	if rw.Code != http.StatusBadRequest {
		t.Errorf("dotted name: status=%d, want 400", rw.Code)
	}
}

func TestReferences_IncludesResources(t *testing.T) {
	h := newSecretsHarness(t)
	// A flow with one node so the references endpoint loads a real graph.
	g := core.Graph{
		ID: "rf", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n1", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}}},
	}
	if _, err := h.ws.Save(g, "t"); err != nil {
		t.Fatalf("save: %v", err)
	}
	h.do(t, "PUT", "/api/v1/resources/leads?scope=flow&flow=rf",
		json.RawMessage(putResourceBodyJSON("google_sheet", map[string]any{"spreadsheet_id": "S"})))

	flowID := url.PathEscape("t/ws/rf")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/references?node=n1", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	res := refTokens(t, rw.Body.Bytes())["resources"]
	if !res["${resource.leads}"] || !res["${resource.leads.rows}"] || !res["${resource.leads.headers}"] {
		t.Errorf("resources group missing token/sub-paths: %v", res)
	}
}
