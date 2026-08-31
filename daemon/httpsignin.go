// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Sign-in, sign-out and identity: exchanging credentials for a session, the
// role elevations a session picks up (platform admin, support agent), and
// the whoami payload the app reads its user and org memberships from.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// signIn validates an email+password pair, mints a session, and sets
// the session cookie. The session token is also returned in the body so
// non-browser clients can hand it back via Authorization: Bearer.
func (h *HTTPGateway) signIn(rw http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "password sign-in not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}](rw, r)
	if !ok {
		return
	}
	user, err := auth.VerifyPassword(r.Context(), h.Users, body.Email, body.Password)
	if err != nil {
		// Tenant is left empty: resolving it would reveal whether the
		// email maps to an account, which the uniform error above
		// deliberately hides.
		h.auditAuth(r.Context(), r, "", strings.ToLower(strings.TrimSpace(body.Email)), "auth.signin_failed", "method=password")
		writeJSONError(rw, http.StatusUnauthorized, "invalid email or password")
		return
	}
	// Locked out? A suspended user — or a member of a suspended org — has a
	// valid password but no access. Refuse at sign-in with a clear reason
	// rather than issuing a session (or a TOTP challenge) the auth
	// ModerationGate would just reject on the next request.
	if msg, locked := h.signInLockout(r.Context(), user); locked {
		h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.signin_suspended", "method=password")
		writeJSONError(rw, http.StatusForbidden, msg)
		return
	}
	// Second factor: if this user has TOTP enabled (and the install has
	// 2FA configured), the password alone is not enough. Mint a
	// short-lived challenge and return it instead of a session; the
	// client posts it back to /auth/totp with a code to finish. We fail
	// closed — an enrolled user can't downgrade to password-only just
	// because the challenge store is missing.
	if user.TOTPEnabled && h.totpConfigured() {
		challenge, cerr := auth.IssueTOTPChallenge(r.Context(), h.TOTPChallenges, user.Email)
		if cerr != nil {
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue challenge: %v", cerr))
			return
		}
		h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.mfa_challenge", "method=password")
		writeJSON(rw, http.StatusOK, map[string]any{
			"totp_required": true,
			"challenge":     challenge,
		})
		return
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevateSessionRoles(r.Context(), user), h.sessionTTL())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signin", "method=password")
	h.setSessionCookie(rw, r, token, sess.ExpiresAt)
	writeJSON(rw, http.StatusOK, map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"tenant":     sess.Tenant,
		"workspace":  sess.Workspace,
		"expires_at": sess.ExpiresAt,
	})
}

// elevatePlatformAdmin grants the platform:admin role to a user whose
// email is in the PlatformAdmins allowlist. Called at every session-issue
// site (sign-in, signup, SSO) so the allowlist is the single source of
// truth — roles are baked into the session at issue time, so an existing
// session must re-authenticate to pick up an allowlist change. No-op when
// the email isn't listed or the role is already present.
func (h *HTTPGateway) elevatePlatformAdmin(ctx context.Context, u auth.User) auth.User {
	env := h.isPlatformAdminEmail(u.Email)
	if !env && !h.isPlatformAdminGranted(u.Email) {
		return u
	}
	for _, r := range u.Roles {
		if r.Has(core.PermPlatformAdmin) {
			return u
		}
	}
	// Copy before appending: u.Roles may alias a slice held by the user
	// store, and we must not mutate that shared backing array.
	u.Roles = append(append([]core.Role(nil), u.Roles...), core.PlatformAdminRole())
	// Record the escalation on first apply (per email, per process): a
	// platform-admin grant is a privileged-access event worth a durable audit
	// record (ISO 27001 A.5.16/A.8.2). Emitting only once avoids a per-sign-in
	// flood, since elevation runs at every session issue.
	source := "runtime_grant"
	if env {
		source = "DAZYFLOW_PLATFORM_ADMINS"
	}
	key := strings.ToLower(strings.TrimSpace(u.Email))
	if _, seen := h.platformAdminGranted.LoadOrStore(key, struct{}{}); !seen {
		h.audit(ctx, core.Principal{Tenant: u.Tenant, Subject: u.Email},
			"platform_admin.granted", u.Email, "source="+source)
	}
	return u
}

// isPlatformAdminGranted reports whether email holds a runtime platform-admin
// grant (the mutable layer). Cheap — reads the store's cached snapshot. Nil
// store (not wired) means no runtime grants exist.
func (h *HTTPGateway) isPlatformAdminGranted(email string) bool {
	return h.PlatformAdminGrants != nil && h.PlatformAdminGrants.Granted(email)
}

// elevateSessionRoles applies every session-issue role elevation in one place,
// so the ~5 issue sites (sign-in, signup, SSO, TOTP) call a single chokepoint.
func (h *HTTPGateway) elevateSessionRoles(ctx context.Context, u auth.User) auth.User {
	return h.elevateSupportAgent(ctx, h.elevatePlatformAdmin(ctx, u))
}

