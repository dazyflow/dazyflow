// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// covSeedScheduledFlow saves a flow carrying a cron_trigger node so the
// schedule listing and the trigger enable/disable handlers have something to
// act on.
func covSeedScheduledFlow(t *testing.T, h *gatewayHarness, id, nodeID string) {
	t.Helper()
	g := core.Graph{ID: id, Tenant: "t", Workspace: "ws", Nodes: []core.Node{
		{ID: nodeID, Module: "cron_trigger", Params: map[string]any{"cron": "0 * * * *"}},
		{ID: "a", Module: "noop"},
	}}
	if _, err := h.ws.Save(g, "u"); err != nil {
		t.Fatalf("seed scheduled flow: %v", err)
	}
}

func TestListSchedulesMe_MissingScope(t *testing.T) {
	h := newGatewayHarness(t)
	role := core.Role{Name: "free", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-unbound2", "", "", "nobody", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	saved := h.token
	h.token = tok
	defer func() { h.token = saved }()
	rw := h.do(t, "GET", "/api/v1/me/schedules", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unbound schedules = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestListSchedulesMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedScheduledFlow(t, h, "sched1", "trig")
	rw := h.do(t, "GET", "/api/v1/me/schedules", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("schedules = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "sched1") {
		t.Errorf("body %s, want sched1", rw.Body.String())
	}
}

func TestSetTriggerEnabled_BadFlowID(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/flows/badid/triggers/trig/disable", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("bad flow_id = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestSetTriggerEnabled_NodeNotFound(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedScheduledFlow(t, h, "sched1", "trig")
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fsched1/triggers/ghostnode/disable", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("missing node = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestSetTriggerEnabled_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedScheduledFlow(t, h, "sched1", "trig")
	if rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fsched1/triggers/trig/disable", nil); rw.Code != http.StatusOK {
		t.Fatalf("disable trigger = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fsched1/triggers/trig/enable", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("enable trigger = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"enabled":true`) {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestSetTriggerEnabled_Forbidden_NonAdmin(t *testing.T) {
	h := newRunOnlyHarness(t)
	covSeedScheduledFlow(t, h.gatewayHarness, "sched1", "trig")
	rw := runOnlyDo(t, h, "POST", "/api/v1/me/flows/t%2Fws%2Fsched1/triggers/trig/disable", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin trigger toggle = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}
