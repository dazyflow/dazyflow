package daemon

import (
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// HTTP surface for the GDPR data-subject rights: erasure (Art. 17) of an
// account and deletion of an org/tenant. The actual cascade lives in
// gdpr.go; these handlers do auth, a confirmation guard, the audit entry,
// and shape the response.

// deleteMyAccountHandler erases the calling user's own account (self-serve
// Right to erasure). Destructive and irreversible, so it requires an
// explicit confirmation matching the caller's email: `?confirm=<email>`.
// When the user's home org is their personal org and they are its sole
// member, the org's content (flows, runs, logs) is wiped too — a shared
// org is left intact for its other members.
func (h *HTTPGateway) deleteMyAccountHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "user store not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(p.Subject))
	if email == "" {
		writeAPIError(rw, http.StatusBadRequest, "no_subject", "this credential has no associated account")
		return
	}
	if !confirmMatches(r, email) {
		writeAPIError(rw, http.StatusBadRequest, "confirmation_required",
			"permanent deletion — re-send with ?confirm=<your email> to confirm")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "unknown_user", "no such account")
		return
	}
	rep, err := h.eraseUserIdentity(r.Context(), email)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "erase_failed", err.Error())
		return
	}
	if looksPersonalTenant(u.Tenant) && !h.tenantHasOtherMembers(r.Context(), u.Tenant, email) {
		orgRep, _ := h.deleteOrgData(r.Context(), u.Tenant)
		rep = mergeErase(rep, orgRep)
	}
	h.audit(r.Context(), p, "account.delete", email, summarizeErase(rep))
	writeJSON(rw, http.StatusOK, rep)
}

// adminDeleteUserHandler erases another user's account. Platform-admin
// only — org admins can remove a member (DELETE …/admin/members) but not
// erase the person's account globally. Same personal-org cascade as self
// deletion.
func (h *HTTPGateway) adminDeleteUserHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "platform:admin required to erase an account")
		return
	}
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "user store not configured")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "email required")
		return
	}
	if !confirmMatches(r, email) {
		writeAPIError(rw, http.StatusBadRequest, "confirmation_required",
			"permanent deletion — re-send with ?confirm=<email> to confirm")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "unknown_user", "no such account")
		return
	}
	rep, err := h.eraseUserIdentity(r.Context(), email)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "erase_failed", err.Error())
		return
	}
	if looksPersonalTenant(u.Tenant) && !h.tenantHasOtherMembers(r.Context(), u.Tenant, email) {
		orgRep, _ := h.deleteOrgData(r.Context(), u.Tenant)
		rep = mergeErase(rep, orgRep)
	}
	h.audit(r.Context(), p, "admin.account.delete", email, summarizeErase(rep))
	writeJSON(rw, http.StatusOK, rep)
}

// adminDeleteOrgHandler deletes an entire org/tenant and all its content.
// Allowed for a platform admin, or for an org admin acting on their own
// tenant. Member user accounts are NOT erased (they may belong to other
// orgs) — only the org's data and the memberships into it.
func (h *HTTPGateway) adminDeleteOrgHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := strings.TrimSpace(r.PathValue("tenant"))
	if tenant == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "tenant required")
		return
	}
	if !isPlatformAdmin(p) {
		if !core.CanAdminOrg(p) || p.Tenant != tenant {
			writeAPIError(rw, http.StatusForbidden, "forbidden", "organization:admin on this tenant (or platform:admin) required")
			return
		}
	}
	if !confirmMatches(r, tenant) {
		writeAPIError(rw, http.StatusBadRequest, "confirmation_required",
			"permanent deletion — re-send with ?confirm=<tenant> to confirm")
		return
	}
	rep, err := h.deleteOrgData(r.Context(), tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "admin.org.delete", tenant, summarizeErase(rep))
	writeJSON(rw, http.StatusOK, rep)
}

// confirmMatches checks the destructive-action confirmation: the caller
// must echo the target id in ?confirm= (case-insensitive). Cheap guard
// against a fat-fingered or CSRF-style accidental erasure.
func confirmMatches(r *http.Request, target string) bool {
	got := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("confirm")))
	return got != "" && got == strings.ToLower(strings.TrimSpace(target))
}

// mergeErase folds an org-data report into an identity report for the
// combined account+personal-org deletion response.
func mergeErase(a, b EraseReport) EraseReport {
	a.Sessions += b.Sessions
	a.APIKeys += b.APIKeys
	a.Memberships += b.Memberships
	a.Invitations += b.Invitations
	a.AuditEvents += b.AuditEvents
	a.Jobs += b.Jobs
	a.RunLogs += b.RunLogs
	a.BusEvents += b.BusEvents
	a.WorkspaceWiped = a.WorkspaceWiped || b.WorkspaceWiped
	a.SandboxWiped = a.SandboxWiped || b.SandboxWiped
	a.OrgAuthDeleted = a.OrgAuthDeleted || b.OrgAuthDeleted
	a.OrgProfileGone = a.OrgProfileGone || b.OrgProfileGone
	a.Warnings = append(a.Warnings, b.Warnings...)
	return a
}

func summarizeErase(r EraseReport) string {
	return fmt.Sprintf("user=%t sessions=%d keys=%d memberships=%d invitations=%d jobs=%d run_logs=%d bus=%d audit=%d ws=%t sandbox=%t warnings=%d",
		r.UserDeleted, r.Sessions, r.APIKeys, r.Memberships, r.Invitations,
		r.Jobs, r.RunLogs, r.BusEvents, r.AuditEvents, r.WorkspaceWiped, r.SandboxWiped, len(r.Warnings))
}
