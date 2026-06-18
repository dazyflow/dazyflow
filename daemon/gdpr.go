package daemon

import (
	"context"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/auth"
)

// GDPR data-subject-rights plumbing: account erasure (Art. 17) and org
// deletion, plus the data export assembled in gdpr_export.go.
//
// The cascade reaches stores that live behind several interfaces, not all
// of which declare a delete method (the shared interfaces stay minimal).
// So rather than widen every interface, each step type-asserts the
// configured store to a narrow capability interface and skips — recording
// a warning — when a store doesn't support it. This mirrors how session
// revocation already probes for auth.SessionRevoker, and means a partial
// deployment (e.g. no audit log) erases what it can instead of failing.

// Narrow capability interfaces the concrete stores satisfy. Several share
// the DeleteByTenant shape, so one interface serves jobs, run logs, bus
// events, api-keys, memberships and invitations alike.
type tenantEraser interface {
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
}
type subjectEraser interface {
	DeleteBySubject(ctx context.Context, subject string) (int, error)
}
type emailEraser interface {
	DeleteByEmail(ctx context.Context, email string) (int, error)
}
type userDeleter interface {
	DeleteUser(ctx context.Context, email string) error
}
type orgProfileDeleter interface {
	DeleteOrgProfile(ctx context.Context, tenant string) error
}
type actorAnonymizer interface {
	AnonymizeActor(ctx context.Context, actor string) (int, error)
}
type tenantDirRemover interface {
	RemoveTenant(tenant string) error
}

