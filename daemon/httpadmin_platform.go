// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

type platformAdminAPI struct {
	auditor
	adminCheck
	svc                 *Service
	Users               auth.UserStore
	Sessions            auth.SessionStore
	Memberships         auth.MembershipStore
	Invitations         auth.InvitationStore
	Profiles            auth.OrgProfileStore
	Blocklist           auth.BlocklistStore
	PlatformAdminGrants PlatformAdminStore
	DropSwitches        DropSwitchStore
}

func (h *HTTPGateway) platformAdminAPI() *platformAdminAPI {
	return &platformAdminAPI{auditor: h.auditor(), adminCheck: h.admins(), svc: h.svc, Users: h.Users, Sessions: h.Sessions, Memberships: h.Memberships, Invitations: h.Invitations, Profiles: h.Profiles, Blocklist: h.Blocklist, PlatformAdminGrants: h.PlatformAdminGrants, DropSwitches: h.DropSwitches}
}

// Cross-tenant tools; every route here is gated on platform:admin.

type platformUserDTO struct {
	Email            string     `json:"email"`
	Subject          string     `json:"subject"`
	Tenant           string     `json:"tenant"`
	TenantName       string     `json:"tenant_name,omitempty"`
	Status           string     `json:"status"`
	SuspendedAt      *time.Time `json:"suspended_at,omitempty"`
	SuspendReason    string     `json:"suspend_reason,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	Verified         bool       `json:"verified"`
	PlatformAdmin    bool       `json:"platform_admin"`
	PlatformAdminEnv bool       `json:"platform_admin_env"`
}

type platformOrgDTO struct {
	Tenant        string     `json:"tenant"`
	DisplayName   string     `json:"display_name"`
	Icon          string     `json:"icon,omitempty"`
	Subdomain     string     `json:"subdomain,omitempty"`
	Status        string     `json:"status"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	SuspendReason string     `json:"suspend_reason,omitempty"`
	MemberCount   int        `json:"member_count"`
}

