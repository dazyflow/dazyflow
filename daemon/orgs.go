package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
)

// switchOrg re-issues the caller's session against a tenant they
// belong to. The session token itself doesn't change — we update the
// server-side Session record in place, so the same cookie/Bearer keeps
// working. The browser then refetches whoami to pick up the new
// tenant/workspace/roles.
//
// Eligibility: the target tenant must be either (a) the caller's home
// tenant (i.e. p.Tenant on the User record) or (b) one of their
// Memberships. We resolve the user's home tenant by looking up their
// email in Users — that's the source of truth for "the org you got
// at signup".
func (h *HTTPGateway) switchOrg(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Sessions == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "sessions/users not configured")
		return
	}
	var body struct {
		Tenant string `json:"tenant"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	target := strings.TrimSpace(body.Tenant)
	if target == "" {
		writeJSONError(rw, http.StatusBadRequest, "tenant required")
		return
	}
	if target == p.Tenant {
		// No-op: already on this org. Returning OK so the UI can call
		// this unconditionally on "click your current org" without
		// special-casing.
		writeJSON(rw, http.StatusOK, map[string]any{
			"tenant":    p.Tenant,
			"workspace": p.Workspace,
			"roles":     p.Roles,
		})
		return
	}
	// Look up the user's home org + scan memberships.
	user, err := h.Users.GetByEmail(r.Context(), p.Subject)
	if err != nil {
		// API-key principals get here too — they have no User record so
		// switching is a no-op for them. Fail loudly so the UI doesn't
		// confuse the user.
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
	// Re-issue under the same token. The cookie keeps working.
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

// listMembers returns one row per person with access to the principal's
// org: the home user (the org owner) plus each Membership. Used by the
// admin Members page. Tenant scope is the principal's own; platform
// admins can pass ?tenant= to inspect another org.
func (h *HTTPGateway) listMembers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Memberships == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "memberships not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
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
	// Owner: the user whose home tenant this is. Scan the user list to
	// find them — JSONUserStore is small enough that O(n) is fine; the
	// Pg variant should grow a "first owner of tenant" query in time.
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

// removeMember deletes a Membership. The home owner can't be removed —
// that would leave an org without a primary contact. To "transfer" the
// org, a separate (TODO) endpoint would re-stamp Users.Tenant; for now
// removing the home owner returns 409.
func (h *HTTPGateway) removeMember(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Memberships == nil {
		writeJSONError(rw, http.StatusNotImplemented, "memberships not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
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
	// Guard: the home owner stays put.
	if h.Users != nil {
		if u, err := h.Users.GetByEmail(r.Context(), email); err == nil && u.Tenant == tenant {
			writeJSONError(rw, http.StatusConflict, "cannot remove the org owner")
			return
		}
	}
	if err := h.Memberships.DeleteMembership(r.Context(), email, tenant); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// createInvitation handler. Body: {email, roles, workspace}. Mints
// a token, stores a pending Invitation, returns the token + accept
// URL. SMTP delivery is a follow-up; for now the response carries
// the accept URL so the admin can copy/paste it into their channel
// of choice.
func (h *HTTPGateway) createInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	var body struct {
		Email     string      `json:"email"`
		Roles     []core.Role `json:"roles"`
		Workspace string      `json:"workspace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if err := validSignupEmail(email); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Roles) == 0 {
		// Default to the editor role so the invited person can do
		// graph work without needing a second admin action. The
		// inviter can override with body.Roles.
		body.Roles = []core.Role{{
			Name: "editor",
			Permissions: []core.Permission{
				core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
				core.PermSecretRead, core.PermSecretWrite,
			},
		}}
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
	writeJSON(rw, http.StatusCreated, map[string]any{
		"token":      token,
		"email":      email,
		"tenant":     inv.Tenant,
		"workspace":  inv.Workspace,
		"roles":      inv.Roles,
		"expires_at": inv.ExpiresAt,
		"accept_url": h.inviteURL(token),
	})
}

