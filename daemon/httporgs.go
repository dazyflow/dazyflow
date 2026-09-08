// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
	"github.com/dazyflow/dazyflow/internal/datenames"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

type orgAPI struct {
	auditor
	langPicker
	seatQuota
	svc            *Service
	logger         *log.Logger
	Users          auth.UserStore
	Sessions       auth.SessionStore
	Memberships    auth.MembershipStore
	Invitations    auth.InvitationStore
	Profiles       auth.OrgProfileStore
	OrgAuth        auth.OrgAuthStore
	SupportAgents  support.AgentStore
	LogTail        *LogTail
	WildcardDomain string
	EnableSignup   bool
	auth           *authAPI
	revokeSessions func(ctx context.Context, subject string)
}

func (h *HTTPGateway) orgAPI() *orgAPI {
	return &orgAPI{auditor: h.auditor(), langPicker: h.lang(), seatQuota: h.seats(), svc: h.svc, logger: h.logger, Users: h.Users, Sessions: h.Sessions, Memberships: h.Memberships, Invitations: h.Invitations, Profiles: h.Profiles, OrgAuth: h.OrgAuth, SupportAgents: h.SupportAgents, LogTail: h.LogTail, WildcardDomain: h.WildcardDomain, EnableSignup: h.EnableSignup, auth: h.authAPI(), revokeSessions: h.platformAdminAPI().revokeSubjectSessions}
}

// Re-issues the session against another tenant the caller is a member of;
// membership is re-checked here rather than trusted from the request.
func (h *orgAPI) switchOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Sessions == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "sessions/users not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Tenant string `json:"tenant"`
	}](rw, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(body.Tenant)
	if target == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant required")
		return
	}
	if target == p.Tenant {
		writeJSON(rw, http.StatusOK, map[string]any{
			"tenant":    p.Tenant,
			"workspace": p.Workspace,
			"roles":     p.Roles,
		})
		return
	}
	user, err := h.Users.GetByEmail(r.Context(), p.Subject)
	if err != nil {
		writeJSONError(rw, http.StatusForbidden, "this credential cannot switch orgs")
		return
	}
	var (
		newWorkspace string
		newRoles     []core.Role
		found        bool
	)
	if target == user.Tenant {
		newWorkspace = user.Workspace
		newRoles = user.Roles
		found = true
	} else if h.Memberships != nil {
		m, err := h.Memberships.GetMembership(r.Context(), p.Subject, target)
		if err == nil {
			newWorkspace = m.Workspace
			newRoles = m.Roles
			found = true
		}
	}
	if !found {
		writeJSONError(rw, http.StatusForbidden, "not a member of that organization")
		return
	}
	token := credentialFromRequest(r)
	if token == "" {
		writeJSONError(rw, http.StatusUnauthorized, "no session token")
		return
	}
	sess, err := h.Sessions.GetSession(r.Context(), auth.SessionLookupKey(token))
	if err != nil {
		writeJSONError(rw, http.StatusUnauthorized, "session not found")
		return
	}
	sess.Tenant = target
	sess.Workspace = newWorkspace
	sess.Roles = newRoles
	if err := h.Sessions.PutSession(r.Context(), sess); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save session: %v", err))
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":    sess.Tenant,
		"workspace": sess.Workspace,
		"roles":     sess.Roles,
	})
}

const maxSelfServeOrgsPerUser = 10

func (h *orgAPI) countOrgsCreatedBy(ctx context.Context, subject string) (int, error) {
	if h.Memberships == nil {
		return 0, nil
	}
	ms, err := h.Memberships.ListByEmail(ctx, subject)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		if strings.EqualFold(m.InvitedBy, subject) {
			n++
		}
	}
	return n, nil
}

