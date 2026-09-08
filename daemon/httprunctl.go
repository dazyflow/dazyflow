// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

type runCtlAPI struct {
	auditor
	svc          *Service
	SlackEvents  *SlackEventsHandler
	GitHubEvents *GitHubEventsHandler
	StripeEvents *StripeEventsHandler
}

func (h *HTTPGateway) runCtlAPI() *runCtlAPI {
	return &runCtlAPI{auditor: h.auditor(), svc: h.svc, SlackEvents: h.SlackEvents, GitHubEvents: h.GitHubEvents, StripeEvents: h.StripeEvents}
}

func (h *runCtlAPI) listPendingApprovals(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	approvals, err := h.svc.ListPendingApprovals(
		r.Context(),
		p,
		r.URL.Query().Get("tenant"),
		r.URL.Query().Get("workspace"),
	)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"approvals": approvals})
}

func (h *runCtlAPI) countPendingApprovals(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	n, err := h.svc.CountPendingApprovals(
		r.Context(),
		p,
		r.URL.Query().Get("tenant"),
		r.URL.Query().Get("workspace"),
	)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"count": n})
}

func (h *runCtlAPI) listDecidedApprovals(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeJSONError(rw, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = n
	}
	approvals, err := h.svc.ListDecidedApprovals(
		r.Context(),
		p,
		r.URL.Query().Get("tenant"),
		r.URL.Query().Get("workspace"),
		limit,
	)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"approvals": approvals})
}

// The authenticated path, as against the signed one-click link.
func (h *runCtlAPI) approveAuthed(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	decision := r.URL.Query().Get("decision")
	if decision == "" {
		decision = "approve"
	}
	runRec, err := h.svc.GetJob(r.Context(), p, runID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	if auth.IsAPIKeyCredential(credentialFromRequest(r)) && approvalNeedsHuman(runRec, nodeID) {
		writeJSONError(rw, http.StatusForbidden,
			"this approval step is for a person to decide, so an API key may not approve it. "+
				"To let a machine decide this gate, turn on 'Let an API key approve this' on the step. "+
				"A human can always decide it from the Approvals inbox.")
		return
	}
	// Attributed to the AUTHENTICATED principal, never a caller-supplied name.
	if err := h.svc.Approve(r.Context(), runID, nodeID, ApprovalDecision{
		Decision: decision,
		Approver: p.Subject,
		Comment:  r.URL.Query().Get("comment"),
	}); err != nil {
		switch {
		case errors.Is(err, core.ErrConflict):
			writeJSONError(rw, http.StatusConflict, err.Error())
		case errors.Is(err, core.ErrNotFound):
			writeJSONError(rw, http.StatusNotFound, err.Error())
		case errors.Is(err, errBadApprovalDecision):
			writeJSONError(rw, http.StatusBadRequest, err.Error())
		default:
			writeJSONError(rw, http.StatusInternalServerError, err.Error())
		}
		return
	}
	h.audit(r.Context(), p, "approval", runID+"/"+nodeID, decision)
	writeJSON(rw, http.StatusOK, map[string]string{"status": "resumed", "decision": decision})
}

// Off the graph the run PINNED, not HEAD: an edit must not open a live gate.
func approvalNeedsHuman(runRec core.JobRecord, nodeID string) bool {
	if len(runRec.GraphPayload) == 0 {
		return false
	}
	var g core.Graph
	if err := json.Unmarshal(runRec.GraphPayload, &g); err != nil {
		return false
	}
	return core.ApprovalStepRequiresHuman(g, nodeID)
}

func (h *runCtlAPI) cancelRun(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	var body struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
			return
		}
	}
	if err := h.svc.CancelGraphRun(r.Context(), p, runID, body.Reason); err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound):
			writeJSONError(rw, http.StatusNotFound, err.Error())
		case errors.Is(err, core.ErrConflict):
			writeJSONError(rw, http.StatusConflict, err.Error())
		case errors.Is(err, core.ErrUnauthorized):
			writeJSONError(rw, http.StatusForbidden, err.Error())
		default:
			writeJSONError(rw, http.StatusInternalServerError, err.Error())
		}
		return
	}
	h.audit(r.Context(), p, "run.cancel", runID, body.Reason)
	writeJSON(rw, http.StatusOK, map[string]string{"status": "cancelled"})
}

