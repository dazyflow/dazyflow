package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// Platform-admin moderation surface: the cross-tenant tools a SaaS
// operator (platform:admin) uses to keep the deployment healthy —
// suspend/ban/delete misbehaving users and orgs, and a global/per-org
// killswitch for individual drops. All handlers gate on platform:admin
// (not the per-org organization:admin) and write an audit event. The
// state they flip is enforced elsewhere: the auth ModerationGate (lockout),
// SubmitGraph (org flow halt), and the engine resolver (drop killswitch).

// ---- request/response shapes ---------------------------------------

type platformUserDTO struct {
	Email   string `json:"email"`
	Subject string `json:"subject"`
	Tenant  string `json:"tenant"`
	// TenantName is the home org's human-facing display name, resolved
	// from the profile store so the UI can show "Brightleaf" instead of
	// the opaque "usr_38f4657c". Empty when the org has no profile — the
	// client falls back to the raw id.
	TenantName    string     `json:"tenant_name,omitempty"`
	Status        string     `json:"status"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	SuspendReason string     `json:"suspend_reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	Verified      bool       `json:"verified"`
	PlatformAdmin bool       `json:"platform_admin"`
}

type platformOrgDTO struct {
	Tenant      string `json:"tenant"`
	DisplayName string `json:"display_name"`
	// Icon is the org's uploaded logo (a data: URL) or empty — the UI
	// renders an <img> when present, a monogram tile otherwise.
	Icon          string     `json:"icon,omitempty"`
	Subdomain     string     `json:"subdomain,omitempty"`
	Status        string     `json:"status"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	SuspendReason string     `json:"suspend_reason,omitempty"`
	MemberCount   int        `json:"member_count"`
}

type platformDropDTO struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Integration string `json:"integration,omitempty"`
	// Icon / Category / Color / BrandLogo mirror the manifest so the page
	// can render the exact same glyph treatment as the build palette.
	Icon      string `json:"icon,omitempty"`
	Category  string `json:"category,omitempty"`
	Color     string `json:"color,omitempty"`
	BrandLogo string `json:"brand_logo,omitempty"`
	// GloballyDisabled is set when a switch with empty tenant exists.
	GloballyDisabled bool `json:"globally_disabled"`
	// DisabledTenants lists the tenants this drop is switched off for
	// (excludes the global switch). Empty when only globally toggled.
	DisabledTenants []string `json:"disabled_tenants,omitempty"`
	Reason          string   `json:"reason,omitempty"`
}

// moderationBody is the shared JSON body for suspend/ban/disable actions.
type moderationBody struct {
	Reason string `json:"reason"`
	// Tenant scopes a drop switch to one org ("" = global). Ignored by the
	// user/org handlers.
	Tenant string `json:"tenant"`
	// Domain, on a user ban, blocks the whole email domain rather than the
	// single address — for shutting down a throwaway-domain abuser.
	Domain bool `json:"domain"`
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

// requirePlatform is the shared gate + nil-store guard for these
// handlers. Returns false (after writing the error) when the caller
// isn't a platform admin or the user store isn't configured.
func (h *HTTPGateway) requirePlatform(rw http.ResponseWriter, p core.Principal) bool {
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

// ---- users ----------------------------------------------------------

// platformListUsers returns every account on the deployment with its
// moderation state — the platform-admin user roster.
func (h *HTTPGateway) platformListUsers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requirePlatform(rw, p) {
		return
	}
	users, err := h.Users.ListUsers(r.Context())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Resolve every home-org id to its display name in one batch so the
	// roster shows real names, not opaque tenant ids.
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

func (h *HTTPGateway) toPlatformUserDTO(u auth.User, tenantName string) platformUserDTO {
	status := u.Status
	if status == "" {
		status = auth.StatusActive
	}
	return platformUserDTO{
		Email:         u.Email,
		Subject:       u.Subject,
		Tenant:        u.Tenant,
		TenantName:    tenantName,
		Status:        status,
		SuspendedAt:   u.SuspendedAt,
		SuspendReason: u.SuspendReason,
		CreatedAt:     u.CreatedAt,
		Verified:      u.EmailVerified(),
		PlatformAdmin: h.isPlatformAdminEmail(u.Email),
	}
}

// tenantNames batch-resolves tenant ids to org display names via the
// profile store. Missing profiles / a nil store simply yield no entry —
// callers fall back to the raw id.
func (h *HTTPGateway) tenantNames(ctx context.Context, tenants []string) map[string]string {
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

// platformGetUser returns one account plus the orgs it belongs to.
func (h *HTTPGateway) platformGetUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	// Org memberships, if the multi-org store is wired.
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

// platformSuspendUser locks an account: it sets the suspended status and
// kills the user's live sessions. Future requests (sessions AND API keys)
// are refused by the auth ModerationGate. Reversible via unsuspend.
func (h *HTTPGateway) platformSuspendUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	h.revokeSubjectSessions(r.Context(), u.Subject)
	h.audit(r.Context(), p, "platform.user.suspend", email, body.Reason)
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

// platformUnsuspendUser reverses a suspension, restoring access.
func (h *HTTPGateway) platformUnsuspendUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	h.audit(r.Context(), p, "platform.user.unsuspend", email, "")
	writeJSON(rw, http.StatusOK, map[string]any{"user": h.toPlatformUserDTO(u, h.tenantNames(r.Context(), []string{u.Tenant})[u.Tenant])})
}

// platformBanUser suspends the account AND blocklists the email (or its
// whole domain) so the person can't simply re-register. The account data
// is kept — use delete (the GDPR erase endpoint) to remove it entirely.
func (h *HTTPGateway) platformBanUser(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

// guardUserModeration loads the target user and refuses the foot-guns:
// acting on yourself, or on another platform admin (the env allowlist
// can't be edited here, and suspending one in the DB would lock out a
// fellow operator). Writes the error and returns ok=false on refusal.
func (h *HTTPGateway) guardUserModeration(rw http.ResponseWriter, ctx context.Context, p core.Principal, email string) (auth.User, bool) {
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return auth.User{}, false
	}
	if email == strings.ToLower(strings.TrimSpace(p.Subject)) {
		writeJSONError(rw, http.StatusBadRequest, "you can't moderate your own account")
		return auth.User{}, false
	}
	if h.isPlatformAdminEmail(email) {
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

// revokeSubjectSessions kills every live session for a subject so a
// suspension/ban takes effect immediately, not just on the session's
// next request. Best-effort: the ModerationGate is the real enforcement.
func (h *HTTPGateway) revokeSubjectSessions(ctx context.Context, subject string) {
	if rev, ok := h.Sessions.(auth.SessionRevoker); ok && subject != "" {
		_, _ = rev.RevokeSubjectSessions(ctx, subject)
	}
}

// signInLockout reports whether a freshly password-verified user is
// barred from signing in by platform-admin moderation — their own
// account suspended, or their home org suspended — and a user-facing
// reason. Used by the sign-in and TOTP-completion paths so a locked-out
// account fails at the door instead of one request later.
func (h *HTTPGateway) signInLockout(ctx context.Context, u auth.User) (string, bool) {
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

// ---- orgs -----------------------------------------------------------

// platformListOrgs returns every org profile with its moderation state.
func (h *HTTPGateway) platformListOrgs(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *HTTPGateway) toPlatformOrgDTO(ctx context.Context, pr auth.OrgProfile) platformOrgDTO {
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

// platformGetOrg returns one org plus its member emails.
func (h *HTTPGateway) platformGetOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
		// No profile row is normal for a never-renamed org — synthesize a
		// minimal active profile so the detail page still resolves.
		pr = auth.OrgProfile{Tenant: tenant, DisplayName: tenant, Status: auth.StatusActive}
	}
	resp := map[string]any{"org": h.toPlatformOrgDTO(r.Context(), pr)}
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

// platformSuspendOrg halts an org: scheduled and triggered flows stop
// firing (SubmitGraph refuses) and every member is locked out at auth.
// Member sessions are revoked for immediate effect.
func (h *HTTPGateway) platformSuspendOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setOrgSuspended(rw, r, p, true, false)
}

// platformUnsuspendOrg reverses an org suspension.
func (h *HTTPGateway) platformUnsuspendOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setOrgSuspended(rw, r, p, false, false)
}

// platformBanOrg suspends the org and blocklists every current member's
// email so they can't re-register fresh accounts.
func (h *HTTPGateway) platformBanOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setOrgSuspended(rw, r, p, true, true)
}

func (h *HTTPGateway) setOrgSuspended(rw http.ResponseWriter, r *http.Request, p core.Principal, suspend, ban bool) {
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

// revokeOrgMemberSessions kills the live sessions of every member of a
// tenant, so an org suspension boots them immediately. Best-effort.
func (h *HTTPGateway) revokeOrgMemberSessions(ctx context.Context, tenant string) {
	for _, email := range h.orgMemberEmails(ctx, tenant) {
		if u, err := h.Users.GetByEmail(ctx, email); err == nil {
			h.revokeSubjectSessions(ctx, u.Subject)
		}
	}
}

// banOrgMembers blocklists every member email of a banned org.
func (h *HTTPGateway) banOrgMembers(ctx context.Context, p core.Principal, tenant, reason string) {
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

// orgMemberEmails collects the distinct member emails of a tenant: the
// explicit memberships plus any users whose home org is this tenant.
func (h *HTTPGateway) orgMemberEmails(ctx context.Context, tenant string) []string {
	seen := map[string]bool{}
	if h.Memberships != nil {
		if rows, err := h.Memberships.ListByTenant(ctx, tenant); err == nil {
			for _, m := range rows {
				seen[strings.ToLower(m.UserEmail)] = true
			}
		}
	}
	// Home-org owners aren't always in the memberships table (a personal
	// org's owner, e.g.), so sweep users for a matching home tenant.
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

// ---- drops (killswitch) --------------------------------------------

// platformListDrops returns the full drop catalog with each drop's
// killswitch state (global + per-tenant). Unlike the build-time palette
// (ListDrops), this includes drops that are switched off.
func (h *HTTPGateway) platformListDrops(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.DropSwitches == nil {
		writeJSONError(rw, http.StatusNotImplemented, "drop killswitch not configured")
		return
	}
	mp, ok := h.svc.Engine.Resolver.(interface {
		Manifests() map[string]core.Manifest
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
	// Index switches by drop id.
	global := map[string]string{}      // id -> reason
	perTenant := map[string][]string{} // id -> tenants
	for _, sw := range switches {
		if sw.Tenant == "" {
			global[sw.DropID] = sw.Reason
		} else {
			perTenant[sw.DropID] = append(perTenant[sw.DropID], sw.Tenant)
		}
	}
	manifests := mp.Manifests()
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
			Reason:           reason,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(rw, http.StatusOK, map[string]any{"drops": out})
}

// platformDisableDrop switches a drop off, globally (no tenant in body)
// or for a single org. The engine resolver refuses it on the next run.
func (h *HTTPGateway) platformDisableDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.DropSwitches == nil {
		writeJSONError(rw, http.StatusNotImplemented, "drop killswitch not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(rw, http.StatusBadRequest, "drop id required")
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

// platformEnableDrop clears a drop switch (global or per-tenant).
func (h *HTTPGateway) platformEnableDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.DropSwitches == nil {
		writeJSONError(rw, http.StatusNotImplemented, "drop killswitch not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(rw, http.StatusBadRequest, "drop id required")
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
