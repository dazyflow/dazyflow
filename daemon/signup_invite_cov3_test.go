// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

func TestListSignupInvites_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no Invitations
	rw := h.platformDo(t, "GET", "/api/v1/admin/signup-invites", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("list invites no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestListSignupInvites_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	inv, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = inv
	// Tenant-admin (organization:admin) is not platform:admin.
	rw := h.adminDo(t, "GET", "/api/v1/admin/signup-invites", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("tenant-admin list invites = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestRevokeSignupInvite_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/signup-invites/sometoken", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("revoke invite no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestRevokeSignupInvite_Forbidden(t *testing.T) {
	h := newGatewayHarness(t)
	inv, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = inv
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/signup-invites/sometoken", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("tenant-admin revoke invite = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestRevokeSignupInvite_NotFound(t *testing.T) {
	h := newGatewayHarness(t)
	inv, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = inv
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/signup-invites/ghosttoken", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown invite = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}