func (h *orgAPI) createOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Memberships == nil || h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "organizations not configured")
		return
	}
	if !h.auth.requireVerifiedInviter(rw, r, p) {
		return
	}
	// An unbounded self-serve create lets one account spin up orgs indefinitely.
	if n, err := h.countOrgsCreatedBy(r.Context(), p.Subject); err == nil && n >= maxSelfServeOrgsPerUser {
		writeJSONError(rw, http.StatusTooManyRequests,
			fmt.Sprintf("you've reached the limit of %d organizations per account — ask an admin if you need more", maxSelfServeOrgsPerUser))
		return
	}
	body, ok := decodeRequestJSON[struct {
		DisplayName string `json:"display_name"`
	}](rw, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if name == "" {
		writeJSONError(rw, http.StatusBadRequest, "display_name is required")
		return
	}
	if len([]rune(name)) > 80 {
		writeJSONError(rw, http.StatusBadRequest, "display_name must be 80 characters or fewer")
		return
	}
	tenant, err := mintOrgTenantID()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("mint tenant: %v", err))
		return
	}
	now := time.Now().UTC()
	if err := h.Memberships.PutMembership(r.Context(), auth.Membership{
		UserEmail: p.Subject,
		Tenant:    tenant,
		Workspace: "main",
		Roles:     []core.Role{core.TeamRoleAdmin()},
		InvitedBy: p.Subject,
		CreatedAt: now,
	}); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("create membership: %v", err))
		return
	}
	if err := h.Profiles.PutOrgProfile(r.Context(), auth.OrgProfile{
		Tenant:      tenant,
		DisplayName: name,
		UpdatedAt:   now,
	}); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save org profile: %v", err))
		return
	}
	h.audit(r.Context(), p, "org.create", tenant, "name="+name)
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       tenant,
		"display_name": name,
		"workspace":    "main",
	})
}

