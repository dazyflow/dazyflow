// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// signupInviteHarness: a signup-DISABLED deployment with users, sessions,
// and an invitations store wired — the setting a platform owner uses to
// invite people one at a time. No mailer, so createSignupInvite reports
// email_sent=false but still returns the link.
func signupInviteHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newGatewayHarness(t)
	users, err := auth.OpenJSONUserStore("")
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	invites, err := auth.OpenJSONInvitationStore("")
	if err != nil {
		t.Fatalf("open invitation store: %v", err)
	}
	h.gw.Users = users
	h.gw.Invitations = invites
	h.gw.Sessions = auth.NewMemSessionStore()
	h.gw.EnableSignup = false // the whole point: signup is closed
	h.svc.PublicBaseURL = "https://app.example"
	return h
}

// createSignupInvite returns the minted token for email.
func createSignupInvite(t *testing.T, h *gatewayHarness, email string) string {
	t.Helper()
	rw := h.platformDo(t, "POST", "/api/v1/admin/signup-invites", map[string]string{"email": email})
	if rw.Code != http.StatusCreated {
		t.Fatalf("create signup-invite: %d %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		Email     string `json:"email"`
		SignupURL string `json:"signup_url"`
		EmailSent bool   `json:"email_sent"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create resp: %v", err)
	}
	if resp.Token == "" || resp.Email != email {
		t.Fatalf("unexpected create resp: %+v", resp)
	}
	return resp.Token
}

// TestSignupInvite_GateOpensForInvitedEmail is the headline behaviour:
// signup is disabled, but a valid invite token lets exactly that email
// through, and the token can't be reused or bent to another address.
func TestSignupInvite_GateOpensForInvitedEmail(t *testing.T) {
	h := signupInviteHarness(t)

	// Baseline: signup is closed without an invite.
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "new@example.com", "password": "TestPassw0rd!23",
	}); rw.Code != http.StatusNotImplemented {
		t.Fatalf("signup without invite: want 501, got %d %s", rw.Code, rw.Body.String())
	}

	token := createSignupInvite(t, h, "new@example.com")

	// Wrong email + right token is still closed — the token binds to the
	// invited address, not just "any signup".
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "intruder@example.com", "password": "TestPassw0rd!23",
		"signup_invite": token,
	}); rw.Code != http.StatusNotImplemented {
		t.Fatalf("signup with mismatched email: want 501, got %d %s", rw.Code, rw.Body.String())
	}

	// Right email + right token: through the gate.
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "new@example.com", "password": "TestPassw0rd!23",
		"signup_invite": token,
	}); rw.Code != http.StatusCreated {
		t.Fatalf("invited signup: want 201, got %d %s", rw.Code, rw.Body.String())
	}

	// Single-use: consuming the invite marks it accepted, so it's no
	// longer pending and the gate closes again — a re-clicked link gets
	// the same 501 as any uninvited signup. (Gate-first ordering is
	// deliberate: it never leaks account existence on a closed
	// deployment.)
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "new@example.com", "password": "TestPassw0rd!23",
		"signup_invite": token,
	}); rw.Code != http.StatusNotImplemented {
		t.Fatalf("re-used invite: want 501, got %d %s", rw.Code, rw.Body.String())
	}

	// The invite shows as accepted in the platform listing.
	rw := h.platformDo(t, "GET", "/api/v1/admin/signup-invites", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rw.Code, rw.Body.String())
	}
	var list struct {
		Invites []struct {
			Email      string `json:"email"`
			Pending    bool   `json:"pending"`
			AcceptedAt string `json:"accepted_at"`
		} `json:"invites"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Invites) != 1 || list.Invites[0].Pending || list.Invites[0].AcceptedAt == "" {
		t.Fatalf("expected one accepted invite, got %+v", list.Invites)
	}
}

// TestSignupInvite_RevokedTokenStaysClosed: a revoked invite no longer
// opens the gate.
func TestSignupInvite_RevokedTokenStaysClosed(t *testing.T) {
	h := signupInviteHarness(t)
	token := createSignupInvite(t, h, "later@example.com")

	if rw := h.platformDo(t, "DELETE", "/api/v1/admin/signup-invites/"+token, nil); rw.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rw.Code, rw.Body.String())
	}
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "later@example.com", "password": "TestPassw0rd!23",
		"signup_invite": token,
	}); rw.Code != http.StatusNotImplemented {
		t.Fatalf("revoked invite signup: want 501, got %d %s", rw.Code, rw.Body.String())
	}
}

// TestSignupInvite_NotAnOrgInvite: a signup-invite token must not be
// usable through the org-invite surfaces (they share a store).
func TestSignupInvite_NotAnOrgInvite(t *testing.T) {
	h := signupInviteHarness(t)
	token := createSignupInvite(t, h, "solo@example.com")

	// The unauthenticated org-invite detail endpoint hides it.
	if rw := h.do(t, "GET", "/api/v1/invitations/"+token, nil); rw.Code != http.StatusNotFound {
		t.Fatalf("viewInvitation on signup-invite: want 404, got %d %s", rw.Code, rw.Body.String())
	}
}

// TestSignupInvite_ExpiredStaysClosed: an expired signup-invite no longer
// opens the gate (IsPending is false past expiry).
func TestSignupInvite_ExpiredStaysClosed(t *testing.T) {
	h := signupInviteHarness(t)
	now := time.Now().UTC()
	if err := h.gw.Invitations.PutInvitation(t.Context(), auth.Invitation{
		Token:     "inv_expiredtoken000000000000000",
		Email:     "old@example.com",
		Tenant:    auth.SignupInviteTenant,
		CreatedAt: now.Add(-48 * time.Hour),
		ExpiresAt: now.Add(-time.Hour), // already expired
	}); err != nil {
		t.Fatalf("seed expired invite: %v", err)
	}
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "old@example.com", "password": "TestPassw0rd!23",
		"signup_invite": "inv_expiredtoken000000000000000",
	}); rw.Code != http.StatusNotImplemented {
		t.Fatalf("expired signup-invite: want 501, got %d %s", rw.Code, rw.Body.String())
	}
}

// TestSignupInvite_RequiresPlatformAdmin: a mere org admin can't mint
// platform signup-invites.
func TestSignupInvite_RequiresPlatformAdmin(t *testing.T) {
	h := signupInviteHarness(t)
	if rw := h.adminDo(t, "POST", "/api/v1/admin/signup-invites", map[string]string{
		"email": "x@example.com",
	}); rw.Code != http.StatusForbidden {
		t.Fatalf("org-admin create: want 403, got %d %s", rw.Code, rw.Body.String())
	}
}

// TestSignupInvite_RejectsExistingAccount: inviting an email that already
// has an account is a 409, not a dangling invite.
func TestSignupInvite_RejectsExistingAccount(t *testing.T) {
	h := signupInviteHarness(t)
	// Mint an account first via an invite.
	token := createSignupInvite(t, h, "taken@example.com")
	if rw := h.do(t, "POST", "/api/v1/auth/signup", map[string]string{
		"email": "taken@example.com", "password": "TestPassw0rd!23",
		"signup_invite": token,
	}); rw.Code != http.StatusCreated {
		t.Fatalf("seed account: %d %s", rw.Code, rw.Body.String())
	}
	if rw := h.platformDo(t, "POST", "/api/v1/admin/signup-invites", map[string]string{
		"email": "taken@example.com",
	}); rw.Code != http.StatusConflict {
		t.Fatalf("invite existing account: want 409, got %d %s", rw.Code, rw.Body.String())
	}
}
