package daemon

import (
	"context"
	"net/http"
	"strconv"
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