func (h *orgAPI) listMembers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Memberships == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "memberships not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot list members of another tenant")
		return
	}
	rows, err := h.Memberships.ListByTenant(r.Context(), tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	type memberDTO struct {
		Email     string      `json:"email"`
		Tenant    string      `json:"tenant"`
		Workspace string      `json:"workspace"`
		Roles     []core.Role `json:"roles"`
		InvitedBy string      `json:"invited_by,omitempty"`
		CreatedAt time.Time   `json:"created_at"`
		Home      bool        `json:"home"`
	}
	out := make([]memberDTO, 0, len(rows)+1)
	users, err := h.Users.ListUsers(r.Context())
	if err == nil {
		for _, u := range users {
			if u.Tenant == tenant {
				out = append(out, memberDTO{
					Email:     u.Email,
					Tenant:    u.Tenant,
					Workspace: u.Workspace,
					Roles:     u.Roles,
					CreatedAt: u.CreatedAt,
					Home:      true,
				})
				break // single home per tenant
			}
		}
	}
	for _, m := range rows {
		out = append(out, memberDTO{
			Email:     m.UserEmail,
			Tenant:    m.Tenant,
			Workspace: m.Workspace,
			Roles:     m.Roles,
			InvitedBy: m.InvitedBy,
			CreatedAt: m.CreatedAt,
			Home:      false,
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"members": out})
}

// The home owner cannot be removed, or the org is left with no admin.
func (h *orgAPI) removeMember(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Memberships == nil {
		writeJSONError(rw, http.StatusNotImplemented, "memberships not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot modify another tenant")
		return
	}
	if h.Users != nil {
		if u, err := h.Users.GetByEmail(r.Context(), email); err == nil && u.Tenant == tenant {
			writeJSONError(rw, http.StatusConflict, "cannot remove the org owner")
			return
		}
	}
	if m, err := h.Memberships.GetMembership(r.Context(), email, tenant); err == nil &&
		h.peerAdminBlocked(r.Context(), p, email, tenant, m.Roles) {
		writeJSONError(rw, http.StatusForbidden, "only the org owner can remove another admin")
		return
	}
	if err := h.Memberships.DeleteMembership(r.Context(), email, tenant); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.revokeMemberSessions(r.Context(), email)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *orgAPI) revokeMemberSessions(ctx context.Context, email string) {
	if h.Users == nil || h.Sessions == nil {
		return
	}
	rev, ok := h.Sessions.(auth.SessionRevoker)
	if !ok {
		// A custom store without SessionRevoker leaves the removed member signed in.
		h.logger.Printf("session store %T cannot revoke by subject — %s keeps existing sessions until they expire", h.Sessions, email)
		return
	}
	u, err := h.Users.GetByEmail(ctx, email)
	if err != nil {
		return // no local user record (e.g. SSO-only) — nothing to sweep
	}
	if n, err := rev.RevokeSubjectSessions(ctx, u.Subject); err != nil {
		h.logger.Printf("session sweep for %s: %v", email, err)
	} else if n > 0 {
		h.logger.Printf("session sweep for %s: %d session(s) revoked after membership change", email, n)
	}
}

func rolesGrantOrgAdmin(roles []core.Role) bool {
	for _, r := range roles {
		if r.Has(core.PermOrganizationAdmin) {
			return true
		}
	}
	return false
}

func (h *orgAPI) callerIsOrgOwner(ctx context.Context, p core.Principal, tenant string) bool {
	if h.Users == nil {
		return false
	}
	u, err := h.Users.GetByEmail(ctx, strings.ToLower(strings.TrimSpace(p.Subject)))
	return err == nil && u.Tenant == tenant
}

// A co-admin must not be able to remove or demote another admin.
func (h *orgAPI) peerAdminBlocked(ctx context.Context, p core.Principal, targetEmail, tenant string, targetRoles []core.Role) bool {
	if !rolesGrantOrgAdmin(targetRoles) {
		return false // target isn't an admin — ordinary member edit
	}
	if strings.EqualFold(strings.TrimSpace(targetEmail), strings.TrimSpace(p.Subject)) {
		return false // acting on yourself is fine
	}
	return !isPlatformAdmin(p) && !h.callerIsOrgOwner(ctx, p, tenant)
}

func resolveCatalogRoles(roles []core.Role) ([]core.Role, error) {
	out := make([]core.Role, len(roles))
	for i, r := range roles {
		if len(r.Permissions) > 0 {
			out[i] = r
			continue
		}
		cat, ok := core.TeamRoleByName(r.Name)
		if !ok {
			return nil, fmt.Errorf("role %q has no permissions and is not a catalog role (viewer/editor/admin)", r.Name)
		}
		out[i] = cat
	}
	return out, nil
}

// Refuses roles whose permissions exceed the caller's own: no privilege escalation.
func capRolesToCaller(p core.Principal, roles []core.Role) error {
	if isPlatformAdmin(p) {
		return nil
	}
	callerPerms := principalPermissions(p)
	for _, role := range roles {
		for _, perm := range role.Permissions {
			if perm == core.PermPlatformAdmin {
				return fmt.Errorf("only a platform admin may grant %q", core.PermPlatformAdmin)
			}
			if _, ok := callerPerms[perm]; !ok {
				return fmt.Errorf("cannot grant permission %q: it exceeds your own permissions", perm)
			}
		}
	}
	return nil
}

// In place, so it does not consume a seat.
func (h *orgAPI) updateMemberRoles(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Memberships == nil {
		writeJSONError(rw, http.StatusNotImplemented, "memberships not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot modify another tenant")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Roles []core.Role `json:"roles"`
	}](rw, r)
	if !ok {
		return
	}
	if len(body.Roles) == 0 {
		writeJSONError(rw, http.StatusBadRequest, "roles required — to remove access, delete the membership instead")
		return
	}
	roles, err := resolveCatalogRoles(body.Roles)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	body.Roles = roles
	if err := capRolesToCaller(p, body.Roles); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	if h.Users != nil {
		if u, err := h.Users.GetByEmail(r.Context(), email); err == nil && u.Tenant == tenant {
			writeJSONError(rw, http.StatusConflict, "cannot change the org owner's roles")
			return
		}
	}
	m, err := h.Memberships.GetMembership(r.Context(), email, tenant)
	if err != nil {
		if errors.Is(err, auth.ErrUnknownMembership) {
			writeJSONError(rw, http.StatusNotFound, "no such member")
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if h.peerAdminBlocked(r.Context(), p, email, tenant, m.Roles) {
		writeJSONError(rw, http.StatusForbidden, "only the org owner can change another admin's roles")
		return
	}
	m.Roles = body.Roles
	if err := h.Memberships.PutMembership(r.Context(), m); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.revokeMemberSessions(r.Context(), email)
	roleNames := make([]string, 0, len(body.Roles))
	for _, role := range body.Roles {
		roleNames = append(roleNames, role.Name)
	}
	h.audit(r.Context(), p, "member.roles.update", email, "roles="+strings.Join(roleNames, ","))
	writeJSON(rw, http.StatusOK, map[string]any{
		"email":     m.UserEmail,
		"tenant":    m.Tenant,
		"workspace": m.Workspace,
		"roles":     m.Roles,
	})
}

// Counts outstanding invitations as well as members, or an org could invite past
// its seat limit and only find out on acceptance.
func (h *orgAPI) invitationSeatExceeded(ctx context.Context, tenant, invitee string) (bool, int) {
	limit := h.svc.effectiveLimits(ctx, tenant).MaxMembers
	if limit <= 0 || h.Memberships == nil {
		return false, limit
	}
	held, ok := h.seatHolders(ctx, tenant)
	if !ok {
		return false, limit // fail open
	}
	if h.Invitations != nil {
		// Cannot see the outstanding promises, so fail closed.
		now := time.Now().UTC()
		if invs, err := h.Invitations.ListByTenant(ctx, tenant); err == nil {
			for _, inv := range invs {
				if inv.IsPending(now) {
					held[normalizeSeatEmail(inv.Email)] = struct{}{}
				}
			}
		}
	}
	delete(held, normalizeSeatEmail(invitee))
	return len(held) >= limit, limit
}

func normalizeSeatEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (h *orgAPI) createInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	// An unverified signup must not be able to mint invitations.
	mayEmailInvite := h.auth.inviterVerified(r, p)
	body, ok := decodeRequestJSON[struct {
		Email     string      `json:"email"`
		Roles     []core.Role `json:"roles"`
		Workspace string      `json:"workspace"`
	}](rw, r)
	if !ok {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if err := validSignupEmail(email); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Refuse at the gate, not on acceptance.
	if exceeded, limit := h.invitationSeatExceeded(r.Context(), p.Tenant, email); exceeded {
		writeJSONError(rw, http.StatusPaymentRequired,
			fmt.Sprintf("your plan includes %d members — upgrade to add more", limit))
		return
	}
	if len(body.Roles) > 0 {
		roles, err := resolveCatalogRoles(body.Roles)
		if err != nil {
			writeJSONError(rw, http.StatusBadRequest, err.Error())
			return
		}
		body.Roles = roles
		if err := capRolesToCaller(p, body.Roles); err != nil {
			writeJSONError(rw, http.StatusForbidden, err.Error())
			return
		}
	}
	if len(body.Roles) == 0 {
		body.Roles = []core.Role{core.TeamRoleEditor()}
	}
	if body.Workspace == "" {
		body.Workspace = "main"
	}
	token, err := auth.MintInvitationToken()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	const inviteTTL = 14 * 24 * time.Hour
	now := time.Now().UTC()
	inv := auth.Invitation{
		Token:     token,
		Email:     email,
		Tenant:    p.Tenant,
		Workspace: body.Workspace,
		Roles:     body.Roles,
		InvitedBy: p.Subject,
		CreatedAt: now,
		ExpiresAt: now.Add(inviteTTL),
	}
	if err := h.Invitations.PutInvitation(r.Context(), inv); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "invitation.create", token, "email="+email)
	emailSent := false
	if acceptURL := h.inviteURL(token); mayEmailInvite && h.svc.Mailer != nil && strings.HasPrefix(acceptURL, "http") {
		lang := h.inviteLang(r.Context(), email, p.Subject)
		m := maillang.For(lang)
		expFmt := datenames.FormatDate(inv.ExpiresAt, lang)
		content := emailtheme.Content{
			Subject:    m.OrgInviteSubject,
			Preheader:  fmt.Sprintf(m.OrgInvitePreheader, p.Subject),
			Eyebrow:    m.OrgInviteEyebrow,
			Heading:    m.OrgInviteHeading,
			Intro:      []string{fmt.Sprintf(m.OrgInviteIntro, p.Subject)},
			Button:     &emailtheme.Button{Label: m.OrgInviteButton, URL: acceptURL},
			Outro:      []string{fmt.Sprintf(m.OrgInviteExpiry, expFmt)},
			FooterNote: m.OrgInviteFooter,
			LogoURL:    emailLogoURL(h.svc.PublicBaseURL),
		}
		if err := h.svc.Mailer.SendThemed(r.Context(), email, emailtheme.PlainText(content), content); err != nil {
			h.logger.Printf("invite email to %s: %v", email, err)
		} else {
			emailSent = true
		}
	}
	writeJSON(rw, http.StatusCreated, map[string]any{
		"token":      token,
		"email":      email,
		"tenant":     inv.Tenant,
		"workspace":  inv.Workspace,
		"roles":      inv.Roles,
		"expires_at": inv.ExpiresAt,
		"accept_url": h.inviteURL(token),
		"email_sent": emailSent,
	})
}

func (h *orgAPI) inviteURL(token string) string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		return "/invite/" + token
	}
	return base + "/invite/" + token
}