// EraseReport tallies what an erasure removed. Returned to the caller and
// folded into the audit detail so the action is itself accountable.
type EraseReport struct {
	Email          string   `json:"email,omitempty"`
	Subject        string   `json:"subject,omitempty"`
	Tenant         string   `json:"tenant,omitempty"`
	UserDeleted    bool     `json:"user_deleted,omitempty"`
	Sessions       int      `json:"sessions_revoked"`
	APIKeys        int      `json:"api_keys_deleted"`
	Memberships    int      `json:"memberships_deleted"`
	Invitations    int      `json:"invitations_deleted"`
	AuditEvents    int      `json:"audit_events"`
	Jobs           int      `json:"jobs_deleted"`
	RunLogs        int      `json:"run_logs_deleted"`
	BusEvents      int      `json:"bus_events_deleted"`
	WorkspaceWiped bool     `json:"workspace_wiped,omitempty"`
	SandboxWiped   bool     `json:"sandbox_wiped,omitempty"`
	OrgAuthDeleted bool     `json:"org_auth_deleted,omitempty"`
	OrgProfileGone bool     `json:"org_profile_deleted,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

func (r *EraseReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// eraseUserIdentity removes a data subject's identity-level personal data:
// sessions, API keys, org memberships, pending invitations, the user row,
// and (pseudonymising, not deleting) their actor in the audit trail. It
// does NOT remove org-level content (graphs/runs/logs) — that is org data,
// erased via deleteOrgData when the org itself is being removed (the
// account-deletion handler composes both for a personal org).
func (h *HTTPGateway) eraseUserIdentity(ctx context.Context, email string) (EraseReport, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	rep := EraseReport{Email: email}
	if h.Users == nil {
		return rep, fmt.Errorf("user store not configured")
	}
	u, err := h.Users.GetByEmail(ctx, email)
	if err != nil {
		return rep, fmt.Errorf("unknown user %q: %w", email, err)
	}
	rep.Subject = u.Subject
	rep.Tenant = u.Tenant

	// Sessions: cut access first so an erased account can't keep acting
	// mid-cascade.
	if rev, ok := h.Sessions.(auth.SessionRevoker); ok {
		if n, err := rev.RevokeSubjectSessions(ctx, u.Subject); err != nil {
			rep.warnf("sessions: %v", err)
		} else {
			rep.Sessions = n
		}
	}
	// API keys issued to this subject (across tenants).
	if ks, ok := h.svc.AdminKeys.(subjectEraser); ok {
		if n, err := ks.DeleteBySubject(ctx, u.Subject); err != nil {
			rep.warnf("api_keys: %v", err)
		} else {
			rep.APIKeys = n
		}
	} else if h.svc.AdminKeys != nil {
		rep.warnf("api_keys: store does not support subject deletion; revoke manually")
	}
	// Memberships in every org.
	if ms, ok := h.Memberships.(emailEraser); ok {
		if n, err := ms.DeleteByEmail(ctx, email); err != nil {
			rep.warnf("memberships: %v", err)
		} else {
			rep.Memberships = n
		}
	}
	// Pending invitations addressed to this email.
	if inv, ok := h.Invitations.(emailEraser); ok {
		if n, err := inv.DeleteByEmail(ctx, email); err != nil {
			rep.warnf("invitations: %v", err)
		} else {
			rep.Invitations = n
		}
	}
	// Audit: pseudonymise rather than delete — keep the security trail
	// (what/when/where) without the identifier. The actor may have been
	// recorded as the subject or the email, so scrub both.
	if an, ok := h.Audit.(actorAnonymizer); ok {
		for _, actor := range dedupeNonEmpty(u.Subject, email) {
			if n, err := an.AnonymizeActor(ctx, actor); err != nil {
				rep.warnf("audit: %v", err)
			} else {
				rep.AuditEvents += n
			}
		}
	}
	// Finally the user row itself.
	if del, ok := h.Users.(userDeleter); ok {
		if err := del.DeleteUser(ctx, email); err != nil {
			return rep, fmt.Errorf("delete user row: %w", err)
		}
		rep.UserDeleted = true
	} else {
		rep.warnf("user: store does not support deletion; remove the row manually")
	}
	return rep, nil
}

// deleteOrgData removes everything scoped to a tenant: the workspace +
// sandbox directories, all job/run-log/bus-event rows, API keys,
// memberships, invitations, the org's SSO config and profile, and the
// tenant's audit trail. Used for org/tenant deletion and, composed with
// eraseUserIdentity, for erasing a personal org on account deletion.
func (h *HTTPGateway) deleteOrgData(ctx context.Context, tenant string) (EraseReport, error) {
	tenant = strings.TrimSpace(tenant)
	rep := EraseReport{Tenant: tenant}
	if tenant == "" {
		return rep, fmt.Errorf("tenant required")
	}

	// Workspace (git store) + sandbox (scratch/files) directories.
	if wr, ok := h.svc.Workspaces.(tenantDirRemover); ok {
		if err := wr.RemoveTenant(tenant); err != nil {
			rep.warnf("workspace: %v", err)
		} else {
			rep.WorkspaceWiped = true
		}
	}
	if h.svc.Engine != nil {
		if sb, ok := h.svc.Engine.Sandbox.(tenantDirRemover); ok {
			if err := sb.RemoveTenant(tenant); err != nil {
				rep.warnf("sandbox: %v", err)
			} else {
				rep.SandboxWiped = true
			}
		}
	}
	// Job records (run history + payloads/results).
	if js, ok := h.svc.Jobs.(tenantEraser); ok {
		if n, err := js.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("jobs: %v", err)
		} else {
			rep.Jobs = n
		}
	}
	// Run logs and spooled bus events reference jobs, so delete them
	// while the job rows are still present to scope the join.
	if rl, ok := h.svc.RunLogs.(tenantEraser); ok {
		if n, err := rl.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("run_logs: %v", err)
		} else {
			rep.RunLogs = n
		}
	}
	if bus, ok := h.svc.Bus.(tenantEraser); ok {
		if n, err := bus.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("bus_events: %v", err)
		} else {
			rep.BusEvents = n
		}
	}
	// API keys, memberships, invitations scoped to the tenant.
	if ks, ok := h.svc.AdminKeys.(tenantEraser); ok {
		if n, err := ks.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("api_keys: %v", err)
		} else {
			rep.APIKeys = n
		}
	}
	if ms, ok := h.Memberships.(tenantEraser); ok {
		if n, err := ms.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("memberships: %v", err)
		} else {
			rep.Memberships = n
		}
	}
	if inv, ok := h.Invitations.(tenantEraser); ok {
		if n, err := inv.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("invitations: %v", err)
		} else {
			rep.Invitations = n
		}
	}
	// Org SSO config + display profile.
	if h.OrgAuth != nil {
		if err := h.OrgAuth.DeleteOrgAuth(ctx, tenant); err != nil {
			rep.warnf("org_auth: %v", err)
		} else {
			rep.OrgAuthDeleted = true
		}
	}
	if pd, ok := h.Profiles.(orgProfileDeleter); ok {
		if err := pd.DeleteOrgProfile(ctx, tenant); err != nil {
			rep.warnf("org_profile: %v", err)
		} else {
			rep.OrgProfileGone = true
		}
	}
	// The tenant is gone, so there is no security trail to preserve —
	// hard-delete its audit events.
	if ae, ok := h.Audit.(tenantEraser); ok {
		if n, err := ae.DeleteByTenant(ctx, tenant); err != nil {
			rep.warnf("audit: %v", err)
		} else {
			rep.AuditEvents = n
		}
	}
	return rep, nil
}

// tenantHasOtherMembers reports whether anyone besides `email` is a member
// of `tenant`. Used to decide whether deleting an account may also wipe its
// home org's data: only when the subject is the org's sole occupant, so we
// never erase a shared org out from under its other members.
func (h *HTTPGateway) tenantHasOtherMembers(ctx context.Context, tenant, email string) bool {
	if h.Memberships == nil {
		return false
	}
	members, err := h.Memberships.ListByTenant(ctx, tenant)
	if err != nil {
		return true // fail safe: assume shared, don't wipe org data
	}
	email = strings.ToLower(strings.TrimSpace(email))
	for _, m := range members {
		if strings.ToLower(m.UserEmail) != email {
			return true
		}
	}
	return false
}

func dedupeNonEmpty(vals ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// looksPersonalTenant reports whether a tenant id is a user's auto-created
// personal org (the `usr_<hex>` shape minted at signup), as opposed to a
// shared org. Account deletion wipes a personal org's data; a shared org is
// left to explicit org deletion.
func looksPersonalTenant(tenant string) bool {
	return strings.HasPrefix(tenant, "usr_")
}