// step=true advances one node instead of continuing.
func (h *runCtlAPI) resumeRun(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	var body struct {
		Step bool `json:"step"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
			return
		}
	}
	if err := h.svc.ResumeGraphRun(r.Context(), p, runID, body.Step); err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound):
			writeJSONError(rw, http.StatusNotFound, err.Error())
		case errors.Is(err, core.ErrConflict):
			writeJSONError(rw, http.StatusConflict, err.Error())
		case errors.Is(err, core.ErrUnauthorized):
			writeJSONError(rw, http.StatusForbidden, err.Error())
		default:
			writeJSONError(rw, http.StatusInternalServerError, err.Error())
		}
		return
	}
	action := "run.resume"
	if body.Step {
		action = "run.step"
	}
	h.audit(r.Context(), p, action, runID, "")
	writeJSON(rw, http.StatusOK, map[string]string{"status": "resumed"})
}

func (h *runCtlAPI) runGraph(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	runID, err := h.svc.SubmitGraphOpts(r.Context(), p, g, SubmitOpts{Manual: true})
	if err != nil {
		if errors.Is(err, core.ErrPlanLimit) {
			writeJSONError(rw, http.StatusPaymentRequired, err.Error())
			return
		}
		if errors.Is(err, core.ErrOrgSuspended) {
			writeJSONError(rw, http.StatusForbidden, err.Error())
			return
		}
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.run", id, "run="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": runID})
}

// A synthetic payload, so an author can test without a real delivery.
func (h *runCtlAPI) testTrigger(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	var rawBody []byte
	if r.Body != nil {
		const maxSampleBytes = 1 << 20 // 1 MiB
		data, err := io.ReadAll(io.LimitReader(r.Body, maxSampleBytes+1))
		_ = r.Body.Close()
		if err != nil {
			writeJSONError(rw, http.StatusBadRequest, "read body")
			return
		}
		if int64(len(data)) > maxSampleBytes {
			writeJSONError(rw, http.StatusRequestEntityTooLarge, "sample body too large")
			return
		}
		rawBody = data
	}
	seed := buildWebhookSeed(rawBody, r)
	seeds := map[string]core.Result{}
	for _, n := range g.Nodes {
		switch n.Module {
		case webhookInputModuleID, core.RequestInputModule, core.FormInputModule:
			seeds[n.ID] = seed
		}
	}
	if len(seeds) == 0 {
		writeJSONError(rw, http.StatusBadRequest, "flow has no Webhook, Form or Request step to send a test event to")
		return
	}
	runID, err := h.svc.SubmitGraphOpts(r.Context(), p, g, SubmitOpts{Seeds: seeds, Manual: true})
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.run", id, "test-trigger run="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": runID})
}

// A partial run ending at one node, which keeps the same dispatch rules as the
// full graph — otherwise "sample this step" would behave differently from
// running the flow.
func (h *runCtlAPI) slackEvents(rw http.ResponseWriter, r *http.Request) {
	if h.SlackEvents == nil {
		http.Error(rw, "Slack events endpoint not configured (set --slack-signing-secret on dzd)", http.StatusNotImplemented)
		return
	}
	h.SlackEvents.ServeHTTP(rw, r)
}

func (h *runCtlAPI) githubEvents(rw http.ResponseWriter, r *http.Request) {
	if h.GitHubEvents == nil {
		http.Error(rw, "GitHub events endpoint not configured (set --github-webhook-secret on dzd)", http.StatusNotImplemented)
		return
	}
	h.GitHubEvents.ServeHTTP(rw, r)
}

func (h *runCtlAPI) stripeTenantEvents(rw http.ResponseWriter, r *http.Request) {
	if h.StripeEvents == nil {
		http.Error(rw, "Stripe events endpoint not configured (encrypted secret store required)", http.StatusNotImplemented)
		return
	}
	h.StripeEvents.ServeHTTP(rw, r)
}
