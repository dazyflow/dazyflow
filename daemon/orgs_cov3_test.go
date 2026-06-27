package daemon

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// --- createOrg validation branches ------------------------------------

func TestCreateOrg_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no Memberships/Profiles
	rw := h.do(t, "POST", "/api/v1/me/orgs", map[string]any{"display_name": "X"})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("create org no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestCreateOrg_DecodeError(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Memberships = newFakeMembershipStore()
	h.gw.Profiles = newRecordingOrgProfiles()
	req := newRawReq(t, h, "POST", "/api/v1/me/orgs", "{not json")
	rw := serveRaw(h, req)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("create org malformed = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestCreateOrg_NameTooLong(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.Memberships = newFakeMembershipStore()
	h.gw.Profiles = newRecordingOrgProfiles()
	rw := h.do(t, "POST", "/api/v1/me/orgs", map[string]any{"display_name": strings.Repeat("x", 81)})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("long name = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

// --- getOrgAuthConfig / deleteOrgAuthConfig ---------------------------

func TestGetOrgAuthConfig_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no OrgAuth
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("no OrgAuth = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestGetOrgAuthConfig_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	// Default editor token lacks organization:admin.
	rw := h.do(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin OrgAuth = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestGetOrgAuthConfig_CrossTenantForbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config?tenant=other", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant OrgAuth = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestGetOrgAuthConfig_UnknownReturnsDefault(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("default OrgAuth = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"google_enabled":false`) {
		t.Errorf("body %s", rw.Body.String())
	}
}

func TestGetOrgAuthConfig_OK(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{
		Tenant: "t", GoogleClientID: "cid", GoogleClientSecret: "csec",
	})
	rw := h.adminDo(t, "GET", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get OrgAuth = %d (%s), want 200", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"google_secret_set":true`) {
		t.Errorf("body %s, want secret_set", rw.Body.String())
	}
}

func TestDeleteOrgAuthConfig_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("delete no OrgAuth = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestDeleteOrgAuthConfig_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	rw := h.do(t, "DELETE", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete OrgAuth = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestDeleteOrgAuthConfig_OK(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.OrgAuth = newMemOrgAuth()
	_ = h.gw.OrgAuth.PutOrgAuth(context.Background(), auth.OrgAuthConfig{Tenant: "t", GoogleClientID: "cid"})
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/org/auth-config", nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("delete OrgAuth = %d (%s), want 204", rw.Code, rw.Body.String())
	}
}
