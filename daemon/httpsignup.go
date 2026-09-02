// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
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
// A new user gets a random tenant ID (usr_<hex>, so the email is not leaked
// into URLs or logs), a default workspace named "main", the `editor` and
// `tenant_owner` roles, and an immediately-issued session matching the signin
// endpoint's cookie + token shape. Anti-abuse is the per-IP auth rate limit
// plus email verification (when DAZYFLOW_SMTP_URL + PUBLIC_BASE_URL are set);
// there is no captcha or plan selection.
//
// With `EnableSignup` false the endpoint returns 501, except for emails in
// DAZYFLOW_PLATFORM_ADMINS, so a fresh instance can bootstrap its first
// super-admin without opening signup to the world.

// signupRequest is the wire shape of POST /api/v1/auth/signup.
type signupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// SignupInvite is the optional platform signup-invite token (see
	// signup_invite.go). When self-serve signup is disabled, a valid,
	// pending invite for this email is the third way through the gate
	// — letting a platform owner onboard specific users one at a time
	// without opening signup to the world.
	SignupInvite string `json:"signup_invite,omitempty"`
}

func (h *HTTPGateway) signUp(rw http.ResponseWriter, r *http.Request) {
	if h.Users == nil || h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "users/sessions not configured")
		return
	}
	var body signupRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// A malformed body shouldn't happen from the real UI; keep the message
		// human in case it ever reaches a person (the web mapper also swallows
		// the raw decode error, but non-web clients see this verbatim).
		writeJSONError(rw, http.StatusBadRequest, "we couldn't read the sign-up details — please try again")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	// Signup is closed by default. Three ways through the gate: the
	// operator enabled self-serve signup; this email is in the
	// platform-admin allowlist (DAZYFLOW_PLATFORM_ADMINS); or the request
	// carries a valid, pending platform signup-invite issued for this
	// email (see signup_invite.go). The allowlist path is the bootstrap
	// hatch — it lets a fresh instance mint its first super-admin without
	// flipping EnableSignup on and back off. All three are self-limiting:
	// once the account exists the duplicate check below returns 409, so a
	// listed email or an invited email can be claimed exactly once. The
	// new account is elevated to platform:admin at IssueSession time (see
	// elevatePlatformAdmin) only for allowlisted emails — an invited user
	// is an ordinary tenant owner.
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

	tenant, err := mintTenantID()
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

	// Seed the org's display name from the email's domain so the
	// switcher and admin pages don't surface the raw usr_<hex> ID by
	// default. The owner can edit it on /admin/workspace at any time.
	// Best-effort: a failure here doesn't block sign-up because the UI
	// already falls back to the tenant ID when no profile exists.
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

	// Welcome email: best-effort, on every signup path. Distinct from
	// verification (see welcome_email.go) — one confirms the address,
	// this one greets the new account.
	h.sendWelcomeEmail(r, user)

	// Auto sign-in: issue a session immediately so the UI can land
	// the user on the welcome page without an extra round trip
	// through the sign-in form. Use the shared sessionTTL() so signup
	// matches the sign-in/SSO/TOTP legs (one source of the default).
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
		// SSO and TOTP legs (httpgateway.go, totp.go).
		Secure: h.requestIsHTTPS(r),
	})
	writeJSON(rw, http.StatusCreated, map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"tenant":     sess.Tenant,
		"workspace":  sess.Workspace,
		"expires_at": sess.ExpiresAt,
		// True when a confirmation email went out — the UI can word the
		// welcome step accordingly. False on deployments without a
		// mailer (verification inactive) or on a send failure.
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
		// Reject control chars + whitespace inside the address.
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return errors.New("email contains invalid characters")
		}
	}
	return nil
}

// validSignupPassword enforces the minimum we can defend in a
// startup-phase product. The point of length-only is to keep the
// signup form fast — complexity rules slow users down without
// meaningfully reducing brute-force risk (the bcrypt cost factor
// is what limits that).
func validSignupPassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(password) > 256 {
		return errors.New("password is too long (max 256)")
	}
	return nil
}

// mintTenantID returns "usr_" + 8 hex chars. Keeps the tenant out
// of URLs/logs as anything resembling the user's email — important
// because tenant IDs show up in webhook URLs and audit trails. The
// 8 hex chars give ~10^9 combinations; collisions are vanishingly
// unlikely for an MVP.
func mintTenantID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "usr_" + hex.EncodeToString(b), nil
}

// mintOrgTenantID returns "org_" + 8 hex chars — the id for a self-serve
// organization a user creates. The "org_" prefix (vs "usr_") keeps it out of
// the personal-tenant heuristics: a user-created org is a real shared tenant,
// not the auto-deletable personal tenant minted at signup (see
// looksPersonalTenant in gdpr.go).
func mintOrgTenantID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "org_" + hex.EncodeToString(b), nil
}

// defaultSignupRoles wires the new user with enough permissions to
// drive their own tenant — they can edit and run graphs, manage
// secrets (so OAuth works), and admin their own tenant (issue API
// keys, invite users via the team-features T3 item).
func defaultSignupRoles() []core.Role {
	return []core.Role{
		core.TeamRoleEditor(),
		{
			Name:        "tenant_owner",
			Permissions: []core.Permission{core.PermOrganizationAdmin},
		},
	}
}

// Workspace provisioning for new signups: every org has exactly one
// workspace, named "main" (set on the User above). Its backing store is
// provisioned lazily on first use by AutoFSWorkspaces.Open, so signup
// itself creates nothing here. There is deliberately no workspace
// create/list surface — workspace is not a user-facing concept.
