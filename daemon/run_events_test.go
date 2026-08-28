// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// createFlowViaAPI saves a flow through the gateway PUT route and returns
// the URL-encoded flow id (t%2Fws%2F<id>).
func createFlowViaAPI(t *testing.T, h *gatewayHarness, id string, nodes []core.Node) string {
	t.Helper()
	fid := "t%2Fws%2F" + id
	rw := h.do(t, "PUT", "/api/v1/me/flows/"+fid, core.Graph{
		ID: id, Tenant: "t", Workspace: "ws", Nodes: nodes,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("create flow %s: code=%d body=%s", id, rw.Code, rw.Body.String())
	}
	return fid
}

func TestRunFlowMe_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	fid := createFlowViaAPI(t, h, "runme", []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}})

	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/run", nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("run = %d: %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.JobID == "" {
		t.Fatal("no job_id returned")
	}

	// Running a missing flow -> 404 (clean flow_not_found).
	if rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/run", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("run ghost = %d, want 404", rw.Code)
	}
}

func TestTestTriggerFlowMe_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Flow WITHOUT a webhook node -> 400.
	noWebhook := createFlowViaAPI(t, h, "nowh", []core.Node{{ID: "a", Module: "noop"}})
	if rw := h.do(t, "POST", "/api/v1/me/flows/"+noWebhook+"/test-trigger", map[string]any{"x": 1}); rw.Code != http.StatusBadRequest {
		t.Fatalf("test-trigger without webhook = %d, want 400", rw.Code)
	}

	// Flow WITH a webhook_input node -> accepted.
	wh := createFlowViaAPI(t, h, "wh", []core.Node{{ID: "w", Module: webhookInputModuleID}})
	rw := h.do(t, "POST", "/api/v1/me/flows/"+wh+"/test-trigger", map[string]any{"hello": "world"})
	if rw.Code != http.StatusAccepted {
		t.Fatalf("test-trigger = %d: %s", rw.Code, rw.Body.String())
	}
}

func TestJobEvents_TerminalSnapshot_Cov(t *testing.T) {
	h := newGatewayHarness(t)
	fid := createFlowViaAPI(t, h, "ev", []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}})
	ctx := context.Background()

	// Submit + force terminal so the events stream returns immediately.
	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/run", nil)
	var resp struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if err := h.store.Complete(ctx, resp.JobID, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	rw = h.do(t, "GET", "/api/v1/me/runs/"+resp.JobID+"/events", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("events = %d: %s", rw.Code, rw.Body.String())
	}
	body := rw.Body.String()
	if !strings.Contains(body, "event: snapshot") {
		t.Errorf("no snapshot frame in:\n%s", body)
	}
	if !strings.Contains(body, "event: terminal") {
		t.Errorf("no terminal frame in:\n%s", body)
	}

	// Unknown run -> 404.
	if rw := h.do(t, "GET", "/api/v1/me/runs/nope/events", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("events unknown = %d, want 404", rw.Code)
	}
}
