// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/url"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestPatchFlowMe_Cov covers patchFlowMe: invalid JSON (400), unknown flow
// (404), and a successful merge-patch (200) that renames a flow.
func TestPatchFlowMe_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	fid := url.PathEscape("t/ws/patchme")

	// Invalid JSON body -> 400 decode_failed.
	if rw := h.do(t, "PATCH", "/api/v1/me/flows/"+fid, "not-an-object"); rw.Code != http.StatusBadRequest {
		t.Fatalf("bad json = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	// Valid patch but the flow doesn't exist -> 404 flow_not_found.
	missing := url.PathEscape("t/ws/ghost")
	if rw := h.do(t, "PATCH", "/api/v1/me/flows/"+missing, map[string]any{"name": "x"}); rw.Code != http.StatusNotFound {
		t.Fatalf("missing flow = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	// Save a flow, then patch its name -> 200.
	g := core.Graph{
		ID: "patchme", Tenant: "t", Workspace: "ws", Name: "Old Name",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	if _, err := h.ws.Save(g, "alice"); err != nil {
		t.Fatalf("save: %v", err)
	}
	rw := h.do(t, "PATCH", "/api/v1/me/flows/"+fid, map[string]any{"name": "New Name"})
	if rw.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	// Confirm the rename landed.
	got, err := h.ws.Load("patchme")
	if err != nil || got.Name != "New Name" {
		t.Fatalf("after patch name = %q (err=%v), want New Name", got.Name, err)
	}
}