// elevateSupportAgent stamps core.SupportAgentRole onto a session whose email
// holds a runtime support-agent grant (there is no env-allowlist layer for
// support). Mirrors elevatePlatformAdmin: baked in at issue time, so a grant
// takes effect on the next session issue and a revoke once live sessions drop.
// No-op when unset or already present. The role itself grants no ambient
// access — it only unlocks requesting an AccessGrant and the support-view
// capability (AuthorizeGraphSupportView).
func (h *HTTPGateway) elevateSupportAgent(ctx context.Context, u auth.User) auth.User {
	if h.SupportAgents == nil || !h.SupportAgents.Granted(u.Email) {
		return u
	}
	for _, r := range u.Roles {
		if r.Has(core.PermSupportAgent) {
			return u
		}
	}
	u.Roles = append(append([]core.Role(nil), u.Roles...), core.SupportAgentRole())
	// Record the escalation once per email per process (privileged-access event).
	key := strings.ToLower(strings.TrimSpace(u.Email))
	if _, seen := h.supportAgentGranted.LoadOrStore(key, struct{}{}); !seen {
		h.audit(ctx, core.Principal{Tenant: u.Tenant, Subject: u.Email},
			"support_agent.granted", u.Email, "source=runtime_grant")
	}
	return u
}

// isPlatformAdmin reports whether email is a platform admin by EITHER layer —
// the immutable env allowlist or a runtime grant. Used for display/effective
// status; the env-only isPlatformAdminEmail still guards immutability (you
// can't revoke an env admin).
func (h *HTTPGateway) isPlatformAdmin(email string) bool {
	return h.isPlatformAdminEmail(email) || h.isPlatformAdminGranted(email)
}

// isPlatformAdminEmail reports whether email is in the allowlist. The
// stored entries are already lowercased + trimmed at wiring time; we
// normalize the candidate the same way so the comparison is exact.
func (h *HTTPGateway) isPlatformAdminEmail(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, a := range h.PlatformAdmins {
		if a == email {
			return true
		}
	}
	return false
}

// adminBootstrapAvailable reports whether at least one platform-admin
// email in the allowlist has not yet claimed an account. It's the
// signal the sign-up page uses to keep itself reachable on a
// signup-disabled deployment: the backend already lets a listed email
// through signUp (see httpsignup.go), but the page would otherwise
// bounce to /signin because EnableSignup is false, leaving the
// bootstrap hatch with no door. The check is self-limiting in lockstep
// with that hatch — once every listed admin has signed up, GetByEmail
// finds them all and this returns false, so the form disappears again
// and a locked-down instance doesn't expose public signup forever.
//
// Unauthenticated callers reach this via getPublicAuthConfig; it leaks
// only a single boolean, never which emails are listed. The allowlist
// is tiny (typically 1-3), so the per-email lookups are cheap.
func (h *HTTPGateway) adminBootstrapAvailable(ctx context.Context) bool {
	if h.Users == nil || len(h.PlatformAdmins) == 0 {
		return false
	}
	for _, email := range h.PlatformAdmins {
		// Mirror signUp's existence test: a non-nil error or an empty
		// email both mean "not claimed yet". Any unclaimed admin keeps
		// the bootstrap door open.
		if u, err := h.Users.GetByEmail(ctx, email); err != nil || u.Email == "" {
			return true
		}
	}
	return false
}

// signOut deletes the server-side session and clears the cookie. It
// silently no-ops when no session is attached so the browser can hit
// this on logout without inspecting state first.
func (h *HTTPGateway) signOut(rw http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "sessions not configured")
		return
	}
	if token := credentialFromRequest(r); strings.HasPrefix(token, auth.SessionTokenPrefix) {
		key := auth.SessionLookupKey(token)
		// Resolve before deleting so the audit event carries the identity
		// that signed out. Best-effort — an already-gone session just
		// skips the record.
		if sess, err := h.Sessions.GetSession(r.Context(), key); err == nil {
			h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signout", "")
		}
		_ = h.Sessions.DeleteSession(r.Context(), key)
	}
	h.clearSessionCookie(rw, r)
	rw.WriteHeader(http.StatusNoContent)
}

