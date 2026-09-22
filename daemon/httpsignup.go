// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// Self-serve signup:
//
//	POST /api/v1/auth/signup    {email, password} → {token, subject, tenant, …}
//
// A new user gets a random tenant ID (usr_<hex>, so the email is not leaked into
// URLs or logs), a default workspace named "main", the `editor` and
// `tenant_owner` roles, and an immediately-issued session matching the signin
// endpoint's cookie + token shape. Anti-abuse is the per-IP auth rate limit plus
// email verification; there is no captcha or plan selection.
//
// With `EnableSignup` false the endpoint returns 501, except for emails in
// DAZYFLOW_PLATFORM_ADMINS, so a fresh instance can bootstrap its first
// super-admin without opening signup to the world.

type signupRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	SignupInvite string `json:"signup_invite,omitempty"`
}

func (h *authAPI) signUp(rw http.ResponseWriter, r *http.Request) {
	if h.Users == nil || h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "users/sessions not configured")
		return
	}
	var body signupRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "we couldn't read the sign-up details — please try again")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	invited := h.validSignupInvite(r.Context(), email, body.SignupInvite)
	if !h.EnableSignup && !h.isPlatformAdminEmail(email) && !invited {
		writeJSONError(rw, http.StatusNotImplemented, "self-serve signup is not enabled on this deployment")
		return
	}
	if err := validSignupEmail(email); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := validSignupPassword(body.Password); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}

	// A store error here is NOT evidence that the email is free. Treating it
	// that way sent signup on to PutUser, which then raced to a duplicate-key
	// 500 — an incoherent response to what is really "the database is
	// briefly unavailable". Only a genuine not-found continues.
	existing, lookupErr := h.Users.GetByEmail(r.Context(), email)
	if lookupErr != nil && !errors.Is(lookupErr, auth.ErrUnknownUser) {
		writeJSONError(rw, http.StatusServiceUnavailable,
			"could not check that email right now — please try again in a moment")
		return
	}
	if lookupErr == nil && existing.Email != "" {
		// "email already in use" is a real fact a malicious enumeration
		// attempt would mine for. Signup stays instant-try even with
		// verification active (the session is issued before the link is
		// clicked), so hiding the conflict would just defer the truth to
		// the sign-in attempt; we tell it and rely on the per-IP auth
		// rate limit to slow enumeration.
		writeJSONError(rw, http.StatusConflict, "an account with that email already exists")
		return
	}

	// Ban enforcement: a platform admin may have blocklisted this email
	// (or its whole domain) so a banned operator can't just re-register a
	// fresh account. Checked after the cheap validations. The message is
	// deliberately generic — it doesn't confirm a ban, only that this
	// address is unusable, which is all a legitimate user needs.
	if h.Blocklist != nil {
		if blocked, _, err := h.Blocklist.IsBlocked(r.Context(), email); err == nil && blocked {
			writeJSONError(rw, http.StatusForbidden, "this email address can't be used to sign up")
			return
		}
	}

	tenant, err := h.mintFreshTenantID(r.Context(), mintTenantID)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("mint tenant: %v", err))
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	user := auth.User{
		Email:        email,
		PasswordHash: hash,
		Subject:      email,
		Tenant:       tenant,
		Workspace:    "main",
		Roles:        defaultSignupRoles(),
		CreatedAt:    time.Now().UTC(),
	}
	if err := h.Users.PutUser(r.Context(), user); err != nil {
		// JSONUserStore.PutUser silently overwrites duplicates — the
		// GetByEmail above is our primary defense. Any error here is
		// genuinely unexpected (disk write failure, e.g.).
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("create user: %v", err))
		return
	}

	// Burn the signup-invite now that the account exists. Best-effort:
	// the email-uniqueness check above already makes the token single-use
	// (a second signup with it hits the 409), so a failure to stamp
	// accepted_at only affects the operator's pending-invites view, not
	// security. validSignupInvite already confirmed the token is pending
	// and addressed to this email.
	if invited {
		if err := h.Invitations.MarkAccepted(r.Context(), body.SignupInvite, time.Now().UTC()); err != nil {
			h.logger.Printf("signup-invite %s: mark accepted: %v", body.SignupInvite, err)
		}
	}

	if h.Profiles != nil {
		if name := auth.DefaultOrgDisplayName(email); name != "" {
			_ = h.Profiles.PutOrgProfile(r.Context(), auth.OrgProfile{
				Tenant:      tenant,
				DisplayName: name,
				UpdatedAt:   time.Now().UTC(),
			})
		}
	}

	// Verification email (active only with a mailer + public base URL):
	// best-effort, never blocks the signup. The UI shows the pending
	// banner via whoami until the link is clicked.
	verificationSent := h.sendVerificationEmail(r, user)

	h.sendWelcomeEmail(r, user)

	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevateSessionRoles(r.Context(), user), h.sessionTTL())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signup", "method=password")
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// requestIsHTTPS, NOT r.TLS != nil: in the shipped topology Caddy
		// terminates TLS and proxies plaintext to dzd:8080, so r.TLS is nil
		// on every production request and a bare r.TLS check would drop the
		// Secure flag exactly where it matters most — the freshly minted
		// session a new user carries away from signup. Matches the sign-in,
		// SSO and TOTP legs (httpgateway.go, httptotp.go).
		Secure: h.requestIsHTTPS(r),
	})
	writeJSON(rw, http.StatusCreated, map[string]any{
		"token":                   token,
		"subject":                 sess.Subject,
		"tenant":                  sess.Tenant,
		"workspace":               sess.Workspace,
		"expires_at":              sess.ExpiresAt,
		"verification_email_sent": verificationSent,
	})
}

