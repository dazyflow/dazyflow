// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// support_routes.go wires the Support feature's HTTP surface (see
// TODO-support-tickets.md): a support agent requests a scoped, time-boxed,
// read-only view of one flow; an org admin approves/denies/revokes; the agent
// then reads the REDACTED bundle. Every action is audited into the ORG's log.
//
// Trust invariants enforced here:
//   - Requesting/viewing requires core.PermSupportAgent (the weak, grant-gated
//     role stamped at session issue).
//   - Deciding/revoking/listing requires org-admin IN the grant's tenant.
//   - The view is authorized ONLY by an active AccessGrant
//     (AuthorizeGraphSupportView), never by tenant membership, and always
//     serves the redacted BuildSupportBundle — never the raw graph/run.

// defaultSupportGrantTTL is the approved-grant lifetime when SupportGrantTTL is
// unset. Chosen as a working-session window (see the design decision).
const defaultSupportGrantTTL = 4 * time.Hour

func (h *HTTPGateway) supportTime() time.Time {
	if h.supportNow != nil {
		return h.supportNow()
	}
	return time.Now().UTC()
}

func (h *HTTPGateway) supportGrantTTL() time.Duration {
	if h.SupportGrantTTL > 0 {
		return h.SupportGrantTTL
	}
	return defaultSupportGrantTTL
}

// supportEnabled reports whether the grant store is wired; endpoints 501 when
// not (a deployment with no support surface).
func (h *HTTPGateway) supportEnabled() bool { return h.Grants != nil }

// requestGrant: a support agent asks for read-only access to one flow.
// POST /api/v1/support/grants  {tenant, flow_id, ticket_id?}
func (h *HTTPGateway) requestGrant(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
		// ExpiresAt is set on approval (now + TTL).
	}
	if err := h.Grants.Create(r.Context(), grant); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Audit into the ORG's log — the org sees support requesting access.
	h.audit(r.Context(), core.Principal{Tenant: grant.Tenant, Subject: p.Subject},
		"support.grant.request", grant.FlowID, "grant="+grant.ID)
	writeJSON(rw, http.StatusCreated, grant)
}

// listGrants: an org admin sees every grant scoped to their tenant (the consent
// surface). GET /api/v1/support/grants
func (h *HTTPGateway) listGrants(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

// listMyGrants: a support agent sees every grant THEY requested, across every
// org — the "flows I can reach" surface that powers one-click open. Keyed on
// the agent, not a tenant. GET /api/v1/support/grants/mine
func (h *HTTPGateway) listMyGrants(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

// decideGrant: an org admin approves or denies a requested grant.
// POST /api/v1/support/grants/{id}/decide  {decision: "approve"|"deny"}
func (h *HTTPGateway) decideGrant(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

// revokeGrant: an org admin (in the grant's tenant) OR the agent themselves ends
// an approved grant early. POST /api/v1/support/grants/{id}/revoke
func (h *HTTPGateway) revokeGrant(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.supportEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	grant, err := h.Grants.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "grant_not_found", "no grant with that id")
		return
	}
	// Either the org admin in the grant's tenant, or the granted agent.
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

// supportView: a support agent reads the REDACTED bundle for one flow, gated by
// an active grant. GET /api/v1/support/flows/{tenant}/{workspace}/{flow_id}?run_id=&mode=
func (h *HTTPGateway) supportView(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	// Belt-and-suspenders: re-check the capability against the loaded graph.
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
	manifests := h.svc.manifestsSnapshot()
	// ValidateGraphFull already includes LintGraph's findings (see
	// core/validate.go), so it's the complete set — appending LintGraph again
	// double-counts every lint issue.
	issues := core.ValidateGraphFull(graph, manifests)
	bundle := core.BuildSupportBundle(graph, runPtr, issues, mode)

	h.audit(r.Context(), core.Principal{Tenant: tenant, Subject: p.Subject},
		"support.view", flowID, "grant="+grant.ID)
	writeJSON(rw, http.StatusOK, bundle)
}

// supportRunSnapshot loads a run's records and projects them into a RunSnapshot,
// but only when the run genuinely belongs to (tenant, flowID) — the grant scopes
// support to one flow, so a run from another flow/tenant is treated as absent.
func (h *HTTPGateway) supportRunSnapshot(ctx context.Context, tenant, workspace, flowID, runID string) (core.RunSnapshot, bool) {
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
	return RunSnapshotFromRecords(runRec, nodes), true
}

// loadGrantForAdmin loads the {id} grant and enforces org-admin in its tenant.
// Writes the error + returns ok=false on any failure.
func (h *HTTPGateway) loadGrantForAdmin(rw http.ResponseWriter, r *http.Request, p core.Principal) (core.AccessGrant, bool) {
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
