// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// Data export — Right of access + portability (GDPR Art. 15/20). Assembles
// a data subject's personal data into one machine-readable JSON document,
// so a user (or an admin on their behalf) gets a complete copy in a single
// call instead of stitching together the piecemeal read APIs.

// subjectLister + invitationLister are the read capabilities the export
// needs beyond the shared store interfaces (which don't declare them).
type subjectLister interface {
	ListBySubject(ctx context.Context, subject string) ([]auth.APIKey, error)
}
type invitationLister interface {
	ListByEmail(ctx context.Context, email string) ([]auth.Invitation, error)
}

// DataExport is the structured, portable copy of one subject's data.
type DataExport struct {
	GeneratedAt string             `json:"generated_at"`
	Profile     exportProfile      `json:"profile"`
	Memberships []exportMembership `json:"memberships"`
	Invitations []exportInvitation `json:"invitations"`
	APIKeys     []APIKeySummary    `json:"api_keys"`
	Flows       []FlowSummary      `json:"flows"`
	Runs        []exportRun        `json:"runs"`
	// SupportTickets is the subject's correspondence with support: threads they
	// opened, with the replies. They wrote the words, so the bodies are theirs
	// under Art. 15 — a support history is the classic DSAR inclusion.
	SupportTickets []exportTicket `json:"support_tickets"`
	// AuditEvents is what this person DID, as recorded about them — including
	// the source IPs kept on auth events.
	AuditEvents []exportAuditEvent `json:"audit_events"`
	// Boards lists the Collections boards in their workspace by name and shape,
	// deliberately without the rows. See assembleExport.
	Boards []exportBoard `json:"boards"`
	// Roles records platform-level roles held, which are personal data about
	// the subject even though no row of "theirs" carries them.
	RoleGrants exportRoleGrants `json:"role_grants"`
	Note       string           `json:"note,omitempty"`
	// Excluded says, in the document itself, what was deliberately left out and
	// why. A DSAR response that silently omits a category is indistinguishable
	// from one that has nothing to report.
	Excluded []string `json:"excluded,omitempty"`
}

