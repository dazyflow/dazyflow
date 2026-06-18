package daemon

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
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
	Note        string             `json:"note,omitempty"`
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

const exportRunCap = 1000

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
		`attachment; filename="hazyflow-org-`+tenant+`-export.json"`)
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
	return g
}

// redactParams copies params, blanking values whose key looks secret. Only
// string values are masked in place; non-string secret-keyed values are
// replaced with the redaction marker too.
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
		out[k] = v
	}
	return out
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
		"bearer",
	} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// exportHandler serves the current subject's data export. Read-only and
// scoped to the caller: a user can only export their own data (the
// principal binds the email/subject/tenant), so authentication is the only
// gate needed.
func (h *HTTPGateway) exportHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "user store not configured")
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
	rw.Header().Set("Content-Disposition", `attachment; filename="hazyflow-data-export.json"`)
	writeJSON(rw, http.StatusOK, exp)
}

// assembleExport gathers the subject's data across stores. Each section is
// best-effort: a store that's unconfigured or errors yields an empty
// section rather than failing the whole export.
func (h *HTTPGateway) assembleExport(ctx context.Context, p core.Principal) (DataExport, error) {
	exp := DataExport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Memberships: []exportMembership{},
		Invitations: []exportInvitation{},
		APIKeys:     []APIKeySummary{},
		Flows:       []FlowSummary{},
		Runs:        []exportRun{},
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
	if flows, err := h.svc.ListFlowSummaries(ctx, p, u.Tenant, u.Workspace); err == nil && flows != nil {
		exp.Flows = flows
	}
	if h.svc.Jobs != nil {
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
	return exp, nil
}