// validSignupEmail does the loose check the IETF actually
// recommends — "looks like an address," not "passes RFC 5322". A
// real-world bounce is the verification step's job.
func validSignupEmail(email string) error {
	if email == "" {
		return errors.New("email is required")
	}
	if len(email) > 254 {
		return errors.New("email is too long")
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return errors.New("email must look like name@domain")
	}
	domain := email[at+1:]
	if !strings.Contains(domain, ".") {
		return errors.New("email domain must contain a dot")
	}
	for _, r := range email {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return errors.New("email contains invalid characters")
		}
	}
	return nil
}

func validSignupPassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(password) > 256 {
		return errors.New("password is too long (max 256)")
	}
	return nil
}

// 80 random bits: a tenant id is the isolation key for everything an org owns,
// so a collision would merge two customers.
const tenantIDRandomBytes = 10

func mintTenantID() (string, error) {
	b := make([]byte, tenantIDRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "usr_" + hex.EncodeToString(b), nil
}

func mintOrgTenantID() (string, error) {
	b := make([]byte, tenantIDRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "org_" + hex.EncodeToString(b), nil
}

// mintFreshTenantID mints with mint until the id is not already in use.
func (h *authAPI) mintFreshTenantID(ctx context.Context, mint func() (string, error)) (string, error) {
	const attempts = 5
	for range attempts {
		id, err := mint()
		if err != nil {
			return "", err
		}
		if !h.tenantIDTaken(ctx, id) {
			return id, nil
		}
	}
	return "", errors.New("could not mint an unused tenant id")
}

func (h *authAPI) tenantIDTaken(ctx context.Context, tenant string) bool {
	if h.Profiles != nil {
		if _, err := h.Profiles.GetOrgProfile(ctx, tenant); err == nil {
			return true
		}
	}
	if h.Memberships != nil {
		if ms, err := h.Memberships.ListByTenant(ctx, tenant); err == nil && len(ms) > 0 {
			return true
		}
	}
	return false
}

func defaultSignupRoles() []core.Role {
	return []core.Role{
		core.TeamRoleEditor(),
		{
			Name: "tenant_owner",
			// graph:admin (publish) is the owner's; editor no longer carries it.
			Permissions: []core.Permission{core.PermOrganizationAdmin, core.PermGraphAdmin},
		},
	}
}