func (h *orgAPI) listInvitations(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot list invitations of another tenant")
		return
	}
	rows, err := h.Invitations.ListByTenant(r.Context(), tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	type invDTO struct {
		Token      string      `json:"token"`
		Email      string      `json:"email"`
		Tenant     string      `json:"tenant"`
		Workspace  string      `json:"workspace"`
		Roles      []core.Role `json:"roles"`
		InvitedBy  string      `json:"invited_by"`
		CreatedAt  time.Time   `json:"created_at"`
		ExpiresAt  time.Time   `json:"expires_at"`
		AcceptedAt *time.Time  `json:"accepted_at,omitempty"`
		RevokedAt  *time.Time  `json:"revoked_at,omitempty"`
		Pending    bool        `json:"pending"`
		AcceptURL  string      `json:"accept_url"`
	}
	out := make([]invDTO, 0, len(rows))
	for _, i := range rows {
		out = append(out, invDTO{
			Token:      i.Token,
			Email:      i.Email,
			Tenant:     i.Tenant,
			Workspace:  i.Workspace,
			Roles:      i.Roles,
			InvitedBy:  i.InvitedBy,
			CreatedAt:  i.CreatedAt,
			ExpiresAt:  i.ExpiresAt,
			AcceptedAt: i.AcceptedAt,
			RevokedAt:  i.RevokedAt,
			Pending:    i.IsPending(now),
			AcceptURL:  h.inviteURL(i.Token),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"invitations": out})
}

func (h *orgAPI) revokeInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	token := r.PathValue("token")
	inv, err := h.Invitations.GetByToken(r.Context(), token)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "invitation not found")
		return
	}
	if inv.Tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot revoke another tenant's invitation")
		return
	}
	if err := h.Invitations.MarkRevoked(r.Context(), token, time.Now().UTC()); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "invitation.revoke", token, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *orgAPI) viewInvitation(rw http.ResponseWriter, r *http.Request) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	token := r.PathValue("token")
	inv, err := h.Invitations.GetByToken(r.Context(), token)
	if err != nil || inv.IsSignupInvite() {
		writeJSONError(rw, http.StatusNotFound, "invitation not found")
		return
	}
	now := time.Now().UTC()
	var orgName string
	if h.Profiles != nil {
		if pr, err := h.Profiles.GetOrgProfile(r.Context(), inv.Tenant); err == nil {
			orgName = pr.DisplayName
		}
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"email":          inv.Email,
		"tenant":         inv.Tenant,
		"tenant_display": orgName,
		"workspace":      inv.Workspace,
		"roles":          inv.Roles,
		"invited_by":     inv.InvitedBy,
		"expires_at":     inv.ExpiresAt,
		"pending":        inv.IsPending(now),
		"accepted":       inv.AcceptedAt != nil,
		"revoked":        inv.RevokedAt != nil,
		"expired":        !now.Before(inv.ExpiresAt),
	})
}

