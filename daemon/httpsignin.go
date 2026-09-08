// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

type authAPI struct {
	auditor
	adminCheck
	urlBuilder
	langPicker
	sessionCookies
	seatQuota
	svc            *Service
	logger         *log.Logger
	Users          auth.UserStore
	Sessions       auth.SessionStore
	Memberships    auth.MembershipStore
	Invitations    auth.InvitationStore
	Profiles       auth.OrgProfileStore
	Blocklist      auth.BlocklistStore
	OrgAuth        auth.OrgAuthStore
	SupportAgents  support.AgentStore
	TOTPChallenges auth.TOTPChallengeStore
	Ephemeral      auth.EphemeralStore
	TOTPKey        []byte
	WildcardDomain string
	EnableSignup   bool
	PlatformAdmins []string

	platformAdminGranted *sync.Map
	supportAgentGranted  *sync.Map

	signInLockout  func(ctx context.Context, u auth.User) (string, bool)
	ticketsEnabled func() bool
}

func (h *HTTPGateway) authAPI() *authAPI {
	return &authAPI{auditor: h.auditor(), adminCheck: h.admins(), urlBuilder: h.urls(), langPicker: h.lang(), sessionCookies: h.cookies(), seatQuota: h.seats(), svc: h.svc, logger: h.logger, Users: h.Users, Sessions: h.Sessions, Memberships: h.Memberships, Invitations: h.Invitations, Profiles: h.Profiles, Blocklist: h.Blocklist, OrgAuth: h.OrgAuth, SupportAgents: h.SupportAgents, TOTPChallenges: h.TOTPChallenges, Ephemeral: h.Ephemeral, TOTPKey: h.TOTPKey, WildcardDomain: h.WildcardDomain, EnableSignup: h.EnableSignup, PlatformAdmins: h.PlatformAdmins, platformAdminGranted: &h.platformAdminGranted, supportAgentGranted: &h.supportAgentGranted, signInLockout: h.platformAdminAPI().signInLockout, ticketsEnabled: h.supportAPI().ticketsEnabled}
}

func (h *authAPI) signIn(rw http.ResponseWriter, r *http.Request) {
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
		// Resolving it would reveal whether the address belongs to a real org.
		h.auditAuth(r.Context(), r, "", strings.ToLower(strings.TrimSpace(body.Email)), "auth.signin_failed", "method=password")
		writeJSONError(rw, http.StatusUnauthorized, "invalid email or password")
		return
	}
	// A suspended user, or a member of a suspended org, must not get a session.
	if msg, locked := h.signInLockout(r.Context(), user); locked {
		h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.signin_suspended", "method=password")
		writeJSONError(rw, http.StatusForbidden, msg)
		return
	}
	// A verified password is NOT sufficient once TOTP is enrolled.
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

// At session-issue time: a grant takes effect on the target's NEXT sign-in.
func (h *authAPI) elevatePlatformAdmin(ctx context.Context, u auth.User) auth.User {
	env := h.isPlatformAdminEmail(u.Email)
	if !env && !h.isPlatformAdminGranted(u.Email) {
		return u
	}
	for _, r := range u.Roles {
		if r.Has(core.PermPlatformAdmin) {
			return u
		}
	}
	u.Roles = append(append([]core.Role(nil), u.Roles...), core.PlatformAdminRole())
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

func (h *authAPI) elevateSessionRoles(ctx context.Context, u auth.User) auth.User {
	return h.elevateSupportAgent(ctx, h.elevatePlatformAdmin(ctx, u))
}

func (h *authAPI) elevateSupportAgent(ctx context.Context, u auth.User) auth.User {
	if h.SupportAgents == nil || !h.SupportAgents.Granted(u.Email) {
		return u
	}
	for _, r := range u.Roles {
		if r.Has(core.PermSupportAgent) {
			return u
		}
	}
	u.Roles = append(append([]core.Role(nil), u.Roles...), core.SupportAgentRole())
	key := strings.ToLower(strings.TrimSpace(u.Email))
	if _, seen := h.supportAgentGranted.LoadOrStore(key, struct{}{}); !seen {
		h.audit(ctx, core.Principal{Tenant: u.Tenant, Subject: u.Email},
			"support_agent.granted", u.Email, "source=runtime_grant")
	}
	return u
}

// Whether a deployment can still bootstrap its first platform admin — once one
// exists this must close, or anyone could claim the role.
func (h *authAPI) adminBootstrapAvailable(ctx context.Context) bool {
	if h.Users == nil || len(h.PlatformAdmins) == 0 {
		return false
	}
	for _, email := range h.PlatformAdmins {
		if u, err := h.Users.GetByEmail(ctx, email); err != nil || u.Email == "" {
			return true
		}
	}
	return false
}

// Deletes server-side first: clearing the cookie alone leaves the session live.
func (h *authAPI) signOut(rw http.ResponseWriter, r *http.Request) {
	if h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "sessions not configured")
		return
	}
	if token := credentialFromRequest(r); strings.HasPrefix(token, auth.SessionTokenPrefix) {
		key := auth.SessionLookupKey(token)
		// Resolve BEFORE deleting, or the audit event has no identity to record.
		if sess, err := h.Sessions.GetSession(r.Context(), key); err == nil {
			h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signout", "")
		}
		_ = h.Sessions.DeleteSession(r.Context(), key)
	}
	h.clearSessionCookie(rw, r)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *authAPI) whoami(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	memberships := h.collectMemberships(r.Context(), p)
	emailVerified, verificationPending := h.verificationStatus(r, p)
	writeJSON(rw, http.StatusOK, meResponse{
		Subject:               p.Subject,
		Tenant:                p.Tenant,
		Workspace:             p.Workspace,
		Roles:                 p.Roles,
		Permissions:           perms,
		Memberships:           memberships,
		EmailVerified:         emailVerified,
		VerificationPending:   verificationPending,
		PublicBaseURL:         h.svc.PublicBaseURL,
		SupportContact:        h.svc.SupportContact,
		SupportTicketsEnabled: h.ticketsEnabled(),
	})
}

// The wire shape of /me and /whoami.
type meResponse struct {
	EmailVerified         bool               `json:"email_verified"`
	Memberships           []orgMembershipDTO `json:"memberships"`
	Permissions           []core.Permission  `json:"permissions"`
	PublicBaseURL         string             `json:"public_base_url"`
	Roles                 []core.Role        `json:"roles"`
	Subject               string             `json:"subject"`
	SupportContact        string             `json:"support_contact"`
	SupportTicketsEnabled bool               `json:"support_tickets_enabled"`
	Tenant                string             `json:"tenant"`
	VerificationPending   bool               `json:"verification_pending"`
	Workspace             string             `json:"workspace"`
}

// Per membership, as whoami emits it.
type orgMembershipDTO struct {
	Tenant      string      `json:"tenant"`
	DisplayName string      `json:"display_name,omitempty"`
	Icon        string      `json:"icon,omitempty"`
	Workspace   string      `json:"workspace"`
	Roles       []core.Role `json:"roles"`
	Home        bool        `json:"home"`
}

func (h *authAPI) collectMemberships(ctx context.Context, p core.Principal) []orgMembershipDTO {
	// The user's OWN tenant, not whichever org the session is currently scoped to.
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
		// Only password-auth subjects have memberships; an API key has none.
		rows, err := h.Memberships.ListByEmail(ctx, p.Subject)
		if err == nil {
			for _, m := range rows {
				if m.Tenant == homeTenant {
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
