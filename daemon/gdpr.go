// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

// roleRevoker is the shape the platform-admin and support-agent grant stores
// share: drop the subject's own grant, then pseudonymise them where they appear
// as the granter of somebody else's.
type roleRevoker interface {
	Revoke(ctx context.Context, email string) error
	AnonymizeGrantedBy(ctx context.Context, email string) (int, error)
}

// subjectAnonymizer is the support stores' erasure surface. Support rows are ORG
// records that happen to carry a person's identity, so a departing member is
// scrubbed OUT of them rather than taking them along — see
// support.PgTicketStore.AnonymizeSubject.
type subjectAnonymizer interface {
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
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
	Shares         int      `json:"shares_deleted"`
	Secrets        int      `json:"secrets_deleted"`
	MCPServers     int      `json:"mcp_servers_deleted"`
	WebAPIs        int      `json:"web_apis_deleted"`
	Runners        int      `json:"runners_deleted"`
	RunnerTasks    int      `json:"runner_tasks_deleted"`
	GitMirrors     int      `json:"git_mirrors_deleted"`
	DropSwitches   int      `json:"drop_switches_deleted"`
	Plans          int      `json:"billing_rows_deleted"`
	Entitlements   int      `json:"entitlements_deleted"`
	UsageCounters  int      `json:"usage_counters_deleted"`
	RoleGrants     int      `json:"role_grants_revoked"`
	GrantedByRefs  int      `json:"granted_by_refs_anonymized"`
	AuthoredRefs   int      `json:"authored_refs_anonymized"`
	BusEvents      int      `json:"bus_events_deleted"`
	Tickets        int      `json:"support_tickets_deleted"`
	Bundles        int      `json:"support_bundles_deleted"`
	Grants         int      `json:"access_grants_deleted"`
	WorkspaceWiped bool     `json:"workspace_wiped,omitempty"`
	SandboxWiped   bool     `json:"sandbox_wiped,omitempty"`
	OrgAuthDeleted bool     `json:"org_auth_deleted,omitempty"`
	OrgProfileGone bool     `json:"org_profile_deleted,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

func (r *EraseReport) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// eraseStep runs one count-returning erasure op and records the outcome:
// on error it appends a "name: <err>" warning; on success it hands the
// count to set. It does NOT do the capability assertion (the caller has
// already narrowed the store) — it just removes the repeated
// error-or-assign branch the cascade does for every store.
func (rep *EraseReport) eraseStep(name string, op func() (int, error), set func(int)) {
	if n, err := op(); err != nil {
		rep.warnf("%s: %v", name, err)
	} else {
		set(n)
	}
}

// tallyByTenant runs the DeleteByTenant erasure for one store IF it
// satisfies tenantEraser, recording the warning/count via eraseStep. The
// many tenant-scoped steps in deleteOrgData share this exact shape (assert
// → delete → warn-or-assign); a store that doesn't implement the
// capability is silently skipped, matching the original per-step `ok`
// guard.
func (rep *EraseReport) tallyByTenant(ctx context.Context, name string, store any, tenant string, set func(int)) {
	if e, ok := store.(tenantEraser); ok {
		rep.eraseStep(name, func() (int, error) { return e.DeleteByTenant(ctx, tenant) }, set)
	}
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
		rep.eraseStep("sessions", func() (int, error) { return rev.RevokeSubjectSessions(ctx, u.Subject) },
			func(n int) { rep.Sessions = n })
	}
	// API keys issued to this subject (across tenants).
	if ks, ok := h.svc.AdminKeys.(subjectEraser); ok {
		rep.eraseStep("api_keys", func() (int, error) { return ks.DeleteBySubject(ctx, u.Subject) },
			func(n int) { rep.APIKeys = n })
	} else if h.svc.AdminKeys != nil {
		rep.warnf("api_keys: store does not support subject deletion; revoke manually")
	}
	// Memberships in every org.
	if ms, ok := h.Memberships.(emailEraser); ok {
		rep.eraseStep("memberships", func() (int, error) { return ms.DeleteByEmail(ctx, email) },
			func(n int) { rep.Memberships = n })
	}
	// Pending invitations addressed to this email.
	if inv, ok := h.Invitations.(emailEraser); ok {
		rep.eraseStep("invitations", func() (int, error) { return inv.DeleteByEmail(ctx, email) },
			func(n int) { rep.Invitations = n })
	}
	// Audit: pseudonymise rather than delete — keep the security trail
	// (what/when/where) without the identifier. The actor may have been
	// recorded as the subject or the email, so scrub both.
	if an, ok := h.Audit.(actorAnonymizer); ok {
		for _, actor := range dedupeNonEmpty(u.Subject, email) {
			rep.eraseStep("audit", func() (int, error) { return an.AnonymizeActor(ctx, actor) },
				func(n int) { rep.AuditEvents += n })
		}
	}
	// Support history: a ticket thread belongs to the ORG (it documents a
	// problem with the org's flows and may involve several people), so erasing
	// this member must not delete it — but it must not leave their email in
	// created_by / assigned_to / message author either, nor the messages they
	// wrote. Same treatment as the audit trail above: pseudonymise, don't
	// delete. Skipping this was the gap that made an erase report read as
	// complete while the address stayed in support_tickets and access_grants.
	//
	// Both identifiers are scrubbed because these columns are written from
	// whichever the calling path had: the ticket routes store the email, the
	// grant store the agent's subject.
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

		// Every other store that records WHO did something. These rows belong
		// to an org, not to the subject, so they survive this erasure — and
		// used to survive it still carrying the person's address in
		// created_by / updated_by / invited_by / disabled_by.
		//
		// Deleting the org takes these rows outright, so this path only matters
		// on the one it does not cover: a member of a SHARED org erases their
		// account and the org carries on. That is also the most common erasure
		// there is, which is what made the residue worth closing.
		//
		// nil-safe: a store the deployment does not run is a nil interface and
		// fails the assertion, so it is skipped rather than warned about.
		for name, store := range h.authorshipStores() {
			if a, ok := store.(subjectAnonymizer); ok {
				rep.eraseStep(name, func() (int, error) { return a.AnonymizeSubject(ctx, ident) },
					func(n int) { rep.AuthoredRefs += n })
			}
		}
	}
	// Platform-admin and support-agent grants. Two things per store: drop the
	// subject's own grant row (their email is the primary key), and scrub them
	// out of anyone else's granted_by. Skipping this left a deleted person's
	// address as a live role holder AND as the granter on every role they had
	// handed out.
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
	// The env allowlist ($DAZYFLOW_PLATFORM_ADMINS) is the other half of
	// platform-admin status, and it is deployment config this process cannot
	// rewrite. Erasing the account while the address still sits in that list
	// would silently re-elevate it if the person ever signed up again, so say
	// so rather than reporting a clean erasure.
	for _, a := range h.PlatformAdmins {
		if strings.EqualFold(strings.TrimSpace(a), email) {
			rep.warnf("platform_admins: %q is in the $DAZYFLOW_PLATFORM_ADMINS env allowlist, "+
				"which this process cannot edit — remove it from the deployment config", email)
		}
	}
	// The blocklist keeps any entry that BANS this person: a ban liftable by
	// asking to be forgotten is not a ban, and the entry stands on legitimate
	// interest (Art. 17(1)(c)). Only their email as the blocking ADMIN is
	// scrubbed.
	if bl, ok := h.Blocklist.(interface {
		AnonymizeCreatedBy(ctx context.Context, email string) (int, error)
	}); ok {
		rep.eraseStep("blocklist_created_by",
			func() (int, error) { return bl.AnonymizeCreatedBy(ctx, email) },
			func(n int) { rep.GrantedByRefs += n })
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
	rep.tallyByTenant(ctx, "jobs", h.svc.Jobs, tenant, func(n int) { rep.Jobs = n })
	// Run logs and spooled bus events reference jobs, so delete them
	// while the job rows are still present to scope the join.
	rep.tallyByTenant(ctx, "run_logs", h.svc.RunLogs, tenant, func(n int) { rep.RunLogs = n })
	rep.tallyByTenant(ctx, "bus_events", h.svc.Bus, tenant, func(n int) { rep.BusEvents = n })
	// API keys, memberships, invitations scoped to the tenant.
	rep.tallyByTenant(ctx, "api_keys", h.svc.AdminKeys, tenant, func(n int) { rep.APIKeys = n })
	rep.tallyByTenant(ctx, "memberships", h.Memberships, tenant, func(n int) { rep.Memberships = n })
	rep.tallyByTenant(ctx, "invitations", h.Invitations, tenant, func(n int) { rep.Invitations = n })
	// Public overview share links for the tenant's workspaces.
	rep.tallyByTenant(ctx, "shares", h.svc.Shares, tenant, func(n int) { rep.Shares = n })
	// Support surface: the org's tickets (customer-written chat), the redacted
	// diagnostic bundles built from its flows, and any access grants naming it.
	// Nil-safe via tallyByTenant's capability probe, so a deployment with the
	// support feature off just records nothing here.
	rep.tallyByTenant(ctx, "support_tickets", h.Tickets, tenant, func(n int) { rep.Tickets = n })
	rep.tallyByTenant(ctx, "support_bundles", h.Bundles, tenant, func(n int) { rep.Bundles = n })
	rep.tallyByTenant(ctx, "access_grants", h.Grants, tenant, func(n int) { rep.Grants = n })
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
	// Every secret the tenant holds, plus its wrapped DEK. Deliberately
	// AFTER DeleteOrgAuth: that path deletes the one SSO client secret by
	// name, and letting it run first keeps its own erasure honest instead of
	// silently no-opping against rows this sweep already took.
	//
	// Until this ran, org deletion left the whole encrypted_secrets table
	// intact — connector credentials and OAuth tokens for a deleted org,
	// still decryptable, because the DEK stayed behind with them.
	if secrets := h.secretsStoreForErase(); secrets != nil {
		rep.eraseStep("secrets", func() (int, error) { return secrets.DeleteByTenant(ctx, tenant) },
			func(n int) { rep.Secrets = n })
	}
	// Tenant-scoped integration config. Each of these arrived after the
	// original cascade was written and had to be added here; the table-coverage
	// test in gdpr_coverage_test.go exists so the next one cannot be forgotten.
	//
	// MCPServers/WebAPIs/Runners are the wrapper types rather than their bare
	// stores, because each has in-process state — a live catalog, a credential
	// index — that a raw row delete would leave behind.
	if h.MCPServers != nil {
		rep.tallyByTenant(ctx, "mcp_servers", h.MCPServers, tenant, func(n int) { rep.MCPServers = n })
	}
	if h.WebAPIs != nil {
		rep.tallyByTenant(ctx, "web_apis", h.WebAPIs, tenant, func(n int) { rep.WebAPIs = n })
	}
	if h.Runners != nil {
		rep.tallyByTenant(ctx, "runners", h.Runners, tenant, func(n int) { rep.Runners = n })
	}
	// Queued/running tasks carry the org's script, env and stdin.
	rep.tallyByTenant(ctx, "runner_tasks", h.RunnerTasks, tenant, func(n int) { rep.RunnerTasks = n })
	rep.tallyByTenant(ctx, "git_mirrors", h.GitMirrors, tenant, func(n int) { rep.GitMirrors = n })
	rep.tallyByTenant(ctx, "drop_switches", h.DropSwitches, tenant, func(n int) { rep.DropSwitches = n })

	// Billing, entitlements and usage. The plan row is erased with the org, but
	// a subscription that is still billing outlives it in Stripe — and once
	// this row is gone nothing here can map that subscription back. Warn while
	// the pointer is still readable, so the operator knows to cancel it.
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

	// The tenant is gone, so there is no security trail to preserve —
	// hard-delete its audit events.
	rep.tallyByTenant(ctx, "audit", h.Audit, tenant, func(n int) { rep.AuditEvents = n })
	return rep, nil
}

// authorshipStores are the stores holding a "who did this" column on rows owned
// by an org: created_by, updated_by, invited_by, disabled_by.
//
// Collected in one place so the erasure loop is a loop rather than a dozen
// near-identical blocks, and so the set is greppable next to the coverage test
// that enumerates the same columns (gdpr_coverage_test.go).
func (h *HTTPGateway) authorshipStores() map[string]any {
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
	// The wrapper types hold the store behind a nil-able pointer, so unwrap
	// them rather than asserting on a non-nil interface wrapping a nil value.
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
	}
	return m
}

// secretsStoreForErase returns the encrypted secret store, or nil when the
// deployment has none (no master key configured).
//
// It checks both homes the store has. cmd/dzd wires the same *EncryptedSecrets
// into the gateway and the service as separate assignments, and handlers in
// this package read whichever one their author reached for. On an erasure path
// that split is a hazard rather than a style question: reading only one field
// turns a half-wired deployment into a silent skip, and a silent skip here is
// undeleted credentials that the report still calls erased.
func (h *HTTPGateway) secretsStoreForErase() *EncryptedSecrets {
	if h.EncryptedSecrets != nil {
		return h.EncryptedSecrets
	}
	if h.svc != nil {
		return h.svc.EncryptedSecrets
	}
	return nil
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
