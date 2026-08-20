// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// These tests drive the /me/flows HTTP handlers' error / authz / validation
// branches that the service-level tests don't reach.

const cov3FlowID = "t%2Fws%2Ff1"

// --- readFlowID branches (shared by every /me/flows/{id} handler) -----

func TestMeFlows_BadFlowID(t *testing.T) {
	h := newGatewayHarness(t)
	// A flow_id without two slashes is invalid -> 400 invalid_flow_id.
	rw := h.do(t, "GET", "/api/v1/me/flows/justanid/published", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("bad flow_id = %d (%s), want 400", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "invalid_flow_id") {
		t.Errorf("body %s, want invalid_flow_id", rw.Body.String())
	}
}

func TestMeFlows_ForbiddenScope(t *testing.T) {
	h := newGatewayHarness(t)
	// Principal is bound to t/ws; acting on another tenant -> 403 forbidden_scope.
	rw := h.do(t, "GET", "/api/v1/me/flows/other%2Fws%2Ff1/published", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant = %d (%s), want 403", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "forbidden_scope") {
		t.Errorf("body %s, want forbidden_scope", rw.Body.String())
	}
	// Wrong workspace also forbidden.
	rw = h.do(t, "GET", "/api/v1/me/flows/t%2Fother%2Ff1/published", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace = %d, want 403", rw.Code)
	}
}

// --- publishedFlowMe / publishFlowMe / unpublishFlowMe ----------------

func TestPublishedFlowMe_NotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fghost/published", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("missing flow = %d, want 404", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "flow_not_found") {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestPublishedFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+cov3FlowID+"/published", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("published = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestPublishFlowMe_MissingFlowErrors(t *testing.T) {
	h := newGatewayHarness(t)
	// A missing flow surfaces as a resolve error mapped to 403 (the publish
	// service can't find HEAD to promote).
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/publish", nil)
	if rw.Code != http.StatusForbidden && rw.Code != http.StatusNotFound {
		t.Fatalf("publish missing = %d (%s), want 403/404", rw.Code, rw.Body.String())
	}
}

func TestPublishFlowMe_OK_WithLabel(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/publish", map[string]any{"label": "v2"})
	if rw.Code != http.StatusOK {
		t.Fatalf("publish = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "published_label") {
		t.Errorf("body %s, want published_label", rw.Body.String())
	}
}

// TestPublishFlowMe_MalformedBodyIsRejected pins that a body we cannot parse
// is a 400. It used to be logged and ignored, which published HEAD instead of
// the ref the client asked for — a successful publish of the WRONG commit,
// with only a server-side log line recording that anything went wrong.
func TestPublishFlowMe_MalformedBodyIsRejected(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	req := newRawReq(t, h, "POST", "/api/v1/me/flows/"+cov3FlowID+"/publish", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("malformed body publish = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

// An absent body still means "publish HEAD, no label" — the optional-body
// contract is unchanged.
func TestPublishFlowMe_EmptyBodyPublishesHead(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	req := newRawReq(t, h, "POST", "/api/v1/me/flows/"+cov3FlowID+"/publish", "")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("empty body publish = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestPublishFlowMe_Forbidden_NonAdmin(t *testing.T) {
	h := newRunOnlyHarness(t)
	covSeedFlow(t, h.gatewayHarness, "f1")
	rw := runOnlyDo(t, h, "POST", "/api/v1/me/flows/"+cov3FlowID+"/publish", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin publish = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestUnpublishFlowMe_MissingFlowErrors(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/unpublish", nil)
	if rw.Code != http.StatusForbidden && rw.Code != http.StatusNotFound {
		t.Fatalf("unpublish missing = %d (%s), want 403/404", rw.Code, rw.Body.String())
	}
}

func TestUnpublishFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	if rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/publish", nil); rw.Code != http.StatusOK {
		t.Fatalf("publish setup = %d", rw.Code)
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/unpublish", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("unpublish = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"published":false`) {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestUnpublishFlowMe_Forbidden_NonAdmin(t *testing.T) {
	h := newRunOnlyHarness(t)
	covSeedFlow(t, h.gatewayHarness, "f1")
	rw := runOnlyDo(t, h, "POST", "/api/v1/me/flows/"+cov3FlowID+"/unpublish", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin unpublish = %d, want 403", rw.Code)
	}
}

// --- restoreFlowMe ----------------------------------------------------

func TestRestoreFlowMe_MissingRef(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/restore", map[string]any{"ref": "  "})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("blank ref = %d (%s), want 400", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "validation_failed") {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestRestoreFlowMe_UnknownRef(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/restore", map[string]any{"ref": "deadbeef"})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unknown ref = %d (%s), want 400", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "restore_failed") {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestRestoreFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	// Save a second revision, then restore to the first.
	v1, _ := h.ws.History("f1", 1)
	g2 := core.Graph{ID: "f1", Tenant: "t", Workspace: "ws", Nodes: []core.Node{
		{ID: "a", Module: "noop"}, {ID: "b", Module: "noop"},
	}}
	if _, err := h.ws.Save(g2, "u"); err != nil {
		t.Fatalf("save v2: %v", err)
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/restore", map[string]any{"ref": v1[0].Commit})
	if rw.Code != http.StatusOK {
		t.Fatalf("restore = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

// --- labelRevisionMe --------------------------------------------------

func TestLabelRevisionMe_MissingFlowErrors(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/label", map[string]any{"label": "x"})
	if rw.Code != http.StatusForbidden && rw.Code != http.StatusNotFound {
		t.Fatalf("label missing = %d (%s), want 403/404", rw.Code, rw.Body.String())
	}
}

func TestLabelRevisionMe_Forbidden_NonAdmin(t *testing.T) {
	h := newRunOnlyHarness(t)
	covSeedFlow(t, h.gatewayHarness, "f1")
	rw := runOnlyDo(t, h, "POST", "/api/v1/me/flows/"+cov3FlowID+"/label", map[string]any{"label": "x"})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin label = %d, want 403", rw.Code)
	}
}

// --- enable / disable -------------------------------------------------

func TestEnableDisableFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	if rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/disable", nil); rw.Code != http.StatusOK {
		t.Fatalf("disable = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/enable", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("enable = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"enabled":true`) {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestSetFlowEnabled_Forbidden_NonAdmin(t *testing.T) {
	h := newRunOnlyHarness(t)
	covSeedFlow(t, h.gatewayHarness, "f1")
	rw := runOnlyDo(t, h, "POST", "/api/v1/me/flows/"+cov3FlowID+"/disable", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin disable = %d, want 403", rw.Code)
	}
}

// --- validateFlowMe ---------------------------------------------------

func TestValidateFlowMe_NotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/validate", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("validate missing = %d, want 404", rw.Code)
	}
}

func TestValidateFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/validate", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("validate = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"ok":`) {
		t.Errorf("body %s, want ok field", rw.Body.String())
	}
}

// --- historyFlowMe ----------------------------------------------------

func TestHistoryFlowMe_NotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fghost/history", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("history missing = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

// --- duplicateFlowMe --------------------------------------------------

func TestDuplicateFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	covSeedFlow(t, h, "f1")
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/duplicate", map[string]any{"name": "My copy"})
	if rw.Code != http.StatusCreated {
		t.Fatalf("duplicate = %d (%s), want 201", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "t/ws/f1-copy") {
		t.Errorf("body %s, want new flow_id t/ws/f1-copy", rw.Body.String())
	}
	// The copy is persisted as a disabled draft.
	g, err := h.ws.Load("f1-copy")
	if err != nil {
		t.Fatalf("load copy: %v", err)
	}
	if !g.Disabled {
		t.Error("duplicated flow should be persisted disabled")
	}
}

func TestDuplicateFlowMe_MissingSource(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/duplicate", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("duplicate missing = %d (%s), want 404", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "flow_not_found") {
		t.Errorf("body %s, want flow_not_found", rw.Body.String())
	}
}