// The invited address must match the signed-in one, or anyone with the link joins.
func (h *orgAPI) acceptInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil || h.Memberships == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !strings.Contains(p.Subject, "@") {
		writeJSONError(rw, http.StatusForbidden, "only password-auth users can accept invitations")
		return
	}
	token := r.PathValue("token")
	inv, err := h.Invitations.GetByToken(r.Context(), token)
	if err != nil || inv.IsSignupInvite() {
		writeJSONError(rw, http.StatusNotFound, "invitation not found")
		return
	}
	now := time.Now().UTC()
	if !inv.IsPending(now) {
		writeJSONError(rw, http.StatusGone, "this invitation has already been used, revoked, or expired")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(p.Subject), strings.TrimSpace(inv.Email)) {
		writeJSONError(rw, http.StatusForbidden,
			"this invitation was sent to a different email — sign in with the email it was sent to")
		return
	}
	m := auth.Membership{
		UserEmail: p.Subject,
		Tenant:    inv.Tenant,
		Workspace: inv.Workspace,
		Roles:     inv.Roles,
		InvitedBy: inv.InvitedBy,
		CreatedAt: now,
	}
	seated, limit, err := h.seatMembership(r.Context(), m)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if !seated {
		writeJSONError(rw, http.StatusPaymentRequired,
			fmt.Sprintf("this organization has reached its %d-member limit — ask an admin to upgrade", limit))
		return
	}
	if h.Users != nil {
		if u, err := h.Users.GetByEmail(r.Context(), p.Subject); err == nil && !u.EmailVerified() {
			u.VerifiedAt = &now
			u.VerifyTokenHash = nil
			u.VerifyExpiresAt = nil
			if err := h.Users.PutUser(r.Context(), u); err != nil {
				h.logger.Printf("verify on invite accept for %s: %v", p.Subject, err)
			} else {
				h.auditAuth(r.Context(), r, u.Tenant, u.Email, "auth.email_verified", "invite_accept")
			}
		}
	}
	if err := h.Invitations.MarkAccepted(r.Context(), token, now); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "invitation.accept", token, "tenant="+inv.Tenant)
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":    inv.Tenant,
		"workspace": inv.Workspace,
		"roles":     inv.Roles,
	})
}

