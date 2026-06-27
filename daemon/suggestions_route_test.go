// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestSuggestionsRoute confirms GET /me/flows/suggestions resolves to the
// suggestions handler (the literal segment must outrank the {flow_id}
// wildcard) and returns the mined adjacency for the caller's workspace.
func TestSuggestionsRoute(t *testing.T) {
	h := newGatewayHarness(t)
	p := core.Principal{
		Subject: "alice", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{
			core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		}}},
	}
	if _, err := h.svc.SaveGraph(context.Background(), p, core.Graph{
		ID: "f", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "http_fetch"},
			{ID: "b", Module: "parse_json"},
		},
		Edges: []core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	rw := h.do(t, "GET", "/api/v1/me/flows/suggestions", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (route should not be swallowed by {flow_id})", rw.Code)
	}
	var resp struct {
		Items []DropAdjacency `json:"items"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].From != "http_fetch" || resp.Items[0].To != "parse_json" {
		t.Errorf("items = %+v, want one http_fetch→parse_json entry", resp.Items)
	}
}
