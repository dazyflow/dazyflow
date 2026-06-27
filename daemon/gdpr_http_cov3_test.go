// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