// Minus the client secret, which never leaves the daemon.
func (h *orgAPI) getOrgAuthConfig(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot view another tenant's SSO config")
		return
	}
	cfg, err := h.OrgAuth.GetOrgAuth(r.Context(), tenant)
	if err != nil {
		if errors.Is(err, auth.ErrUnknownOrgAuth) {
			writeJSON(rw, http.StatusOK, map[string]any{
				"tenant":                  tenant,
				"google_enabled":          false,
				"google_client_id":        "",
				"google_workspace_domain": "",
			})
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":                  cfg.Tenant,
		"google_enabled":          cfg.GoogleEnabled(),
		"google_client_id":        cfg.GoogleClientID,
		"google_workspace_domain": cfg.GoogleWorkspaceDomain,
		"google_secret_set":       cfg.GoogleClientSecret != "",
		"updated_at":              cfg.UpdatedAt,
	})
}

func (h *orgAPI) putOrgAuthConfig(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	body, ok := decodeRequestJSON[struct {
		GoogleClientID        string `json:"google_client_id"`
		GoogleClientSecret    string `json:"google_client_secret"`
		GoogleWorkspaceDomain string `json:"google_workspace_domain"`
	}](rw, r)
	if !ok {
		return
	}
	cfg := auth.OrgAuthConfig{
		Tenant:                p.Tenant,
		GoogleClientID:        strings.TrimSpace(body.GoogleClientID),
		GoogleClientSecret:    strings.TrimSpace(body.GoogleClientSecret),
		GoogleWorkspaceDomain: strings.TrimSpace(body.GoogleWorkspaceDomain),
		UpdatedAt:             time.Now().UTC(),
	}
	if cfg.GoogleClientSecret == "" {
		if old, err := h.OrgAuth.GetOrgAuth(r.Context(), p.Tenant); err == nil {
			cfg.GoogleClientSecret = old.GoogleClientSecret
		}
	}
	if err := h.OrgAuth.PutOrgAuth(r.Context(), cfg); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "org_auth.update", p.Tenant, "")
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":         cfg.Tenant,
		"google_enabled": cfg.GoogleEnabled(),
	})
}