type platformDropDTO struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	Integration      string   `json:"integration,omitempty"`
	Icon             string   `json:"icon,omitempty"`
	Category         string   `json:"category,omitempty"`
	Color            string   `json:"color,omitempty"`
	BrandLogo        string   `json:"brand_logo,omitempty"`
	GloballyDisabled bool     `json:"globally_disabled"`
	DisabledTenants  []string `json:"disabled_tenants,omitempty"`
	// Only for a tenant runner's drop: an instance-wide one belongs to nobody.
	OwnedByTenants []string `json:"owned_by_tenants,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type moderationBody struct {
	Reason string `json:"reason"`
	Tenant string `json:"tenant"`
	Domain bool   `json:"domain"`
}

func decodeModerationBody(r *http.Request) moderationBody {
	var b moderationBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&b)
	}
	b.Reason = strings.TrimSpace(b.Reason)
	b.Tenant = strings.TrimSpace(b.Tenant)
	return b
}

// Shared, so no route here can forget the gate.
func (h *platformAdminAPI) requirePlatform(rw http.ResponseWriter, p core.Principal) bool {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return false
	}
	if h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "user store not configured")
		return false
	}
	return true
}

func (h *platformAdminAPI) platformListUsers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	users, err := h.Users.ListUsers(r.Context())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	tenants := make([]string, 0, len(users))
	for _, u := range users {
		tenants = append(tenants, u.Tenant)
	}
	names := h.tenantNames(r.Context(), tenants)
	out := make([]platformUserDTO, 0, len(users))
	for _, u := range users {
		out = append(out, h.toPlatformUserDTO(u, names[u.Tenant]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Email < out[j].Email })
	writeJSON(rw, http.StatusOK, map[string]any{"users": out})
}

func (h *platformAdminAPI) toPlatformUserDTO(u auth.User, tenantName string) platformUserDTO {
	status := u.Status
	if status == "" {
		status = auth.StatusActive
	}
	return platformUserDTO{
		Email:            u.Email,
		Subject:          u.Subject,
		Tenant:           u.Tenant,
		TenantName:       tenantName,
		Status:           status,
		SuspendedAt:      u.SuspendedAt,
		SuspendReason:    u.SuspendReason,
		CreatedAt:        u.CreatedAt,
		Verified:         u.EmailVerified(),
		PlatformAdmin:    h.isPlatformAdmin(u.Email),
		PlatformAdminEnv: h.isPlatformAdminEmail(u.Email),
	}
}

func (h *platformAdminAPI) tenantNames(ctx context.Context, tenants []string) map[string]string {
	out := map[string]string{}
	if h.Profiles == nil || len(tenants) == 0 {
		return out
	}
	profs, err := h.Profiles.ListOrgProfiles(ctx, tenants)
	if err != nil {
		return out
	}
	for tn, p := range profs {
		if p.DisplayName != "" {
			out[tn] = p.DisplayName
		}
	}
	return out
}

func (h *platformAdminAPI) platformGetUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "no such account")
		return
	}
	resp := map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])}
	if h.Memberships != nil {
		if rows, err := h.Memberships.ListByEmail(r.Context(), email); err == nil {
			orgs := make([]string, 0, len(rows))
			for _, m := range rows {
				orgs = append(orgs, m.Tenant)
			}
			resp["memberships"] = orgs
		}
	}
	writeJSON(rw, http.StatusOK, resp)
}

func (h *platformAdminAPI) platformSuspendUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	body := decodeModerationBody(r)
	u, ok := h.guardUserModeration(rw, r.Context(), p, email)
	if !ok {
		return
	}
	u.Status = auth.StatusSuspended
	now := time.Now().UTC()
	u.SuspendedAt = &now
	u.SuspendReason = body.Reason
	if err := h.Users.PutUser(r.Context(), u); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateModeration(u.Subject, "")
	h.revokeSubjectSessions(r.Context(), u.Subject)
	h.audit(r.Context(), p, "platform.user.suspend", email, body.Reason)
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

func (h *platformAdminAPI) platformUnsuspendUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "no such account")
		return
	}
	u.Status = auth.StatusActive
	u.SuspendedAt = nil
	u.SuspendReason = ""
	if err := h.Users.PutUser(r.Context(), u); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateModeration(u.Subject, "")
	h.audit(r.Context(), p, "platform.user.unsuspend", email, "")
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

func (h *platformAdminAPI) platformVerifyUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "no such account")
		return
	}
	if !u.EmailVerified() {
		now := time.Now().UTC()
		u.VerifiedAt = &now
		u.VerifyTokenHash = nil
		u.VerifyExpiresAt = nil
		if err := h.Users.PutUser(r.Context(), u); err != nil {
			writeJSONError(rw, http.StatusInternalServerError, err.Error())
			return
		}
		h.audit(r.Context(), p, "platform.user.verify", email, "")
	}
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

func (h *platformAdminAPI) platformGrantAdmin(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	if h.PlatformAdminGrants == nil {
		writeJSONError(rw, http.StatusNotImplemented, "platform-admin grant store not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "no such account")
		return
	}
	if h.isPlatformAdminEmail(email) {
		writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
		return
	}
	if err := h.PlatformAdminGrants.Grant(r.Context(), email, p.Subject); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.revokeSubjectSessions(r.Context(), u.Subject)
	h.audit(r.Context(), p, "platform.user.grant_admin", email, "")
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

func (h *platformAdminAPI) platformRevokeAdmin(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	if h.PlatformAdminGrants == nil {
		writeJSONError(rw, http.StatusNotImplemented, "platform-admin grant store not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return
	}
	if email == strings.ToLower(strings.TrimSpace(p.Subject)) {
		writeJSONError(rw, http.StatusBadRequest, "you can't revoke your own platform-admin role")
		return
	}
	if h.isPlatformAdminEmail(email) {
		writeJSONError(rw, http.StatusConflict,
			"this admin is granted by DAZYFLOW_PLATFORM_ADMINS — remove the email there and restart to revoke")
		return
	}
	if err := h.PlatformAdminGrants.Revoke(r.Context(), email); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if u, err := h.Users.GetByEmail(r.Context(), email); err == nil {
		h.revokeSubjectSessions(r.Context(), u.Subject)
	}
	h.audit(r.Context(), p, "platform.user.revoke_admin", email, "")
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"email": email, "platform_admin": false})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

func (h *platformAdminAPI) platformBanUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	if h.Blocklist == nil {
		writeJSONError(rw, http.StatusNotImplemented, "blocklist store not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	body := decodeModerationBody(r)
	u, ok := h.guardUserModeration(rw, r.Context(), p, email)
	if !ok {
		return
	}
	u.Status = auth.StatusSuspended
	now := time.Now().UTC()
	u.SuspendedAt = &now
	u.SuspendReason = body.Reason
	if err := h.Users.PutUser(r.Context(), u); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateModeration(u.Subject, "")
	h.revokeSubjectSessions(r.Context(), u.Subject)
	value, kind := email, auth.BlockEmail
	if body.Domain {
		if d := emailDomainOf(email); d != "" {
			value, kind = d, auth.BlockDomain
		}
	}
	if err := h.Blocklist.Block(r.Context(), auth.Blocked{
		Value: value, Kind: kind, Reason: body.Reason, CreatedBy: p.Subject,
	}); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "platform.user.ban", email, body.Reason)
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant]), "blocked": value})
}

func (h *platformAdminAPI) guardUserModeration(rw http.ResponseWriter, ctx context.Context, p core.Principal, email string) (auth.User, bool) {
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return auth.User{}, false
	}
	if email == strings.ToLower(strings.TrimSpace(p.Subject)) {
		writeJSONError(rw, http.StatusBadRequest, "you can't moderate your own account")
		return auth.User{}, false
	}
	if h.isPlatformAdmin(email) {
		writeJSONError(rw, http.StatusForbidden, "can't moderate a platform admin")
		return auth.User{}, false
	}
	u, err := h.Users.GetByEmail(ctx, email)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "no such account")
		return auth.User{}, false
	}
	return u, true
}

func (h *platformAdminAPI) revokeSubjectSessions(ctx context.Context, subject string) {
	if rev, ok := h.Sessions.(auth.SessionRevoker); ok && subject != "" {
		_, _ = rev.RevokeSubjectSessions(ctx, subject)
	}
}

func (h *platformAdminAPI) invalidateModeration(subject, tenant string) {
	if g, ok := h.svc.Auth.(*auth.ModerationGate); ok {
		g.Invalidate(subject, tenant)
	}
}

func (h *platformAdminAPI) signInLockout(ctx context.Context, u auth.User) (string, bool) {
	if u.Suspended() {
		return "your account has been suspended", true
	}
	if h.svc.orgSuspended(ctx, u.Tenant) {
		return "your organization has been suspended", true
	}
	return "", false
}

func emailDomainOf(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return ""
	}
	return email[at+1:]
}

func (h *platformAdminAPI) platformListOrgs(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	lister, ok := h.Profiles.(interface {
		ListAllOrgProfiles(ctx context.Context) ([]auth.OrgProfile, error)
	})
	if h.Profiles == nil || !ok {
		writeJSONError(rw, http.StatusNotImplemented, "org profile store not configured")
		return
	}
	profiles, err := lister.ListAllOrgProfiles(r.Context())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]platformOrgDTO, 0, len(profiles))
	for _, pr := range profiles {
		out = append(out, h.toPlatformOrgDTO(r.Context(), pr))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"orgs": out})
}

func (h *platformAdminAPI) toPlatformOrgDTO(ctx context.Context, pr auth.OrgProfile) platformOrgDTO {
	status := pr.Status
	if status == "" {
		status = auth.StatusActive
	}
	count := 0
	if h.Memberships != nil {
		if rows, err := h.Memberships.ListByTenant(ctx, pr.Tenant); err == nil {
			count = len(rows)
		}
	}
	return platformOrgDTO{
		Tenant:        pr.Tenant,
		DisplayName:   pr.DisplayName,
		Icon:          pr.Icon,
		Subdomain:     pr.Subdomain,
		Status:        status,
		SuspendedAt:   pr.SuspendedAt,
		SuspendReason: pr.SuspendReason,
		MemberCount:   count,
	}
}

func (h *platformAdminAPI) platformGetOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profile store not configured")
		return
	}
	tenant := strings.TrimSpace(r.PathValue("tenant"))
	pr, err := h.Profiles.GetOrgProfile(r.Context(), tenant)
	if err != nil {
		pr = auth.OrgProfile{Tenant: tenant, DisplayName: tenant, Status: auth.StatusActive}
	}
	resp := map[string]any{
		"org":       h.toPlatformOrgDTO(r.Context(), pr),
		"effective": h.svc.effectiveLimits(r.Context(), tenant),
	}
	if h.Memberships != nil {
		if rows, err := h.Memberships.ListByTenant(r.Context(), tenant); err == nil {
			members := make([]string, 0, len(rows))
			for _, m := range rows {
				members = append(members, m.UserEmail)
			}
			resp["members"] = members
		}
	}
	writeJSON(rw, http.StatusOK, resp)
}

func (h *platformAdminAPI) platformSuspendOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setOrgSuspended(rw, r, p, true, false)
}

func (h *platformAdminAPI) platformUnsuspendOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setOrgSuspended(rw, r, p, false, false)
}

func (h *platformAdminAPI) platformBanOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setOrgSuspended(rw, r, p, true, true)
}

func (h *platformAdminAPI) setOrgSuspended(rw http.ResponseWriter, r *http.Request, p core.Principal, suspend, ban bool) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profile store not configured")
		return
	}
	tenant := strings.TrimSpace(r.PathValue("tenant"))
	if tenant == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant required")
		return
	}
	if tenant == p.Tenant {
		writeJSONError(rw, http.StatusBadRequest, "you can't moderate your own org")
		return
	}
	body := decodeModerationBody(r)
	pr, err := h.Profiles.GetOrgProfile(r.Context(), tenant)
	if err != nil {
		pr = auth.OrgProfile{Tenant: tenant, DisplayName: tenant}
	}
	if suspend {
		pr.Status = auth.StatusSuspended
		now := time.Now().UTC()
		pr.SuspendedAt = &now
		pr.SuspendReason = body.Reason
	} else {
		pr.Status = auth.StatusActive
		pr.SuspendedAt = nil
		pr.SuspendReason = ""
	}
	if err := h.Profiles.PutOrgProfile(r.Context(), pr); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.invalidateModeration("", pr.Tenant)
	action := "platform.org.unsuspend"
	if suspend {
		action = "platform.org.suspend"
		h.revokeOrgMemberSessions(r.Context(), tenant)
	}
	if ban {
		action = "platform.org.ban"
		h.banOrgMembers(r.Context(), p, tenant, body.Reason)
	}
	h.audit(r.Context(), p, action, tenant, body.Reason)
	writeJSON(rw, http.StatusOK, map[string]any{"org": h.toPlatformOrgDTO(r.Context(), pr)})
}

func (h *platformAdminAPI) revokeOrgMemberSessions(ctx context.Context, tenant string) {
	for _, email := range h.orgMemberEmails(ctx, tenant) {
		if u, err := h.Users.GetByEmail(ctx, email); err == nil {
			h.revokeSubjectSessions(ctx, u.Subject)
		}
	}
}

func (h *platformAdminAPI) banOrgMembers(ctx context.Context, p core.Principal, tenant, reason string) {
	if h.Blocklist == nil {
		return
	}
	for _, email := range h.orgMemberEmails(ctx, tenant) {
		if h.isPlatformAdminEmail(email) {
			continue // never blocklist an operator
		}
		_ = h.Blocklist.Block(ctx, auth.Blocked{
			Value: email, Kind: auth.BlockEmail, Reason: reason, CreatedBy: p.Subject,
		})
	}
}

func (h *platformAdminAPI) orgMemberEmails(ctx context.Context, tenant string) []string {
	seen := map[string]bool{}
	if h.Memberships != nil {
		if rows, err := h.Memberships.ListByTenant(ctx, tenant); err == nil {
			for _, m := range rows {
				seen[strings.ToLower(m.UserEmail)] = true
			}
		}
	}
	if users, err := h.Users.ListUsers(ctx); err == nil {
		for _, u := range users {
			if u.Tenant == tenant {
				seen[strings.ToLower(u.Email)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	return out
}

func (h *platformAdminAPI) platformListDrops(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.DropSwitches == nil {
		writeJSONError(rw, http.StatusNotImplemented, "step killswitch not configured")
		return
	}
	mp, ok := h.svc.Engine.Resolver.(interface {
		AllManifests() (map[string]core.Manifest, map[string][]string)
	})
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "resolver has no catalog")
		return
	}
	switches, err := h.DropSwitches.List(r.Context())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	global := map[string]string{}      // id -> reason
	perTenant := map[string][]string{} // id -> tenants
	for _, sw := range switches {
		if sw.Tenant == "" {
			global[sw.DropID] = sw.Reason
		} else {
			perTenant[sw.DropID] = append(perTenant[sw.DropID], sw.Tenant)
		}
	}
	manifests, remoteTenants := mp.AllManifests()
	out := make([]platformDropDTO, 0, len(manifests))
	for id, m := range manifests {
		reason, isGlobal := global[id]
		out = append(out, platformDropDTO{
			ID:               id,
			Label:            m.Label,
			Integration:      m.Integration,
			Icon:             m.Icon,
			Category:         m.Category,
			Color:            m.Color,
			BrandLogo:        m.BrandLogo,
			GloballyDisabled: isGlobal,
			DisabledTenants:  perTenant[id],
			OwnedByTenants:   remoteTenants[id],
			Reason:           reason,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(rw, http.StatusOK, map[string]any{"drops": out})
}

func (h *platformAdminAPI) platformDisableDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.DropSwitches == nil {
		writeJSONError(rw, http.StatusNotImplemented, "step killswitch not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(rw, http.StatusBadRequest, "step id required")
		return
	}
	body := decodeModerationBody(r)
	if err := h.DropSwitches.Disable(r.Context(), DropSwitch{
		DropID: id, Tenant: body.Tenant, DisabledBy: p.Subject, Reason: body.Reason,
	}); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	target := id
	if body.Tenant != "" {
		target = id + "@" + body.Tenant
	}
	h.audit(r.Context(), p, "platform.drop.disable", target, body.Reason)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *platformAdminAPI) platformEnableDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.DropSwitches == nil {
		writeJSONError(rw, http.StatusNotImplemented, "step killswitch not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(rw, http.StatusBadRequest, "step id required")
		return
	}
	body := decodeModerationBody(r)
	if err := h.DropSwitches.Enable(r.Context(), id, body.Tenant); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	target := id
	if body.Tenant != "" {
		target = id + "@" + body.Tenant
	}
	h.audit(r.Context(), p, "platform.drop.enable", target, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *platformAdminAPI) requirePlatformEntitlements(rw http.ResponseWriter, p core.Principal) bool {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return false
	}
	if h.svc.Entitlements == nil {
		writeJSONError(rw, http.StatusNotImplemented, "entitlements not configured")
		return false
	}
	return true
}

func (h *platformAdminAPI) platformListTiers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatformEntitlements(rw, p) {
		return
	}
	tiers, err := h.svc.Entitlements.ListTiers(r.Context())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tiers": tiers})
}

func (h *platformAdminAPI) platformPutTier(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatformEntitlements(rw, p) {
		return
	}
	var t Tier
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode tier: "+err.Error())
		return
	}
	if id := strings.TrimSpace(r.PathValue("id")); id != "" {
		t.ID = id
	}
	t.ID = strings.TrimSpace(t.ID)
	if t.ID == "" {
		writeJSONError(rw, http.StatusBadRequest, "tier id required")
		return
	}
	if existing, ok := h.svc.Entitlements.GetTier(r.Context(), t.ID); ok {
		t.BuiltIn = existing.BuiltIn
	} else {
		t.BuiltIn = false
	}
	if err := h.svc.Entitlements.PutTier(r.Context(), t); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "platform.tier.put", t.ID, "")
	writeJSON(rw, http.StatusOK, map[string]any{"tier": t})
}

func (h *platformAdminAPI) platformDeleteTier(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatformEntitlements(rw, p) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := h.svc.Entitlements.DeleteTier(r.Context(), id); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "platform.tier.delete", id, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *platformAdminAPI) platformGetEntitlement(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatformEntitlements(rw, p) {
		return
	}
	tenant := strings.TrimSpace(r.PathValue("tenant"))
	if tenant == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant required")
		return
	}
	ent, _ := h.svc.Entitlements.GetEntitlement(r.Context(), tenant)
	ent.Tenant = tenant
	tiers, _ := h.svc.Entitlements.ListTiers(r.Context())
	writeJSON(rw, http.StatusOK, map[string]any{
		"entitlement": ent,
		"effective":   h.svc.effectiveLimits(r.Context(), tenant),
		"tiers":       tiers,
	})
}

func (h *platformAdminAPI) platformPutEntitlement(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatformEntitlements(rw, p) {
		return
	}
	tenant := strings.TrimSpace(r.PathValue("tenant"))
	if tenant == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant required")
		return
	}
	var ent TenantEntitlement
	if err := json.NewDecoder(r.Body).Decode(&ent); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode entitlement: "+err.Error())
		return
	}
	ent.Tenant = tenant
	if ent.PlanOverride != "" && ent.PlanOverride != PlanFree && ent.PlanOverride != PlanPro {
		writeJSONError(rw, http.StatusBadRequest, "plan_override must be empty, 'free', or 'pro'")
		return
	}
	if err := h.svc.Entitlements.PutEntitlement(r.Context(), ent); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "platform.entitlement.put", tenant, ent.TierID)
	writeJSON(rw, http.StatusOK, map[string]any{
		"entitlement": ent,
		"effective":   h.svc.effectiveLimits(r.Context(), tenant),
	})
}

func (h *platformAdminAPI) platformInviteMember(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	tenant := strings.TrimSpace(r.PathValue("tenant"))
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
	roles := body.Roles
	if len(roles) > 0 {
		resolved, err := resolveCatalogRoles(roles)
		if err != nil {
			writeJSONError(rw, http.StatusBadRequest, err.Error())
			return
		}
		roles = resolved
	} else {
		roles = []core.Role{core.TeamRoleEditor()}
	}
	ws := body.Workspace
	if ws == "" {
		ws = "main"
	}
	token, err := auth.MintInvitationToken()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	inv := auth.Invitation{
		Token:     token,
		Email:     email,
		Tenant:    tenant,
		Workspace: ws,
		Roles:     roles,
		InvitedBy: p.Subject,
		CreatedAt: now,
		ExpiresAt: now.Add(14 * 24 * time.Hour),
	}
	if err := h.Invitations.PutInvitation(r.Context(), inv); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "platform.member.invite", tenant, "email="+email)
	writeJSON(rw, http.StatusCreated, map[string]any{"token": token, "email": email, "tenant": tenant})
}
