// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// --- runFlowMe --------------------------------------------------------

func TestRunFlowMe_NotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fghost/run", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("run missing flow = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestRunFlowMe_OK(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{ID: "f1", Tenant: "t", Workspace: "ws", Nodes: []core.Node{
		{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
	}}
	if _, err := h.ws.Save(g, "u"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rw := h.do(t, "POST", "/api/v1/me/flows/"+cov3FlowID+"/run", nil)
	if rw.Code != http.StatusAccepted {
		t.Fatalf("run flow = %d (%s), want 202", rw.Code, rw.Body.String())
	}
}

// --- validateGraphLiteral --------------------------------------------

func TestValidateGraphLiteral_BadJSON(t *testing.T) {
	h := newGatewayHarness(t)
	req := newRawReq(t, h, "POST", "/api/v1/validate/graph", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("validate bad json = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestValidateGraphLiteral_OK(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/validate/graph", core.Graph{
		ID: "x", Nodes: []core.Node{{ID: "a", Module: "noop"}},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("validate literal = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

// --- removeMember -----------------------------------------------------

func TestRemoveMember_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no Memberships
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/members/victim@example.com", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("remove no memberships = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestRemoveMember_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Memberships = newFakeMembershipStore()
	// Default editor token lacks organization:admin.
	rw := h.do(t, "DELETE", "/api/v1/admin/members/victim@example.com", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin remove = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestRemoveMember_CrossTenantForbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Memberships = newFakeMembershipStore()
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/members/victim@example.com?tenant=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant remove = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestRemoveMember_OwnerConflict(t *testing.T) {
	h := newGatewayHarness(t)
	mem := newFakeMembershipStore()
	users, _ := auth.OpenJSONUserStore("")
	// The owner's home tenant equals the org tenant "t" -> protected.
	_ = users.PutUser(t.Context(), auth.User{Email: "owner@example.com", Subject: "owner@example.com", Tenant: "t"})
	h.gw.Memberships = mem
	h.gw.Users = users
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/members/owner@example.com", nil)
	if rw.Code != http.StatusConflict {
		t.Fatalf("remove owner = %d (%s), want 409", rw.Code, rw.Body.String())
	}
}

func TestRemoveMember_OK(t *testing.T) {
	h := newGatewayHarness(t)
	mem := newFakeMembershipStore()
	_ = mem.PutMembership(t.Context(), auth.Membership{UserEmail: "guest@example.com", Tenant: "t", Workspace: "ws"})
	h.gw.Memberships = mem
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/members/guest@example.com", nil)
	if rw.Code != http.StatusOK && rw.Code != http.StatusNoContent {
		t.Fatalf("remove member = %d (%s), want 200/204", rw.Code, rw.Body.String())
	}
}