func (h *orgAPI) deleteOrgAuthConfig(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	if err := h.OrgAuth.DeleteOrgAuth(r.Context(), p.Tenant); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "org_auth.delete", p.Tenant, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *orgAPI) getPublicAuthConfig(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"signup_enabled":  h.EnableSignup,
		"admin_bootstrap": h.auth.adminBootstrapAvailable(r.Context()),
		"wildcard_domain": h.WildcardDomain,
	})
}

func (h *orgAPI) getPublicSSOStatus(rw http.ResponseWriter, r *http.Request) {
	if h.OrgAuth == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"google_enabled": false})
		return
	}
	tenant := r.PathValue("tenant")
	cfg, err := h.OrgAuth.GetOrgAuth(r.Context(), tenant)
	if err != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"google_enabled": false})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"google_enabled":          cfg.GoogleEnabled(),
		"google_workspace_domain": cfg.GoogleWorkspaceDomain,
	})
}

type seatQuota struct {
	svc         *Service
	Users       auth.UserStore
	Memberships auth.MembershipStore
}

func (q seatQuota) ownerEmail(ctx context.Context, tenant string) string {
	if q.Users == nil {
		return ""
	}
	users, err := q.Users.ListUsers(ctx)
	if err != nil {
		return ""
	}
	for _, u := range users {
		if u.Tenant == tenant {
			return normalizeSeatEmail(u.Email) // single home per tenant
		}
	}
	return ""
}

// Members and outstanding invitations both occupy a seat.
func (q seatQuota) seatHolders(ctx context.Context, tenant string) (map[string]struct{}, bool) {
	members, err := q.Memberships.ListByTenant(ctx, tenant)
	if err != nil {
		return nil, false
	}
	held := make(map[string]struct{}, len(members)+1)
	for _, m := range members {
		held[normalizeSeatEmail(m.UserEmail)] = struct{}{}
	}
	if owner := q.ownerEmail(ctx, tenant); owner != "" {
		held[owner] = struct{}{}
	}
	return held, true
}

func (q seatQuota) seatQuotaExceeded(ctx context.Context, tenant string) (bool, int) {
	limit := q.svc.effectiveLimits(ctx, tenant).MaxMembers
	if limit <= 0 || q.Memberships == nil {
		return false, limit
	}
	held, ok := q.seatHolders(ctx, tenant)
	if !ok {
		return false, limit // fail open
	}
	return len(held) >= limit, limit
}

// Refuses a NEW member with no seat left; a role change is always allowed.
func (q seatQuota) seatMembership(ctx context.Context, m auth.Membership) (bool, int, error) {
	limit := q.svc.effectiveLimits(ctx, m.Tenant).MaxMembers
	if limit <= 0 {
		return true, limit, q.Memberships.PutMembership(ctx, m) // uncapped
	}
	if sl, ok := q.Memberships.(auth.SeatLimitedMembershipStore); ok {
		rowLimit := limit
		if q.ownerEmail(ctx, m.Tenant) != "" {
			rowLimit--
		}
		if rowLimit < 0 {
			rowLimit = 0
		}
		seated, err := sl.PutMembershipWithinLimit(ctx, m, rowLimit)
		return seated, limit, err
	}
	if exceeded, _ := q.seatQuotaExceeded(ctx, m.Tenant); exceeded {
		return false, limit, nil
	}
	return true, limit, q.Memberships.PutMembership(ctx, m)
}

func (h *HTTPGateway) seats() seatQuota {
	return seatQuota{svc: h.svc, Users: h.Users, Memberships: h.Memberships}
}
