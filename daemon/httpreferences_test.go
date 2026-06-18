package daemon

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"

	_ "git.sr.ht/~klahr/dazyflow/drops" // register real manifests (sheets_*, webhook_input)
)

// refTokens flattens a references response into a kind→set-of-tokens map
// for easy assertions.
func refTokens(t *testing.T, body []byte) map[string]map[string]bool {
	t.Helper()
	var resp struct {
		Groups map[string][]struct {
			Token string `json:"token"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, body)
	}
	out := map[string]map[string]bool{}
	for kind, items := range resp.Groups {
		set := map[string]bool{}
		for _, it := range items {
			set[it.Token] = true
		}
		out[kind] = set
	}
	return out
}

func TestReferences_UpstreamAncestorsTriggerAndSecrets(t *testing.T) {
	h := newSecretsHarness(t) // editor + secret perms, store wired

	// Diamond: trigger → read → append; `other` is unconnected.
	// References for `append` must include read's outputs and the trigger
	// fields, but NOT the unconnected `other` node nor append itself.
	g := core.Graph{
		ID: "leads", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "trigger", Module: "webhook_input", Params: map[string]any{
				"public_form": true, "form_fields": []any{"name", "email"},
			}},
			{ID: "read", Module: "sheets_read_range", Params: map[string]any{"spreadsheet_id": "S"}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
			{ID: "other", Module: "sheets_read_range", Params: map[string]any{"spreadsheet_id": "Z"}},
		},
		Edges: []core.Edge{
			{From: "trigger", FromPort: "body", To: "read", ToPort: "in"},
			{From: "read", FromPort: "rows", To: "append", ToPort: "rows"},
		},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	// One org secret, one flow secret — both should surface.
	h.do(t, "PUT", "/api/v1/secrets/ORG_KEY", json.RawMessage(putBody("v1")))
	h.do(t, "PUT", "/api/v1/secrets/FLOW_KEY?scope=flow&flow=leads", json.RawMessage(putBody("v2")))

	flowID := url.PathEscape("t/ws/leads")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/references?node=append", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	tokens := refTokens(t, rw.Body.Bytes())

	up := tokens["upstream"]
	if !up["${upstream.read.rows}"] || !up["${upstream.read.headers}"] {
		t.Errorf("upstream missing read ports: %v", up)
	}
	if up["${upstream.other.rows}"] {
		t.Errorf("upstream leaked unconnected node `other`: %v", up)
	}
	for tok := range up {
		if strings.HasPrefix(tok, "${upstream.append") {
			t.Errorf("upstream must not include the target node itself: %s", tok)
		}
	}

	trig := tokens["trigger"]
	if !trig["${trigger.body.name}"] || !trig["${trigger.body.email}"] {
		t.Errorf("trigger fields missing: %v", trig)
	}

	sec := tokens["secrets"]
	if !sec["${secret.ORG_KEY}"] || !sec["${secret.FLOW_KEY}"] {
		t.Errorf("secrets missing flow/org names: %v", sec)
	}
}

// A row-source upstream (here: Gmail search, whose match stubs always carry
// id + threadId) also offers ready-made FIRST-ROW field tokens, so a user
// can pick "first match → id" instead of hand-typing the [0].id syntax.
func TestReferences_FirstRowFieldTokens(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "fr", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "search", Module: "gmail_search_messages", Params: map[string]any{"query": "x"}},
			{ID: "readmail", Module: "gmail_get_message", Params: map[string]any{}},
		},
		Edges: []core.Edge{
			{From: "search", FromPort: "pass", To: "readmail", ToPort: "pass"},
		},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/fr")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/references?node=readmail", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	up := refTokens(t, rw.Body.Bytes())["upstream"]
	if !up["${upstream.search.messages}"] {
		t.Errorf("whole-port token missing: %v", up)
	}
	if !up["${upstream.search.messages[0].subject}"] || !up["${upstream.search.messages[0].from}"] {
		t.Errorf("first-row field tokens missing: %v", up)
	}
}

func TestReferences_NoNodeListsAllNodes(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "f2", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "sheets_read_range", Params: map[string]any{"spreadsheet_id": "S"}},
			{ID: "b", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
		},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/f2")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/references", nil) // no ?node
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	up := refTokens(t, rw.Body.Bytes())["upstream"]
	if !up["${upstream.a.rows}"] {
		t.Errorf("with no node, all nodes should be listed: %v", up)
	}
}
