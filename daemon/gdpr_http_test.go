// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// HTTP-handler branches of the GDPR erasure endpoints.

func TestDeleteMyAccount_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // gw.Users is nil
	rw := h.do(t, "DELETE", "/api/v1/me/account", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("no user store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestDeleteMyAccount_ConfirmationRequired(t *testing.T) {
	user := auth.User{Subject: "del@example.com", Email: "del@example.com", Tenant: "home", Workspace: "main"}
	h, _, _, tok := orgsSessionHarness(t, user)
	// No ?confirm= -> 400 confirmation_required.
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/account", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("no confirm = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestDeleteMyAccount_WrongConfirm(t *testing.T) {
	user := auth.User{Subject: "del@example.com", Email: "del@example.com", Tenant: "home", Workspace: "main"}
	h, _, _, tok := orgsSessionHarness(t, user)
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/account?confirm=other@example.com", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirm = %d, want 400", rw.Code)
	}
}

func TestDeleteMyAccount_OK(t *testing.T) {
	user := auth.User{Subject: "del@example.com", Email: "del@example.com", Tenant: "shared", Workspace: "main"}
	h, _, _, tok := orgsSessionHarness(t, user)
	// Confirm matching the email; shared (non-personal) tenant so no org cascade.
	rw := sessionDo(t, h, tok, "DELETE", "/api/v1/me/account?confirm=del@example.com", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("delete self = %d (%s), want 200", rw.Code, rw.Body.String())
	}
}

func TestAdminDeleteUser_NotPlatformAdmin(t *testing.T) {
	h := newGatewayHarness(t)
	// Tenant-admin token, not platform admin -> 403.
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/users/victim@example.com?confirm=victim@example.com", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("tenant admin = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestAdminDeleteUser_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // Users nil
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/victim@example.com?confirm=victim@example.com", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("no user store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestAdminDeleteUser_ConfirmationRequired(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(t.Context(), auth.User{Email: "victim@example.com", Subject: "victim@example.com", Tenant: "shared"})
	h.gw.Users = users
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/victim@example.com", nil)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("no confirm = %d (%s), want 400", rw.Code, rw.Body.String())
	}
}

func TestAdminDeleteUser_UnknownUser(t *testing.T) {
	h := newGatewayHarness(t)
	users, _ := auth.OpenJSONUserStore("")
	h.gw.Users = users
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/users/ghost@example.com?confirm=ghost@example.com", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestAdminDeleteOrg_BadRequest(t *testing.T) {
	h := newGatewayHarness(t)
	// Empty tenant path segment isn't routable; use a real tenant that the
	// caller can't manage to exercise the forbidden branch instead.
	rw := h.do(t, "DELETE", "/api/v1/admin/orgs/stranger?confirm=stranger", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("non-manager delete org = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestAdminDeleteOrg_SessionRequired(t *testing.T) {
	h := newGatewayHarness(t)
	// Org-admin API key can manage own tenant "t" but org deletion needs a
	// session credential -> 403 session_required.
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/orgs/t?confirm=t", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("api-key org delete = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

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
