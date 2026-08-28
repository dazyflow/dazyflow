// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	// Column order is folded onto the rows value now, so there's no separate
	// `headers` output port/reference — just `rows`.
	if !up["${upstream.read.rows}"] {
		t.Errorf("upstream missing read.rows port: %v", up)
	}
	if up["${upstream.read.headers}"] {
		t.Errorf("read.headers should be folded away (no separate headers port): %v", up)
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

// The picker names each upstream step, and a step the author renamed has to be
// named the way they named it: a reference reads as "<step> · <port>", and
// naming it differently here to the way it is named on the canvas is how you
// end up hunting for a step that is right in front of you.
func TestReferences_UpstreamCarriesTheAuthorsStepName(t *testing.T) {
	h := newSecretsHarness(t)
	g := core.Graph{
		ID: "named", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "read", Module: "sheets_read_range", Label: "Yesterday's orders",
				Params: map[string]any{"spreadsheet_id": "S"}},
			{ID: "plain", Module: "sheets_read_range", Params: map[string]any{"spreadsheet_id": "T"}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
		},
		Edges: []core.Edge{
			{From: "read", FromPort: "rows", To: "append", ToPort: "rows"},
			{From: "plain", FromPort: "rows", To: "append", ToPort: "rows"},
		},
	}
	if _, err := h.ws.Save(g, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}
	rw := h.do(t, "GET", "/api/v1/me/flows/"+url.PathEscape("t/ws/named")+"/references?node=append", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	var out struct {
		Groups struct {
			Upstream []struct {
				NodeID    string `json:"node_id"`
				NodeLabel string `json:"node_label"`
			} `json:"upstream"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]string{}
	for _, i := range out.Groups.Upstream {
		seen[i.NodeID] = i.NodeLabel
	}
	if got := seen["read"]; got != "Yesterday's orders" {
		t.Errorf("renamed step is called %q in the picker, want its own name", got)
	}
	// A step nobody renamed still goes by its drop's name.
	if got := seen["plain"]; got == "" || got == "plain" {
		t.Errorf("unnamed step is called %q, want the drop's label", got)
	}
}