type exportTicket struct {
	ID        string            `json:"id"`
	Tenant    string            `json:"tenant"`
	Subject   string            `json:"subject"`
	Status    string            `json:"status"`
	FlowID    string            `json:"flow_id,omitempty"`
	RunID     string            `json:"run_id,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Messages  []exportTicketMsg `json:"messages"`
}

type exportTicketMsg struct {
	Author     string    `json:"author"`
	AuthorKind string    `json:"author_kind"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type exportAuditEvent struct {
	Time   time.Time `json:"time"`
	Tenant string    `json:"tenant"`
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

type exportBoard struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	Rows      int64  `json:"rows"`
}

type exportRoleGrants struct {
	PlatformAdmin bool `json:"platform_admin"`
	SupportAgent  bool `json:"support_agent"`
}

type exportProfile struct {
	Email       string      `json:"email"`
	Subject     string      `json:"subject"`
	Tenant      string      `json:"tenant"`
	Workspace   string      `json:"workspace"`
	Roles       []core.Role `json:"roles"`
	CreatedAt   time.Time   `json:"created_at"`
	VerifiedAt  *time.Time  `json:"verified_at,omitempty"`
	TOTPEnabled bool        `json:"totp_enabled"`
}

type exportMembership struct {
	Tenant    string      `json:"tenant"`
	Workspace string      `json:"workspace"`
	Roles     []core.Role `json:"roles"`
	CreatedAt time.Time   `json:"created_at"`
}

type exportInvitation struct {
	Tenant     string     `json:"tenant"`
	Workspace  string     `json:"workspace"`
	InvitedBy  string     `json:"invited_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type exportRun struct {
	ID      string `json:"id"`
	GraphID string `json:"graph_id"`
	Status  string `json:"status"`
}

const (
	exportRunCap = 1000
	// exportAuditCap bounds the subject's own trail. Audit retention defaults to
	// 90 days, so this is generous for one person's activity in that window.
	exportAuditCap = 5000
	// exportTicketCap bounds how many of the org's tickets are scanned to find
	// the subject's own. Scanning is needed because the store lists by tenant,
	// not by author.
	exportTicketCap = 500
)

// OrgExport is a portable copy of one organization's restorable data — its
// profile, members, and every flow's full graph definition across all its
// workspaces. Offered as the "export first" step before deleting an org so
// the data isn't gone for good.
type OrgExport struct {
	GeneratedAt string            `json:"generated_at"`
	Tenant      string            `json:"tenant"`
	DisplayName string            `json:"display_name,omitempty"`
	Members     []exportOrgMember `json:"members"`
	Flows       []exportOrgFlow   `json:"flows"`
	Note        string            `json:"note,omitempty"`
}

type exportOrgMember struct {
	Email string      `json:"email"`
	Roles []core.Role `json:"roles"`
}

type exportOrgFlow struct {
	Workspace string     `json:"workspace"`
	ID        string     `json:"id"`
	Graph     core.Graph `json:"graph"`
}

// canManageOrg gates the org-scoped admin actions (export, delete): a platform
// admin may act on any org; everyone else only on the org they're an admin of
// AND currently active in (the daemon scopes non-platform principals to one
// tenant, so p.Tenant must equal the target).
func canManageOrg(p core.Principal, tenant string) bool {
	if isPlatformAdmin(p) {
		return true
	}
	return core.CanAdminOrg(p) && p.Tenant == tenant
}

// exportOrgHandler serves an org's full export (GET /admin/orgs/{tenant}/export).
// Read-only; same authorization bar as deleting the org. Offered as the
// export-first step so an admin can keep a copy before the irreversible wipe.
func (h *HTTPGateway) exportOrgHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := strings.TrimSpace(r.PathValue("tenant"))
	if tenant == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "tenant required")
		return
	}
	if !canManageOrg(p, tenant) {
		writeAPIError(rw, http.StatusForbidden, "forbidden",
			"organization:admin on this tenant (or platform:admin) required")
		return
	}
	exp := h.assembleOrgExport(r.Context(), tenant)
	h.audit(r.Context(), p, "org.export", tenant, "organization data export")
	rw.Header().Set("Content-Disposition",
		`attachment; filename="dazyflow-org-`+tenant+`-export.json"`)
	writeJSON(rw, http.StatusOK, exp)
}

// assembleOrgExport gathers the org's profile, members, and every flow's full
// graph across all its workspaces. Each section is best-effort: an
// unconfigured/erroring store yields an empty section rather than failing the
// whole export. Reads stores directly (the handler already authorized) so it
// works for a platform admin exporting an org they aren't a member of.
func (h *HTTPGateway) assembleOrgExport(ctx context.Context, tenant string) OrgExport {
	exp := OrgExport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Tenant:      tenant,
		Members:     []exportOrgMember{},
		Flows:       []exportOrgFlow{},
	}
	if h.Profiles != nil {
		if pr, err := h.Profiles.GetOrgProfile(ctx, tenant); err == nil {
			exp.DisplayName = pr.DisplayName
		}
	}
	if h.Memberships != nil {
		if rows, err := h.Memberships.ListByTenant(ctx, tenant); err == nil {
			for _, m := range rows {
				exp.Members = append(exp.Members, exportOrgMember{
					Email: m.UserEmail, Roles: m.Roles,
				})
			}
		}
	}
	if h.svc != nil && h.svc.Workspaces != nil {
		wss, err := h.svc.Workspaces.List(tenant)
		if err != nil {
			exp.Note = "could not list workspaces: " + err.Error()
			return exp
		}
		for _, ws := range wss {
			store, err := h.svc.Workspaces.Open(tenant, ws)
			if err != nil {
				continue
			}
			ids, err := store.ListGraphs()
			if err != nil {
				continue
			}
			for _, id := range ids {
				g, err := store.Load(id)
				if err != nil {
					continue
				}
				exp.Flows = append(exp.Flows, exportOrgFlow{
					Workspace: ws, ID: id, Graph: redactGraphSecrets(g),
				})
			}
		}
	}
	return exp
}

// redactedValue is the placeholder substituted for a secret-bearing field
// in an export. Distinct from "" so the reader can tell a redaction from an
// genuinely empty value.
const redactedValue = "***redacted***"

// redactGraphSecrets returns a copy of g safe to serialize into an org
// export: webhook trigger secrets and any node Param/Env whose key looks
// credential-bearing are blanked. Unlike the per-user export (which only
// emits FlowSummary metadata, never the graph body), the org export carries
// each flow's full graph, so it would otherwise leak the webhook bearer
// token and any inline credentials. We deep-copy the slices and maps we
// touch so the on-disk graph the store handed us is never mutated in place.
func redactGraphSecrets(g core.Graph) core.Graph {
	if len(g.Triggers) > 0 {
		triggers := make([]core.GraphTrigger, len(g.Triggers))
		copy(triggers, g.Triggers)
		for i := range triggers {
			if triggers[i].Secret != "" {
				triggers[i].Secret = redactedValue
			}
		}
		g.Triggers = triggers
	}
	if len(g.Nodes) > 0 {
		nodes := make([]core.Node, len(g.Nodes))
		copy(nodes, g.Nodes)
		for i := range nodes {
			nodes[i].Params = redactParams(nodes[i].Params)
			nodes[i].Env = redactEnv(nodes[i].Env)
		}
		g.Nodes = nodes
	}
	// The FailureNotify webhook URL is itself the bearer secret (a Slack /
	// Discord / PagerDuty incoming-webhook URL), so it must be blanked too.
	// Keep the Email — it's PII the subject is entitled to, not a credential.
	if g.FailureNotify != nil && g.FailureNotify.Webhook != "" {
		fn := *g.FailureNotify
		fn.Webhook = redactedValue
		g.FailureNotify = &fn
	}
	return g
}

// redactParams copies params, blanking values whose key looks secret. It
// recurses into nested maps and slices so a secret tucked under a
// non-secret-named key (e.g. headers.Authorization, body.api_key) is masked
// too — a flat top-level scan would leak those in cleartext.
func redactParams(in map[string]any) map[string]any {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if looksSecretKey(k) {
			out[k] = redactedValue
			continue
		}
		out[k] = redactValueDeep(v)
	}
	return out
}

// redactValueDeep walks nested maps/slices applying the secret-key heuristic
// at every level. Scalars pass through unchanged (their parent key already
// decided they weren't secret-named).
func redactValueDeep(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return redactParams(t)
	case []any:
		out := make([]any, len(t))
		for i, el := range t {
			out[i] = redactValueDeep(el)
		}
		return out
	default:
		return v
	}
}

func redactEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if looksSecretKey(k) {
			out[k] = redactedValue
			continue
		}
		out[k] = v
	}
	return out
}

// looksSecretKey is a conservative name heuristic: a param/env key
// containing any of these substrings is treated as credential-bearing.
// Inline credentials are an anti-pattern (the secret store is the right
// home), but until every flow is migrated we must not leak the ones that
// are still inline.
func looksSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, needle := range []string{
		"secret", "token", "password", "passwd", "apikey", "api_key",
		"access_key", "private_key", "client_secret", "credential", "auth",
		"bearer", "webhook", "cookie", "session", "dsn", "connection_string",
		"signature",
	} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// exportHandler serves the current subject's data export. The export is
// keyed on p.Subject — which is the verified email ONLY for a session
// principal. For an API key, Subject is operator-chosen at issue time and not
// bound to the holder, so an org admin could mint a key with another user's
// email as the Subject and dump that victim's profile + cross-org
// memberships. Require a session credential here so Subject is always the
// authenticated human's own verified identity. (Mirrors the org-delete
// step-up in gdpr_http.go.)
func (h *HTTPGateway) exportHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "user store not configured")
		return
	}
	if !strings.HasPrefix(credentialFromRequest(r), auth.SessionTokenPrefix) {
		writeAPIError(rw, http.StatusForbidden, "session_required",
			"data export requires a signed-in session, not an API key")
		return
	}
	exp, err := h.assembleExport(r.Context(), p)
	if err != nil {
		// The only hard failure is loading the subject's own user row;
		// the other sections are best-effort. A missing/unloadable subject
		// is a 404, not a server error (keeps this off the 5xx path).
		writeAPIError(rw, http.StatusNotFound, "unknown_user", "no account found for this credential")
		return
	}
	h.audit(r.Context(), p, "account.export", p.Subject, "data subject export (Art. 15/20)")
	// Offer it as a download so a browser saves a file rather than rendering it.
	rw.Header().Set("Content-Disposition", `attachment; filename="dazyflow-data-export.json"`)
	writeJSON(rw, http.StatusOK, exp)
}

// assembleExport gathers the subject's data across stores. Each section is
// best-effort: a store that's unconfigured or errors yields an empty
// section rather than failing the whole export.
func (h *HTTPGateway) assembleExport(ctx context.Context, p core.Principal) (DataExport, error) {
	exp := DataExport{
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Memberships:    []exportMembership{},
		Invitations:    []exportInvitation{},
		APIKeys:        []APIKeySummary{},
		Flows:          []FlowSummary{},
		Runs:           []exportRun{},
		SupportTickets: []exportTicket{},
		AuditEvents:    []exportAuditEvent{},
		Boards:         []exportBoard{},
	}
	email := p.Subject // Subject is the email for human (session) principals.

	u, err := h.Users.GetByEmail(ctx, email)
	if err != nil {
		return exp, err
	}
	exp.Profile = exportProfile{
		Email:       u.Email,
		Subject:     u.Subject,
		Tenant:      u.Tenant,
		Workspace:   u.Workspace,
		Roles:       u.Roles,
		CreatedAt:   u.CreatedAt,
		VerifiedAt:  u.VerifiedAt,
		TOTPEnabled: u.TOTPEnabled,
	}

	if h.Memberships != nil {
		if ms, err := h.Memberships.ListByEmail(ctx, email); err == nil {
			for _, m := range ms {
				exp.Memberships = append(exp.Memberships, exportMembership{
					Tenant: m.Tenant, Workspace: m.Workspace, Roles: m.Roles, CreatedAt: m.CreatedAt,
				})
			}
		}
	}
	if il, ok := h.Invitations.(invitationLister); ok {
		if invs, err := il.ListByEmail(ctx, email); err == nil {
			for _, inv := range invs {
				exp.Invitations = append(exp.Invitations, exportInvitation{
					Tenant: inv.Tenant, Workspace: inv.Workspace, InvitedBy: inv.InvitedBy,
					CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt,
					AcceptedAt: inv.AcceptedAt, RevokedAt: inv.RevokedAt,
				})
			}
		}
	}
	// API keys issued to this subject, redacted (no hash/salt).
	if ks, ok := h.svc.AdminKeys.(subjectLister); ok {
		if keys, err := ks.ListBySubject(ctx, u.Subject); err == nil {
			now := time.Now()
			for _, k := range keys {
				exp.APIKeys = append(exp.APIKeys, redactKey(k, now))
			}
		}
	}
	// Flows + runs in the subject's home workspace.
	//
	// The Workspaces guard is not decoration: ListFlowSummaries calls
	// s.Workspaces.Open with no nil check of its own, so on a deployment
	// without a workspace store this panicked the whole endpoint — the one
	// section that could take the export down instead of coming back empty,
	// which is what the rest of this function promises.
	if h.svc != nil && h.svc.Workspaces != nil {
		if flows, err := h.svc.ListFlowSummaries(ctx, p, u.Tenant, u.Workspace); err == nil && flows != nil {
			exp.Flows = flows
		}
	}
	if h.svc != nil && h.svc.Jobs != nil {
		runs, err := h.svc.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant: u.Tenant, Workspace: u.Workspace, Limit: exportRunCap,
		})
		if err == nil {
			for _, rec := range runs {
				exp.Runs = append(exp.Runs, exportRun{
					ID: rec.ID, GraphID: rec.GraphID, Status: string(rec.Status),
				})
			}
			if len(runs) == exportRunCap {
				exp.Note = "run history truncated to the most recent " + strconv.Itoa(exportRunCap) + " runs"
			}
		}
	}

	// Support correspondence: threads this person opened, with the replies.
	// The store lists by tenant, so their own are filtered out here — and only
	// their own: another member's thread is that member's personal data, not
	// this subject's, and Art. 15 does not entitle anyone to it (Art. 15(4)).
	if h.Tickets != nil {
		if ts, err := h.Tickets.ListForTenant(ctx, u.Tenant, core.TicketListOpts{Limit: exportTicketCap}); err == nil {
			for _, t := range ts {
				if !identityMatches(t.CreatedBy, u.Subject, email) {
					continue
				}
				out := exportTicket{
					ID: t.ID, Tenant: t.Tenant, Subject: t.Subject, Status: string(t.Status),
					FlowID: t.FlowID, RunID: t.RunID,
					CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
					Messages: []exportTicketMsg{},
				}
				// The whole thread, including support's replies: a reply
				// written TO this person about their problem is part of the
				// correspondence they are entitled to a copy of.
				if msgs, err := h.Tickets.ListMessages(ctx, t.ID); err == nil {
					for _, m := range msgs {
						out.Messages = append(out.Messages, exportTicketMsg{
							Author: m.Author, AuthorKind: string(m.AuthorKind),
							Body: m.Body, CreatedAt: m.CreatedAt,
						})
					}
				}
				exp.SupportTickets = append(exp.SupportTickets, out)
			}
		}
	}

	// Their own audit trail — what they did, and the source IPs recorded with
	// it. Scoped to this actor in SQL, so it carries none of the org's other
	// activity.
	if h.Audit != nil {
		for _, actor := range dedupeNonEmpty(u.Subject, email) {
			evs, err := h.Audit.List(ctx, core.AuditQuery{
				Tenant: u.Tenant, Actor: actor, Limit: exportAuditCap,
			})
			if err != nil {
				continue
			}
			for _, e := range evs {
				// Re-check the actor here rather than trusting the store to
				// have applied AuditQuery.Actor. The field is newer than the
				// AuditLog interface, so an implementation predating it — or
				// any future one that overlooks it — would return the whole
				// tenant's trail, and this loop would copy a colleague's
				// actions and source IP straight into someone else's access
				// request. The SQL filter is for efficiency; this is the
				// guarantee.
				if !identityMatches(e.Actor, u.Subject, email) {
					continue
				}
				exp.AuditEvents = append(exp.AuditEvents, exportAuditEvent{
					Time: e.Time, Tenant: e.Tenant, Action: e.Action,
					Target: e.Target, Detail: e.Detail,
				})
			}
		}
		if len(exp.AuditEvents) >= exportAuditCap {
			exp.Note = strings.TrimSpace(exp.Note + " audit trail truncated to the most recent " +
				strconv.Itoa(exportAuditCap) + " events.")
		}
	}

	// Collections boards: named and counted, NOT dumped.
	//
	// A board holds rows a flow collected — leads, form responses, scraped
	// contacts — which are usually personal data about THIRD PARTIES. Handing
	// one member a copy of all of it under their own access request would
	// disclose other people's data, which Art. 15(4) exists to prevent. The
	// row-level export stays where it belongs: the Results page's per-board
	// CSV, used by someone acting for the org rather than for themselves.
	//
	// This mirrors how runs are already treated — ids and status, never the
	// payloads.
	//
	// ListBoards does no authorization of its own (its HTTP handler gates it),
	// so the same permission check is applied here. Without it the export would
	// be a way around it.
	if h.svc != nil && core.Require(p, core.PermGraphRun) == nil {
		if boards, err := h.svc.ListBoards(ctx, p, u.Tenant, u.Workspace); err == nil {
			for _, b := range boards {
				exp.Boards = append(exp.Boards, exportBoard{
					Workspace: u.Workspace, Name: b.Name, Rows: b.Rows,
				})
			}
		}
		if len(exp.Boards) > 0 {
			exp.Excluded = append(exp.Excluded,
				"Collections board CONTENTS are listed by name and row count only — the rows are "+
					"usually personal data about third parties, and Art. 15(4) limits disclosing "+
					"it through one member's access request. Export rows from the Results page.")
		}
	}

	// Platform-level roles held. No row of the subject's carries these, but
	// "this person is a platform admin" is personal data about them.
	if h.PlatformAdminGrants != nil {
		exp.RoleGrants.PlatformAdmin = h.PlatformAdminGrants.Granted(email)
	}
	for _, a := range h.PlatformAdmins {
		if strings.EqualFold(strings.TrimSpace(a), email) {
			exp.RoleGrants.PlatformAdmin = true
		}
	}
	if h.SupportAgents != nil {
		exp.RoleGrants.SupportAgent = h.SupportAgents.Granted(email)
	}

	// Said outright rather than silently omitted: a blocklist entry naming this
	// person is personal data being processed about them, and it is left out of
	// the self-serve download on purpose — disclosing the reason and the fact of
	// a ban through an automated endpoint would undermine the anti-abuse measure
	// it exists to be. Operators service that part of an access request by hand.
	exp.Excluded = append(exp.Excluded,
		"Anti-abuse blocklist entries are not included in the self-serve export. "+
			"If you believe one concerns you, ask the operator directly.")

	return exp, nil
}

// identityMatches reports whether a stored identifier is this subject, under
// either of the forms rows are written with (the principal subject or the
// email) and ignoring case.
func identityMatches(stored string, forms ...string) bool {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return false
	}
	for _, f := range forms {
		if f != "" && strings.EqualFold(stored, strings.TrimSpace(f)) {
			return true
		}
	}
	return false
}
