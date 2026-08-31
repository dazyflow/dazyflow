// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestRunFlowMe_RunAndMissing_Cov4 covers runFlowMe + runGraph: the clean 404 for a missing
// flow and the happy-path 202 submit for an existing one.
func TestRunFlowMe_RunAndMissing_Cov4(t *testing.T) {
	h := newGatewayHarness(t)

	// Unknown flow -> 404 flow_not_found.
	missing := url.PathEscape("t/ws/ghost")
	if rw := h.do(t, "POST", "/api/v1/me/flows/"+missing+"/run", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("run(missing) = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	// Save a flow, then run it -> 202 with a job id.
	g := core.Graph{
		ID: "runnable", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	if _, err := h.ws.Save(g, "alice"); err != nil {
		t.Fatalf("save flow: %v", err)
	}
	fid := url.PathEscape("t/ws/runnable")
	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/run", nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("run(runnable) = %d, want 202; body=%s", rw.Code, rw.Body.String())
	}
}
