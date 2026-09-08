// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

type supportAPI struct {
	auditor
	svc              *Service
	Tickets          core.TicketStore
	Grants           core.GrantStore
	Bundles          core.BundleStore
	SupportAgents    support.AgentStore
	SupportInbox     string
	SupportGrantTTL  time.Duration
	SupportRateLimit *ipRateLimiter
	supportNow       func() time.Time
}

func (h *HTTPGateway) supportAPI() *supportAPI {
	return &supportAPI{auditor: h.auditor(), svc: h.svc, Tickets: h.Tickets, Grants: h.Grants, Bundles: h.Bundles, SupportAgents: h.SupportAgents, SupportInbox: h.SupportInbox, SupportGrantTTL: h.SupportGrantTTL, SupportRateLimit: h.SupportRateLimit, supportNow: h.supportNow}
}

// httpsupport.go wires the Support feature's HTTP surface: a support agent
// requests a scoped, time-boxed, read-only view of one flow; an org admin
// approves/denies/revokes; the agent then reads the REDACTED bundle. Every
// action is audited into the ORG's log.
//
// Trust invariants enforced here:
//   - Requesting/viewing requires core.PermSupportAgent (the weak, grant-gated
//     role stamped at session issue).
//   - Deciding/revoking/listing requires org-admin IN the grant's tenant.
//   - The view is authorized ONLY by an active AccessGrant
//     (AuthorizeGraphSupportView), never by tenant membership, and always
//     serves the redacted BuildSupportBundle — never the raw graph/run.

const defaultSupportGrantTTL = 4 * time.Hour

func (h *supportAPI) supportTime() time.Time {
	if h.supportNow != nil {
		return h.supportNow()
	}
	return time.Now().UTC()
}

func (h *supportAPI) supportGrantTTL() time.Duration {
	if h.SupportGrantTTL > 0 {
		return h.SupportGrantTTL
	}
	return defaultSupportGrantTTL
}

func (h *supportAPI) supportEnabled() bool { return h.Grants != nil }

