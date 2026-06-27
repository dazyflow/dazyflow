package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// TestListInvitations_Cov covers listInvitations: no-store (501), non-admin
// (403), cross-tenant refusal (403), and the happy path returning a pending
// invitation.
func TestListInvitations_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// No Invitations store -> 501 (admin token so the store guard is reached).
	if rw := h.adminDo(t, "GET", "/api/v1/admin/invitations", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("no store = %d, want 501; body=%s", rw.Code, rw.Body.String())
	}

	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites

	// Non-admin (default editor token) -> 403.
	if rw := h.do(t, "GET", "/api/v1/admin/invitations", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}

	// Cross-tenant (admin of "t" asking for "other") -> 403.
	if rw := h.adminDo(t, "GET", "/api/v1/admin/invitations?tenant=other", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}

	// Seed a pending invitation in the admin's tenant ("t") and list it.
	_ = invites.PutInvitation(t.Context(), auth.Invitation{
		Token: "inv1", Email: "new@t.test", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{core.TeamRoleEditor()}, ExpiresAt: time.Now().Add(time.Hour),
	})
	rw := h.adminDo(t, "GET", "/api/v1/admin/invitations", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Invitations []map[string]any `json:"invitations"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Invitations) != 1 || resp.Invitations[0]["email"] != "new@t.test" {
		t.Fatalf("invitations = %s", rw.Body.String())
	}
}
