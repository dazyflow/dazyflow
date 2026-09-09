// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops" // real manifests: webhook_input, builtin_store_append
)

// A wire from the Webhook trigger's Headers output is a legitimate connection:
// the port is declared and carries application/json, which is exactly what a
// Collections "Rows" input takes.
//
// It used to be deleted. A data-model migration dropped every edge whose port was
// NAMED "headers" — matched by name, with no manifest check — on every save AND
// every load. So the canvas accepted the wire, the autosave returned 200, and
// the edge was gone by the time anything read the flow back, failing the run
// with missing_input on an input the author had just connected.
//
// Ports are removed from manifests, not filtered by name, so the round-trip must
// carry whatever the port rules accept.
func TestSaveLoadGraph_KeepsHeadersEdge(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	g := core.Graph{
		ID: "claude-status", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
		Nodes: []core.Node{
			{ID: "webhook_input_1", Module: "webhook_input",
				Params: map[string]any{"secrets": []any{"a-webhook-key"}}},
			{ID: "store_1", Module: "builtin_store_append",
				Params: map[string]any{"table": "Claude Status"}},
		},
		Edges: []core.Edge{
			{From: "webhook_input_1", FromPort: "headers", To: "store_1", ToPort: "rows"},
		},
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save a flow wiring the webhook's Headers output: %v", err)
	}

	got, err := h.svc.LoadGraph(ctx, h.alice, "t", "ws", "claude-status", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("headers wire did not survive the round-trip: %d edges, want 1 (%+v)",
			len(got.Edges), got.Edges)
	}
	if e := got.Edges[0]; e.FromPort != "headers" || e.ToPort != "rows" {
		t.Errorf("wrong wire came back: %+v", e)
	}
}