func (h *supportAPI) requestGrant(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if err := core.Require(p, core.PermSupportAgent); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "support agent role required")
		return
	}
	var body struct {
		Tenant   string `json:"tenant"`
		FlowID   string `json:"flow_id"`
		TicketID string `json:"ticket_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Tenant == "" || body.FlowID == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "tenant and flow_id are required")
		return
	}
	// NOTE: deliberately NOT validating that the tenant exists here. The
	// obvious oracle (memberships.ListByTenant) is wrong: membership rows only
	// exist for explicitly-created multi-user orgs, so every personal `usr_*`
	// tenant looks non-existent and a legitimate request for a real customer
	// gets refused. Blocking a support agent from helping a paying customer is
	// far worse than a typo'd grant sitting unapproved, so the request is
	// permissive and approval — which only the real org can give — is the gate.
	id, err := newID()
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	now := h.supportTime()
	grant := core.AccessGrant{
		ID:           id,
		TicketID:     body.TicketID,
		Tenant:       body.Tenant,
		FlowID:       body.FlowID,
		AgentSubject: p.Subject,
		Status:       core.GrantRequested,
		RequestedAt:  now,
		RequestedBy:  p.Subject,
	}
	if err := h.Grants.Create(r.Context(), grant); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r.Context(), core.Principal{Tenant: grant.Tenant, Subject: p.Subject},
		"support.grant.request", grant.FlowID, "grant="+grant.ID)
	if h.ticketsEnabled() && grant.TicketID != "" {
		_ = h.appendSystemNote(r.Context(), grant.TicketID, core.NoteGrantRequested,
			"Support requested read-only access to this flow. An organization admin must approve it.", now)
	}
	writeJSON(rw, http.StatusCreated, grant)
}

func (h *supportAPI) listGrants(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if !core.CanAdminOrg(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "organization admin required")
		return
	}
	grants, err := h.Grants.ListForTenant(r.Context(), p.Tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"grants": grants})
}

func (h *supportAPI) listMyGrants(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if err := core.Require(p, core.PermSupportAgent); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "support agent role required")
		return
	}
	grants, err := h.Grants.ListForAgent(r.Context(), p.Subject)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"grants": grants})
}

func (h *supportAPI) decideGrant(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	grant, ok := h.loadGrantForAdmin(rw, r, p)
	if !ok {
		return
	}
	var body struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	var status core.GrantStatus
	switch body.Decision {
	case "approve":
		status = core.GrantApproved
	case "deny":
		status = core.GrantDenied
	default:
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "decision must be 'approve' or 'deny'")
		return
	}
	now := h.supportTime()
	if err := h.Grants.Decide(r.Context(), grant.ID, status, p.Subject, now, now.Add(h.supportGrantTTL())); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "grant_not_found", "no grant with that id")
			return
		}
		writeAPIError(rw, http.StatusConflict, "grant_conflict", err.Error())
		return
	}
	h.audit(r.Context(), core.Principal{Tenant: grant.Tenant, Subject: p.Subject},
		"support.grant."+body.Decision, grant.FlowID, "grant="+grant.ID)
	updated, _ := h.Grants.Get(r.Context(), grant.ID)
	writeJSON(rw, http.StatusOK, updated)
}

func (h *supportAPI) revokeGrant(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	grant, err := h.Grants.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "grant_not_found", "no grant with that id")
		return
	}
	orgAdmin := core.CanAdminOrg(p) && core.RequireTenant(p, grant.Tenant) == nil
	selfAgent := p.Subject != "" && p.Subject == grant.AgentSubject
	if !orgAdmin && !selfAgent {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "not allowed to revoke this grant")
		return
	}
	if err := h.Grants.Revoke(r.Context(), grant.ID, p.Subject, h.supportTime()); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "grant_not_found", "no grant with that id")
			return
		}
		writeAPIError(rw, http.StatusConflict, "grant_conflict", err.Error())
		return
	}
	h.audit(r.Context(), core.Principal{Tenant: grant.Tenant, Subject: p.Subject},
		"support.grant.revoke", grant.FlowID, "grant="+grant.ID)
	writeJSON(rw, http.StatusOK, map[string]any{"status": "revoked"})
}

func (h *supportAPI) supportView(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if err := core.Require(p, core.PermSupportAgent); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "support agent role required")
		return
	}
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	flowID := r.PathValue("flow_id")
	now := h.supportTime()

	// The grant is the sole authority. No active grant → 404 (don't leak the
	// flow's existence to an agent the org hasn't consented to).
	grant, ok, err := h.Grants.ActiveGrant(r.Context(), p.Subject, tenant, flowID, now)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !ok {
		writeAPIError(rw, http.StatusNotFound, "no_access", "no active support grant for this flow")
		return
	}
	graph, err := h.svc.LoadGraphForSupport(r.Context(), tenant, workspace, flowID)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", "no flow with that id")
		return
	}
	if err := core.AuthorizeGraphSupportView(p, graph, grant, now); err != nil {
		writeAPIError(rw, http.StatusNotFound, "no_access", "no active support grant for this flow")
		return
	}

	// Optional run, scoped to this flow/tenant so a stray run_id can't pull an
	// unrelated run's (redacted) shape.
	var runPtr *core.RunSnapshot
	if runID := r.URL.Query().Get("run_id"); runID != "" {
		if rs, ok := h.supportRunSnapshot(r.Context(), tenant, workspace, flowID, runID); ok {
			runPtr = &rs
		}
	}

	mode := core.RedactMode(r.URL.Query().Get("mode")) // "" → structure-only default
	manifests := h.svc.manifestsSnapshot(tenant)
	// ValidateGraphFull already includes LintGraph's findings (see
	// core/validate.go), so it's the complete set — appending LintGraph again
	// double-counts every lint issue.
	issues := core.ValidateGraphFull(graph, manifests)
	bundle := core.BuildSupportBundle(graph, runPtr, issues, mode)

	h.audit(r.Context(), core.Principal{Tenant: tenant, Subject: p.Subject},
		"support.view", flowID, "grant="+grant.ID)
	writeJSON(rw, http.StatusOK, bundle)
}

func (h *supportAPI) supportRunSnapshot(ctx context.Context, tenant, workspace, flowID, runID string) (core.RunSnapshot, bool) {
	runRec, err := h.svc.Jobs.Get(ctx, runID)
	if err != nil || runRec.Tenant != tenant || runRec.GraphID != flowID {
		return core.RunSnapshot{}, false
	}
	nodes, err := h.svc.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant:     tenant,
		Workspace:  workspace,
		GraphRunID: runID,
		Limit:      1000,
	})
	if err != nil {
		return core.RunSnapshot{}, false
	}
	return support.RunSnapshotFromRecords(runRec, nodes), true
}

func (h *supportAPI) loadGrantForAdmin(rw http.ResponseWriter, r *http.Request, p core.Principal) (core.AccessGrant, bool) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return core.AccessGrant{}, false
	}
	grant, err := h.Grants.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "grant_not_found", "no grant with that id")
		return core.AccessGrant{}, false
	}
	if !core.CanAdminOrg(p) || core.RequireTenant(p, grant.Tenant) != nil {
		// 404 (not 403) so a cross-tenant admin can't probe grant existence.
		writeAPIError(rw, http.StatusNotFound, "grant_not_found", "no grant with that id")
		return core.AccessGrant{}, false
	}
	return grant, true
}

func (h *supportAPI) allowSupportWrite(rw http.ResponseWriter, p core.Principal) bool {
	if h.SupportRateLimit == nil {
		return true
	}
	key := p.Subject
	if key == "" {
		key = p.Tenant
	}
	if !h.SupportRateLimit.Allow(key) {
		rw.Header().Set("Retry-After", "60")
		writeAPIError(rw, http.StatusTooManyRequests, "rate_limited",
			"you're filing support messages very quickly — wait a moment and try again")
		return false
	}
	return true
}
