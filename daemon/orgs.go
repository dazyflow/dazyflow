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
	// Same rationale as updateMemberRoles: removal takes effect now, not
	// whenever the removed member's session happens to expire.
	h.revokeMemberSessions(r.Context(), email)
	rw.WriteHeader(http.StatusNoContent)
}

// revokeMemberSessions force-signs-out the user behind email (all their
// sessions, every org — a session carries one role set, and re-signing
// in rebuilds it from the current memberships). Best-effort: a failed
// sweep logs and moves on; sessions also expire on their own TTL.
func (h *HTTPGateway) revokeMemberSessions(ctx context.Context, email string) {
	if h.Users == nil || h.Sessions == nil {
		return
	}
	rev, ok := h.Sessions.(auth.SessionRevoker)
	if !ok {
		// Every in-tree store implements SessionRevoker; a custom one
		// that doesn't leaves demoted members holding stale roles until
		// session expiry — say so instead of degrading silently.
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

// resolveCatalogRoles fills in permissions for name-only roles from the
// canonical team catalog (core.TeamRoleViewer/Editor/Admin), so clients
// send {"name":"viewer"} and the grant is always the server's CURRENT
// definition — the TS mirror can't drift. Roles carrying explicit
// permissions pass through as custom; a name-only role outside the
// catalog is a mistake, not an empty grant.
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

// capRolesToCaller rejects roles whose permissions exceed the caller's
// own. Mirrors the IssueAPIKey / IssueOwnAPIKey guards: only a platform
// admin may hand out the cross-tenant super-admin role, and a tenant
// admin can only delegate permissions they themselves hold. Without
// this an org admin could grant (or self-grant) a membership carrying
// platform:admin and, after switchOrg copies the membership roles into
// the session, break out of their own tenant. Shared by createInvitation
// and updateMemberRoles so the two grant paths can't drift.
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

// updateMemberRoles changes an existing member's roles in place —
// PATCH /api/v1/admin/members/{email} with {"roles":[...]}. The home
// owner can't be edited here (their roles live on the User record, and
// the org must always keep its owner-admin), and roles can't be emptied
// (removing access is DELETE's job). Role grants are capped to the
// caller's own permissions, same as invitations.
func (h *HTTPGateway) updateMemberRoles(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	var body struct {
		Roles []core.Role `json:"roles"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
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
	// Guard: the home owner's roles aren't membership-backed.
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
	m.Roles = body.Roles
	if err := h.Memberships.PutMembership(r.Context(), m); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Demotion takes effect NOW, not at the member's next sign-in: kill
	// their live sessions so the next request re-authenticates against
	// the new roles. Best-effort — the role change above is the truth.
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

// createInvitation handler. Body: {email, roles, workspace}. Mints
// a token, stores a pending Invitation, and returns the token + accept
// URL. When the operator wired a mailer the link is also emailed; the
// response always carries the URL so the admin can copy/paste it into
// their channel of choice either way.
func (h *HTTPGateway) createInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if !core.CanAdminOrg(p) {
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
	// Resolve catalog names to the server's role definitions, then cap
	// the grant to the caller's own permissions (see capRolesToCaller).
	// Only explicitly-requested roles are checked; the default editor
	// role below is a trusted server-side grant.
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
		// Default to the editor role so the invited person can do
		// graph work without needing a second admin action. The
		// inviter can override with body.Roles.
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
	// Deliver the invite by email when the operator wired a mailer AND
	// the accept URL is absolute (no public base URL = a path-only link
	// that's useless in an inbox). Best-effort: the response always
	// carries the link for copy/paste, emailed or not.
	emailSent := false
	if acceptURL := h.inviteURL(token); h.svc.Mailer != nil && strings.HasPrefix(acceptURL, "http") {
		body := fmt.Sprintf(
			"%s invited you to join their organization on Hazyflow.\n\n"+
				"Accept the invitation:\n%s\n\n"+
				"The link expires %s. If you weren't expecting this, ignore this email.",
			p.Subject, acceptURL, inv.ExpiresAt.Format("2006-01-02"))
		if err := h.svc.Mailer.Send(r.Context(), email, "You're invited to Hazyflow", body); err != nil {
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

func (h *HTTPGateway) revokeInvitation(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *HTTPGateway) putOrgAuthConfig(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OrgAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org SSO config not configured")
		return
	}
	if !core.CanAdminOrg(p) {
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
	if !core.CanAdminOrg(p) {
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
func (h *HTTPGateway) getPublicAuthConfig(rw http.ResponseWriter, r *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"signup_enabled": h.EnableSignup,
		// admin_bootstrap keeps the sign-up page reachable on a
		// signup-disabled deployment while a platform-admin email is
		// still unclaimed, so the first super-admin can bootstrap
		// without flipping EnableSignup on. It self-clears once every
		// listed admin has an account. See adminBootstrapAvailable.
		"admin_bootstrap": h.adminBootstrapAvailable(r.Context()),
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