// whoami returns the authenticated principal's identity AND the flat
// set of permissions any of their roles grant. The UI uses this for
// role gating (whether to show the Admin link, the Edit button, etc.)
// without re-implementing role unrolling client-side.
func (h *HTTPGateway) whoami(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	permSet := map[core.Permission]struct{}{}
	for _, role := range p.Roles {
		for _, perm := range role.Permissions {
			permSet[perm] = struct{}{}
		}
	}
	perms := make([]core.Permission, 0, len(permSet))
	for perm := range permSet {
		perms = append(perms, perm)
	}
	// Memberships lets the UI surface an org switcher even for non-
	// platform-admin users. The list always includes the principal's
	// current org (Home=true) so a fresh signup with no extra
	// memberships still sees a single entry and the switcher gracefully
	// hides itself.
	memberships := h.collectMemberships(r.Context(), p)
	emailVerified, verificationPending := h.verificationStatus(r, p)
	writeJSON(rw, http.StatusOK, map[string]any{
		"subject":     p.Subject,
		"tenant":      p.Tenant,
		"workspace":   p.Workspace,
		"roles":       p.Roles,
		"permissions": perms,
		"memberships": memberships,
		// email_verified / verification_pending drive the "confirm your
		// email" banner. pending is false on deployments without a
		// mailer (nothing to verify against) and for API-key callers.
		"email_verified":       emailVerified,
		"verification_pending": verificationPending,
		// public_base_url lets the UI build externally-correct webhook /
		// hosted-form URLs instead of guessing the host. Empty when the
		// operator hasn't set --public-base-url; the UI falls back to a
		// localhost hint in that case.
		"public_base_url": h.svc.PublicBaseURL,
		// support_contact surfaces an operator-set email/URL on UI
		// surfaces that depend on server-side setup the end user can't
		// fix themselves (e.g. OAuth/secret-store not configured on the
		// Connections page). Empty = the UI shows a generic "contact
		// your administrator" message with no link.
		"support_contact": h.svc.SupportContact,
		// support_tickets_enabled tells the UI whether the native ticket
		// surface is wired (DAZYFLOW_SUPPORT_ENABLED). The UI hides "Report
		// a problem" / the Support page when off, rather than letting the
		// user hit a 501.
		"support_tickets_enabled": h.ticketsEnabled(),
	})
}

// orgMembershipDTO is the wire shape whoami emits per membership. The
// home org always appears with home=true; the others come from the
// MembershipStore. DisplayName is the org's human-facing name (from
// OrgProfile) — empty when the org has no profile yet, in which case
// the UI falls back to the raw Tenant ID.
type orgMembershipDTO struct {
	Tenant      string      `json:"tenant"`
	DisplayName string      `json:"display_name,omitempty"`
	Icon        string      `json:"icon,omitempty"`
	Workspace   string      `json:"workspace"`
	Roles       []core.Role `json:"roles"`
	Home        bool        `json:"home"`
}

func (h *HTTPGateway) collectMemberships(ctx context.Context, p core.Principal) []orgMembershipDTO {
	// The home entry is the user's OWN tenant (from the user record), not the
	// session's current tenant — otherwise switching into another org would
	// make the home org follow p.Tenant and drop out of the list (it isn't a
	// membership row). Fall back to p.Tenant for API-key principals, which
	// have no user record and are bound to one tenant.
	homeTenant, homeWorkspace, homeRoles := p.Tenant, p.Workspace, p.Roles
	if h.Users != nil && strings.Contains(p.Subject, "@") {
		if u, err := h.Users.GetByEmail(ctx, p.Subject); err == nil {
			homeTenant, homeWorkspace, homeRoles = u.Tenant, u.Workspace, u.Roles
		}
	}
	out := []orgMembershipDTO{{
		Tenant:    homeTenant,
		Workspace: homeWorkspace,
		Roles:     homeRoles,
		Home:      true,
	}}
	if h.Memberships != nil && p.Subject != "" && strings.Contains(p.Subject, "@") {
		// Only password-auth subjects (email-shaped) have Memberships;
		// API-key principals are bound to one tenant by their key. A
		// silent skip on a non-email subject avoids accidentally exposing
		// memberships keyed by a coincidental UUID match.
		rows, err := h.Memberships.ListByEmail(ctx, p.Subject)
		if err == nil {
			for _, m := range rows {
				if m.Tenant == homeTenant {
					// Already in `out` as the home entry — skip the duplicate.
					continue
				}
				out = append(out, orgMembershipDTO{
					Tenant:    m.Tenant,
					Workspace: m.Workspace,
					Roles:     m.Roles,
					Home:      false,
				})
			}
		}
	}
	// Bulk-resolve display names so the switcher can render pretty
	// labels without an extra round-trip per membership. A missing
	// profile leaves DisplayName empty; the UI falls back to Tenant.
	if h.Profiles != nil && len(out) > 0 {
		tenants := make([]string, 0, len(out))
		for _, m := range out {
			tenants = append(tenants, m.Tenant)
		}
		if profiles, err := h.Profiles.ListOrgProfiles(ctx, tenants); err == nil {
			for i := range out {
				if pr, ok := profiles[out[i].Tenant]; ok {
					out[i].DisplayName = pr.DisplayName
					out[i].Icon = pr.Icon
				}
			}
		}
	}
	return out
}
