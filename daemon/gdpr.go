// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
)

// Erasure runs as a CASCADE of independent steps, each recording its own
// outcome, so one store failing does not abort the rest and the report says
// exactly what was and was not erased.

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

type roleRevoker interface {
	Revoke(ctx context.Context, email string) error
	AnonymizeGrantedBy(ctx context.Context, email string) (int, error)
}

type subjectAnonymizer interface {
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
}
type tenantDirRemover interface {
	RemoveTenant(tenant string) error
}

type EraseReport struct {
	Email            string   `json:"email,omitempty"`
	Subject          string   `json:"subject,omitempty"`
	Tenant           string   `json:"tenant,omitempty"`
	UserDeleted      bool     `json:"user_deleted,omitempty"`
	Sessions         int      `json:"sessions_revoked"`
	APIKeys          int      `json:"api_keys_deleted"`
	Memberships      int      `json:"memberships_deleted"`
	Invitations      int      `json:"invitations_deleted"`
	AuditEvents      int      `json:"audit_events"`
	Jobs             int      `json:"jobs_deleted"`
	RunLogs          int      `json:"run_logs_deleted"`
	Shares           int      `json:"shares_deleted"`
	CollectionShares int      `json:"collection_shares_deleted"`
	Secrets          int      `json:"secrets_deleted"`
	MCPServers       int      `json:"mcp_servers_deleted"`
	WebAPIs          int      `json:"web_apis_deleted"`
	Runners          int      `json:"runners_deleted"`
	RunnerTasks      int      `json:"runner_tasks_deleted"`
	GitMirrors       int      `json:"git_mirrors_deleted"`
	DropSwitches     int      `json:"drop_switches_deleted"`
	Plans            int      `json:"billing_rows_deleted"`
	Entitlements     int      `json:"entitlements_deleted"`
	UsageCounters    int      `json:"usage_counters_deleted"`
	RoleGrants       int      `json:"role_grants_revoked"`
	GrantedByRefs    int      `json:"granted_by_refs_anonymized"`
	AuthoredRefs     int      `json:"authored_refs_anonymized"`
	BusEvents        int      `json:"bus_events_deleted"`
	FlowSchedules    int      `json:"flow_schedules_deleted"`
	Tickets          int      `json:"support_tickets_deleted"`
	Bundles          int      `json:"support_bundles_deleted"`
	Grants           int      `json:"access_grants_deleted"`
	WorkspaceWiped   bool     `json:"workspace_wiped,omitempty"`
	SandboxWiped     bool     `json:"sandbox_wiped,omitempty"`
	OrgAuthDeleted   bool     `json:"org_auth_deleted,omitempty"`
	OrgProfileGone   bool     `json:"org_profile_deleted,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

func (r *EraseReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Records the outcome rather than aborting, so the cascade continues.
func (rep *EraseReport) eraseStep(name string, op func() (int, error), set func(int)) {
	if n, err := op(); err != nil {
		rep.warnf("%s: %v", name, err)
	} else {
		set(n)
	}
}

// Probed for the capability rather than required: a store without it is SKIPPED,
// which is the documented fallback and is why the conformance test exists.
func (rep *EraseReport) tallyByTenant(ctx context.Context, name string, store any, tenant string, set func(int)) {
	if e, ok := store.(tenantEraser); ok {
		rep.eraseStep(name, func() (int, error) { return e.DeleteByTenant(ctx, tenant) }, set)
	}
}

func (h *gdprAPI) eraseUserIdentity(ctx context.Context, email string) (EraseReport, error) {
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

	if rev, ok := h.Sessions.(auth.SessionRevoker); ok {
		rep.eraseStep("sessions", func() (int, error) { return rev.RevokeSubjectSessions(ctx, u.Subject) },
			func(n int) { rep.Sessions = n })
	}
	if ks, ok := h.svc.AdminKeys.(subjectEraser); ok {
		rep.eraseStep("api_keys", func() (int, error) { return ks.DeleteBySubject(ctx, u.Subject) },
			func(n int) { rep.APIKeys = n })
	} else if h.svc.AdminKeys != nil {
		rep.warnf("api_keys: store does not support subject deletion; revoke manually")
	}
	if ms, ok := h.Memberships.(emailEraser); ok {
		rep.eraseStep("memberships", func() (int, error) { return ms.DeleteByEmail(ctx, email) },
			func(n int) { rep.Memberships = n })
	}
	if inv, ok := h.Invitations.(emailEraser); ok {
		rep.eraseStep("invitations", func() (int, error) { return inv.DeleteByEmail(ctx, email) },
			func(n int) { rep.Invitations = n })
	}
	// Pseudonymise, not delete: the security trail must survive the person leaving.
	if an, ok := h.Audit.(actorAnonymizer); ok {
		for _, actor := range dedupeNonEmpty(u.Subject, email) {
			rep.eraseStep("audit", func() (int, error) { return an.AnonymizeActor(ctx, actor) },
				func(n int) { rep.AuditEvents += n })
		}
	}
	// A ticket thread belongs to the ORG, so it survives one member leaving; their
	// identifiers and their own words go.
	for _, ident := range dedupeNonEmpty(u.Subject, email) {
		if ts, ok := h.Tickets.(subjectAnonymizer); ok {
			rep.eraseStep("support_tickets", func() (int, error) { return ts.AnonymizeSubject(ctx, ident) },
				func(n int) { rep.Tickets += n })
		} else if h.Tickets != nil {
			rep.warnf("support_tickets: store does not support subject anonymisation; scrub manually")
		}
		if gs, ok := h.Grants.(subjectAnonymizer); ok {
			rep.eraseStep("access_grants", func() (int, error) { return gs.AnonymizeSubject(ctx, ident) },
				func(n int) { rep.Grants += n })
		} else if h.Grants != nil {
			rep.warnf("access_grants: store does not support subject anonymisation; scrub manually")
		}

		for name, store := range h.authorshipStores() {
			if a, ok := store.(subjectAnonymizer); ok {
				rep.eraseStep(name, func() (int, error) { return a.AnonymizeSubject(ctx, ident) },
					func(n int) { rep.AuthoredRefs += n })
			}
		}
	}
	// Both the grant they held and their name as the GRANTER of someone else's.
	for name, store := range map[string]any{
		"platform_admin_grant": h.PlatformAdminGrants,
		"support_agent_grant":  h.SupportAgents,
	} {
		rv, ok := store.(roleRevoker)
		if !ok {
			continue
		}
		if err := rv.Revoke(ctx, email); err != nil {
			rep.warnf("%s: %v", name, err)
		} else {
			rep.RoleGrants++
		}
		rep.eraseStep(name+"_granted_by",
			func() (int, error) { return rv.AnonymizeGrantedBy(ctx, email) },
			func(n int) { rep.GrantedByRefs += n })
	}
	// The env allowlist is the other half, and no code can erase a config file.
	for _, a := range h.PlatformAdmins {
		if strings.EqualFold(strings.TrimSpace(a), email) {
			rep.warnf("platform_admins: %q is in the $DAZYFLOW_PLATFORM_ADMINS env allowlist, "+
				"which this process cannot edit — remove it from the deployment config", email)
		}
	}
	if bl, ok := h.Blocklist.(interface {
		AnonymizeCreatedBy(ctx context.Context, email string) (int, error)
	}); ok {
		rep.eraseStep("blocklist_created_by",
			func() (int, error) { return bl.AnonymizeCreatedBy(ctx, email) },
			func(n int) { rep.GrantedByRefs += n })
	}

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

func (h *gdprAPI) deleteOrgData(ctx context.Context, tenant string) (EraseReport, error) {
	tenant = strings.TrimSpace(tenant)
	rep := EraseReport{Tenant: tenant}
	if tenant == "" {
		return rep, fmt.Errorf("tenant required")
	}

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
	rep.tallyByTenant(ctx, "jobs", h.svc.Jobs, tenant, func(n int) { rep.Jobs = n })
	rep.tallyByTenant(ctx, "flow_schedules", h.svc.Schedules, tenant, func(n int) { rep.FlowSchedules = n })
	rep.tallyByTenant(ctx, "run_logs", h.svc.RunLogs, tenant, func(n int) { rep.RunLogs = n })
	rep.tallyByTenant(ctx, "bus_events", h.svc.Bus, tenant, func(n int) { rep.BusEvents = n })
	rep.tallyByTenant(ctx, "api_keys", h.svc.AdminKeys, tenant, func(n int) { rep.APIKeys = n })
	rep.tallyByTenant(ctx, "memberships", h.Memberships, tenant, func(n int) { rep.Memberships = n })
	rep.tallyByTenant(ctx, "invitations", h.Invitations, tenant, func(n int) { rep.Invitations = n })
	rep.tallyByTenant(ctx, "shares", h.svc.Shares, tenant, func(n int) { rep.Shares = n })
	rep.tallyByTenant(ctx, "collection_shares", h.svc.CollectionShares, tenant,
		func(n int) { rep.CollectionShares = n })
	rep.tallyByTenant(ctx, "support_tickets", h.Tickets, tenant, func(n int) { rep.Tickets = n })
	rep.tallyByTenant(ctx, "support_bundles", h.Bundles, tenant, func(n int) { rep.Bundles = n })
	rep.tallyByTenant(ctx, "access_grants", h.Grants, tenant, func(n int) { rep.Grants = n })
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
	// The DEK goes too, or the rows are unreadable but still present.
	if secrets := h.secretsStoreForErase(); secrets != nil {
		rep.eraseStep("secrets", func() (int, error) { return secrets.DeleteByTenant(ctx, tenant) },
			func(n int) { rep.Secrets = n })
	}
	if h.MCPServers != nil {
		rep.tallyByTenant(ctx, "mcp_servers", h.MCPServers, tenant, func(n int) { rep.MCPServers = n })
	}
	if h.WebAPIs != nil {
		rep.tallyByTenant(ctx, "web_apis", h.WebAPIs, tenant, func(n int) { rep.WebAPIs = n })
	}
	if h.Runners != nil {
		rep.tallyByTenant(ctx, "runners", h.Runners, tenant, func(n int) { rep.Runners = n })
	}
	rep.tallyByTenant(ctx, "runner_tasks", h.RunnerTasks, tenant, func(n int) { rep.RunnerTasks = n })
	rep.tallyByTenant(ctx, "git_mirrors", h.GitMirrors, tenant, func(n int) { rep.GitMirrors = n })
	rep.tallyByTenant(ctx, "drop_switches", h.DropSwitches, tenant, func(n int) { rep.DropSwitches = n })

	if h.svc != nil {
		if h.svc.Plans != nil {
			if plan, err := h.svc.Plans.GetPlan(ctx, tenant); err == nil && liveSubscription(plan) {
				rep.warnf("billing: Stripe subscription %s (%s) is still live — cancel it in Stripe; "+
					"the local mapping to this org is being erased",
					plan.StripeSubscriptionID, plan.SubscriptionStatus)
			}
			rep.tallyByTenant(ctx, "billing", h.svc.Plans, tenant, func(n int) { rep.Plans = n })
		}
		rep.tallyByTenant(ctx, "entitlements", h.svc.Entitlements, tenant, func(n int) { rep.Entitlements = n })
		rep.tallyByTenant(ctx, "usage_counters", h.svc.Usage, tenant, func(n int) { rep.UsageCounters = n })
	}

	rep.tallyByTenant(ctx, "audit", h.Audit, tenant, func(n int) { rep.AuditEvents = n })
	return rep, nil
}

func (h *gdprAPI) authorshipStores() map[string]any {
	m := map[string]any{
		"git_mirrors":     h.GitMirrors,
		"drop_switches":   h.DropSwitches,
		"mcp_servers":     nil,
		"web_apis":        nil,
		"runners":         nil,
		"memberships":     h.Memberships,
		"invitations":     h.Invitations,
		"support_bundles": h.Bundles,
	}
	if h.MCPServers != nil {
		m["mcp_servers"] = h.MCPServers.Store
	}
	if h.WebAPIs != nil {
		m["web_apis"] = h.WebAPIs.Store
	}
	if h.Runners != nil {
		m["runners"] = h.Runners.Store
	}
	if h.svc != nil {
		m["shares"] = h.svc.Shares
		m["collection_shares"] = h.svc.CollectionShares
	}
	return m
}

// Nil when the deployment has none, which is a skip and not a failure.
func (h *gdprAPI) secretsStoreForErase() *EncryptedSecrets {
	if h.EncryptedSecrets != nil {
		return h.EncryptedSecrets
	}
	if h.svc != nil {
		return h.svc.EncryptedSecrets
	}
	return nil
}

// An org with other members must not be deleted along with one person.
func (h *gdprAPI) tenantHasOtherMembers(ctx context.Context, tenant, email string) bool {
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

func looksPersonalTenant(tenant string) bool {
	return strings.HasPrefix(tenant, "usr_")
}