func (h *HTTPGateway) inviteURL(token string) string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		// Falls back to a path-only URL when the operator hasn't set
		// --public-base-url. The UI will rewrite it against window.origin
		// before showing it to the admin.
		return "/invite/" + token
	}
	return base + "/invite/" + token
}

// listInvitations returns the tenant's pending + recently-resolved
// invitations. The admin Invitations page renders this.
func (h *HTTPGateway) listInvitations(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
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

func (h *HTTPGateway) revokeInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
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

// viewInvitation: no auth required — the token IS the credential at
// this step. Returns just enough for the /invite landing page to
// render the org name and the email it was sent to.
func (h *HTTPGateway) viewInvitation(rw http.ResponseWriter, r *http.Request) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	token := r.PathValue("token")
	inv, err := h.Invitations.GetByToken(r.Context(), token)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, "invitation not found")
		return
	}
	now := time.Now().UTC()
	// Show the org's display name on the invite landing — "you've been
	// invited to Acme" beats "to usr_de3d2365". The name isn't
	// sensitive (it's the marketing-facing label, not a credential),
	// so exposing it on the unauthenticated detail endpoint is
	// acceptable. Falls back to the tenant ID when no profile is set.
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

// acceptInvitation requires the caller to be signed in. We bind the
// invitation's tenant + roles to the caller's email by creating a
// Membership, then mark the invitation accepted.
func (h *HTTPGateway) acceptInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	if err != nil {
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
	if err := h.Memberships.PutMembership(r.Context(), m); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
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

// getOrgAuthConfig returns the per-org SSO config minus the secret. The
// secret round-trips only on PUT (and is write-only after that — the
// admin re-pastes it to change it).
func (h *HTTPGateway) getOrgAuthConfig(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
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

func (h *HTTPGateway) putOrgAuthConfig(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	var body struct {
		GoogleClientID        string `json:"google_client_id"`
		GoogleClientSecret    string `json:"google_client_secret"`
		GoogleWorkspaceDomain string `json:"google_workspace_domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	cfg := auth.OrgAuthConfig{
		Tenant:                p.Tenant,
		GoogleClientID:        strings.TrimSpace(body.GoogleClientID),
		GoogleClientSecret:    strings.TrimSpace(body.GoogleClientSecret),
		GoogleWorkspaceDomain: strings.TrimSpace(body.GoogleWorkspaceDomain),
		UpdatedAt:             time.Now().UTC(),
	}
	// Allow blank secret to mean "keep the existing one" so re-saving
	// other fields doesn't force the admin to re-paste the secret.
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

func (h *HTTPGateway) deleteOrgAuthConfig(rw http.ResponseWriter, _ *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !p.Has(core.PermOrganizationAdmin) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
		return
	}
	if err := h.OrgAuth.DeleteOrgAuth(context.Background(), p.Tenant); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(context.Background(), p, "org_auth.delete", p.Tenant, "")
	rw.WriteHeader(http.StatusNoContent)
}

// getPublicAuthConfig surfaces deployment-level auth toggles the
// sign-in page needs to render correctly (currently just whether
// self-serve signup is enabled). Unauthenticated — the response holds
// no secrets, just feature flags.
func (h *HTTPGateway) getPublicAuthConfig(rw http.ResponseWriter, _ *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"signup_enabled": h.EnableSignup,
		// wildcard_domain, when set, lets the sign-in page derive the
		// target org from a "<org>.<domain>" host so a visit to
		// acme.hazyflow.app preselects org=acme. Empty = feature off.
		"wildcard_domain": h.WildcardDomain,
	})
}

// getPublicSSOStatus is the unauthenticated lookup the sign-in page
// uses to decide whether to show a "Sign in with Google" button for
// a given org. We expose only the booleans; secrets stay server-side.
func (h *HTTPGateway) getPublicSSOStatus(rw http.ResponseWriter, r *http.Request) {
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
