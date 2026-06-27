package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// TestAdminDeleteUserHandler_Cov covers adminDeleteUserHandler's guard legs:
// non-platform-admin (403), no user store (501), missing confirm (400),
// unknown user (404), and the happy path on an existing personal-tenant user.
func TestAdminDeleteUserHandler_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Non-platform-admin (default editor token) -> 403.
	if rw := h.do(t, "DELETE", "/api/v1/admin/users/a@x.test?confirm=a@x.test", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("non-admin = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}

	// Platform admin but no Users store -> 501.
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/a@x.test?confirm=a@x.test", nil); rw.Code != http.StatusNotImplemented {
		t.Fatalf("no user store = %d, want 501; body=%s", rw.Code, rw.Body.String())
	}

	// Wire the stores the erase path touches.
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	h.gw.Sessions = auth.NewMemSessionStore()

	// Missing/wrong confirm -> 400.
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/a@x.test", nil); rw.Code != http.StatusBadRequest {
		t.Fatalf("missing confirm = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	// Confirm matches but user doesn't exist -> 404.
	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/ghost@x.test?confirm=ghost@x.test", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	// Happy path: a personal-tenant user is erased -> 200.
	_ = users.PutUser(t.Context(), auth.User{
		Email: "del@x.test", Subject: "del@x.test", Tenant: "usr_del", Workspace: "main",
	})
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/del@x.test?confirm=del@x.test", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("erase = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if _, err := users.GetByEmail(t.Context(), "del@x.test"); err == nil {
		t.Error("user should be gone after erase")
	}
}
