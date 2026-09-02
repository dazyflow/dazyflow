// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

var emailKeyCounter atomic.Int64

// emailTokenDo runs an authed request under an API key whose Subject is an
// email (acceptInvitation requires an @-bearing identity).
func emailTokenDo(t *testing.T, h *gatewayHarness, email, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	role := core.Role{Name: "ed", Permissions: []core.Permission{core.PermGraphRun}}
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-email-"+strconv.FormatInt(emailKeyCounter.Add(1), 10), "t", "ws", email, []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	req := httptest.NewRequest(method, path, bytes.NewBuffer(nil))
	req.Header.Set("Authorization", "Bearer "+tok)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// TestAcceptInvitation_Cov covers acceptInvitation: no-store (501), unknown
// token (404), wrong-email (403), expired (410), and the happy path (200).
func TestAcceptInvitation_Cov(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)

	// No invitation/membership stores -> 501.
	if rw := emailTokenDo(t, h, "u1@t.test", "POST", "/api/v1/invitations/tok/accept"); rw.Code != http.StatusNotImplemented {
		t.Fatalf("no store = %d, want 501; body=%s", rw.Code, rw.Body.String())
	}

	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites
	h.gw.Memberships = newFakeMembershipStore()

	// Unknown token -> 404.
	if rw := emailTokenDo(t, h, "u2@t.test", "POST", "/api/v1/invitations/ghost/accept"); rw.Code != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}

	// An expired invitation -> 410 Gone.
	_ = invites.PutInvitation(t.Context(), auth.Invitation{
		Token: "expired", Email: "u3@t.test", Tenant: "acme", Workspace: "main",
		Roles: []core.Role{core.TeamRoleEditor()}, ExpiresAt: time.Now().Add(-time.Hour),
	})
	if rw := emailTokenDo(t, h, "u3@t.test", "POST", "/api/v1/invitations/expired/accept"); rw.Code != http.StatusGone {
		t.Fatalf("expired = %d, want 410; body=%s", rw.Code, rw.Body.String())
	}

	// A pending invitation accepted by the WRONG email -> 403.
	_ = invites.PutInvitation(t.Context(), auth.Invitation{
		Token: "pending", Email: "right@t.test", Tenant: "acme", Workspace: "main",
		Roles: []core.Role{core.TeamRoleEditor()}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if rw := emailTokenDo(t, h, "wrong@t.test", "POST", "/api/v1/invitations/pending/accept"); rw.Code != http.StatusForbidden {
		t.Fatalf("wrong email = %d, want 403; body=%s", rw.Code, rw.Body.String())
	}

	// Happy path: the right email accepts -> 200 and a membership is created.
	rw := emailTokenDo(t, h, "right@t.test", "POST", "/api/v1/invitations/pending/accept")
	if rw.Code != http.StatusOK {
		t.Fatalf("accept = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if _, err := h.gw.Memberships.GetMembership(t.Context(), "right@t.test", "acme"); err != nil {
		t.Fatalf("membership not created: %v", err)
	}
}

// TestAcceptInvitation_VerifiesEmail confirms accepting an invite stamps the
// accepting account as email-verified: the invite was issued by a verified
// admin who vouched for the address, so the new member skips the redundant
// verification nag.
func TestAcceptInvitation_VerifiesEmail(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	invites, _ := auth.OpenJSONInvitationStore("")
	h.gw.Invitations = invites
	h.gw.Memberships = newFakeMembershipStore()
	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	h.gw.Users = users

	// An unverified account that matches the invited address.
	if err := users.PutUser(t.Context(), auth.User{
		Email: "newbie@t.test", Subject: "newbie@t.test", Tenant: "acme",
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if u, _ := users.GetByEmail(t.Context(), "newbie@t.test"); u.EmailVerified() {
		t.Fatal("precondition: user should start unverified")
	}

	_ = invites.PutInvitation(t.Context(), auth.Invitation{
		Token: "welcome", Email: "newbie@t.test", Tenant: "acme", Workspace: "main",
		Roles: []core.Role{core.TeamRoleEditor()}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if rw := emailTokenDo(t, h, "newbie@t.test", "POST", "/api/v1/invitations/welcome/accept"); rw.Code != http.StatusOK {
		t.Fatalf("accept = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	u, err := users.GetByEmail(t.Context(), "newbie@t.test")
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if !u.EmailVerified() {
		t.Fatalf("accepting an invite should verify the email, got %+v", u)
	}
}

// TestPlatformVerifyUser covers the admin support hatch: a platform admin can
// mark an account verified directly, and it's idempotent.
func TestPlatformVerifyUser(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	h.gw.Users = users
	if err := users.PutUser(t.Context(), auth.User{
		Email: "support@t.test", Subject: "support@t.test", Tenant: "acme",
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	do := func() *httptest.ResponseRecorder {
		return h.platformDo(t, "POST", "/api/v1/admin/platform/users/support@t.test/verify", nil)
	}
	if rw := do(); rw.Code != http.StatusOK {
		t.Fatalf("verify = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	u, _ := users.GetByEmail(t.Context(), "support@t.test")
	if !u.EmailVerified() {
		t.Fatalf("verify should set VerifiedAt, got %+v", u)
	}
	// Idempotent: verifying again still 200s.
	if rw := do(); rw.Code != http.StatusOK {
		t.Fatalf("re-verify = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
}
