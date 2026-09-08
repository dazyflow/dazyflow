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

type subjectLister interface {
	ListBySubject(ctx context.Context, subject string) ([]auth.APIKey, error)
}
type invitationLister interface {
	ListByEmail(ctx context.Context, email string) ([]auth.Invitation, error)
}

type DataExport struct {
	GeneratedAt    string             `json:"generated_at"`
	Profile        exportProfile      `json:"profile"`
	Memberships    []exportMembership `json:"memberships"`
	Invitations    []exportInvitation `json:"invitations"`
	APIKeys        []APIKeySummary    `json:"api_keys"`
	Flows          []FlowSummary      `json:"flows"`
	Runs           []exportRun        `json:"runs"`
	SupportTickets []exportTicket     `json:"support_tickets"`
	AuditEvents    []exportAuditEvent `json:"audit_events"`
	Boards         []exportBoard      `json:"boards"`
	RoleGrants     exportRoleGrants   `json:"role_grants"`
	Note           string             `json:"note,omitempty"`
	// In the document itself: an export that silently omits is worse than one that says.
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
	exportRunCap   = 1000
	exportAuditCap = 5000
	// Bounds the scan; an export must not become a full table walk.
	exportTicketCap = 500
)

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

// Export and delete: both need more than ordinary org admin.
func canManageOrg(p core.Principal, tenant string) bool {
	if isPlatformAdmin(p) {
		return true
	}
	return core.CanAdminOrg(p) && p.Tenant == tenant
}

func (h *gdprAPI) exportOrgHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *gdprAPI) assembleOrgExport(ctx context.Context, tenant string) OrgExport {
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

// Substituted for a secret-bearing field.
const redactedValue = "***redacted***"

// A COPY: the export must never be able to write back into the live graph, and a
// flow carries pasted credentials as often as referenced ones.
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
	// The webhook URL IS the bearer secret.
	if g.FailureNotify != nil && g.FailureNotify.Webhook != "" {
		fn := *g.FailureNotify
		fn.Webhook = redactedValue
		g.FailureNotify = &fn
	}
	return g
}

// Copies rather than editing in place.
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

// Conservative: over-redacting an export is far cheaper than leaking a key.
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

// The CURRENT subject's own data, never another's.
func (h *gdprAPI) exportHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
		writeAPIError(rw, http.StatusNotFound, "unknown_user", "no account found for this credential")
		return
	}
	h.audit(r.Context(), p, "account.export", p.Subject, "data subject export (Art. 15/20)")
	rw.Header().Set("Content-Disposition", `attachment; filename="dazyflow-data-export.json"`)
	writeJSON(rw, http.StatusOK, exp)
}

func (h *gdprAPI) assembleExport(ctx context.Context, p core.Principal) (DataExport, error) {
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
	if ks, ok := h.svc.AdminKeys.(subjectLister); ok {
		if keys, err := ks.ListBySubject(ctx, u.Subject); err == nil {
			now := time.Now()
			for _, k := range keys {
				exp.APIKeys = append(exp.APIKeys, redactKey(k, now))
			}
		}
	}
	if h.svc != nil && h.svc.Workspaces != nil {
		if flows, err := h.svc.ListFlowSummaries(ctx, p, u.Tenant, u.Workspace); err == nil && flows != nil {
			exp.Flows = flows
		}
	}
	if h.svc != nil && h.svc.Jobs != nil {
		runs, err := core.ListRunSummaries(ctx, h.svc.Jobs, core.ListGraphRunsOpts{
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

	if h.Audit != nil {
		for _, actor := range dedupeNonEmpty(u.Subject, email) {
			evs, err := h.Audit.List(ctx, core.AuditQuery{
				Tenant: u.Tenant, Actor: actor, Limit: exportAuditCap,
			})
			if err != nil {
				continue
			}
			for _, e := range evs {
				// Re-checked rather than trusting the store to have scoped it.
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

	// Named and counted, NOT dumped: a board is org data, not one person's.
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

	// Said outright rather than silently omitted.
	exp.Excluded = append(exp.Excluded,
		"Anti-abuse blocklist entries are not included in the self-serve export. "+
			"If you believe one concerns you, ask the operator directly.")

	return exp, nil
}

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
