// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Routes that act on runs rather than report them: starting a flow, testing
// a trigger, cancelling and resuming, clearing pending approvals, and the
// provider webhook endpoints that fire flows from outside.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"git.sr.ht/~klahr/dazyflow/core"
)

// listPendingApprovals returns the await_approval inbox: every node
// in the principal's scope currently parked with Status=awaiting and
// a `pending_url` output. Sorted newest-first by the service layer.
//
// Optional ?workspace= narrows the inbox to a single workspace.
// Admins (whose principal carries no workspace binding) get the
// tenant-wide view by default; the UI uses this query param to
// reflect the workspace switcher's current selection.
func (h *HTTPGateway) listPendingApprovals(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

// approveAuthed is the bearer-token-authenticated approval path used
// by the inbox UI. The principal's identity is trusted directly — no
// HMAC token to validate, because by getting here they've already
// proven workspace membership through the API key chain.
//
// The HMAC-based /approve/{runID}/{nodeID} endpoint stays available
// for the email/Slack notification flow where the approver doesn't
// have a session.
func (h *HTTPGateway) approveAuthed(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	nodeID := r.PathValue("nodeID")
	decision := r.URL.Query().Get("decision")
	if decision == "" {
		decision = "approve"
	}
	// Tenant scope: load the parent graph through GetJob, which already
	// enforces the principal's tenant. Prevents a malicious-but-valid
	// API key from approving someone else's pending node.
	if _, err := h.svc.GetJob(r.Context(), p, runID); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeJSONError(rw, http.StatusNotFound, err.Error())
			return
		}
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	// Always attribute the approval to the authenticated principal — never a
	// client-supplied ?approver=. This path has a proven identity (the API-key
	// chain), so honoring a query param would let a valid caller forge who
	// approved in both the audit log and the resumed node's record. (The
	// unauthenticated HMAC /approve path is different: there the approver is
	// supplied because there's no session identity to trust.)
	if err := h.svc.Approve(r.Context(), runID, nodeID, ApprovalDecision{
		Decision: decision,
		Approver: p.Subject,
		Comment:  r.URL.Query().Get("comment"),
	}); err != nil {
		// Sentinels, not substrings: Approve documents exactly which errors
		// it returns (ErrConflict when the node isn't awaiting, ErrNotFound
		// when the record is unknown, errBadApprovalDecision for a malformed
		// decision), and matching on message text meant any reword flipped
		// the status — including "not found" appearing incidentally inside an
		// unrelated wrapped error.
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

// cancelRun aborts an in-flight run. Body is an optional
// {"reason":"..."} for the audit trail. Maps service-layer errors to
// the conventional status codes: 404 unknown run, 409 already
// terminal, 403 unauthorized.
func (h *HTTPGateway) cancelRun(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("runID")
	var body struct {
		Reason string `json:"reason"`
	}
	// Empty body is fine — keep the API ergonomic for the UI's
	// no-arg cancel click. Only fail on malformed JSON.
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

// resumeRun continues a run paused at a breakpoint (#12). Body {"step":true}
// advances one node and pauses again; otherwise the run continues to the
// next breakpoint or completion.
func (h *HTTPGateway) resumeRun(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *HTTPGateway) runGraph(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	// Manual: this is the editor's Run button (and the runs list's "Run again"),
	// so somebody is watching. No failure email — see core.JobRecord.Manual.
	runID, err := h.svc.SubmitGraphOpts(r.Context(), p, g, SubmitOpts{Manual: true})
	if err != nil {
		// Plan-gate refusals get 402 so the web client can show an
		// upgrade prompt instead of a generic error toast.
		if errors.Is(err, core.ErrPlanLimit) {
			writeJSONError(rw, http.StatusPaymentRequired, err.Error())
			return
		}
		// A suspended org is locked out — 403, not a generic 400.
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

// testTrigger runs a webhook-triggered flow with a synthetic payload so
// a user can verify the flow end-to-end without wiring up an external
// caller (their website form, Zapier, curl, …). The request body is the
// sample payload; we feed it through the exact same seed-building path a
// real /trigger hit uses (buildWebhookSeed), so webhook_input nodes
// light up identically — closing the "Run button does nothing useful on
// a webhook flow" gap and the documented sampleNode webhook limitation.
//
// Unlike the public /trigger listener (bearer-secret auth, system
// principal), this runs under the caller's own token + graph:run, so it
// respects normal flow visibility and shows up in the run list like any
// other run.
func (h *HTTPGateway) testTrigger(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	// Read the sample body with a cap — a synthetic test payload is
	// small, and we don't want a stray large POST to balloon memory.
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
		if n.Module == webhookInputModuleID {
			seeds[n.ID] = seed
		}
	}
	if len(seeds) == 0 {
		writeJSONError(rw, http.StatusBadRequest, "flow has no webhook_input node to send a test event to")
		return
	}
	// A test fired from the editor with a made-up payload is the definition of
	// a run someone is watching, so no failure email.
	runID, err := h.svc.SubmitGraphOpts(r.Context(), p, g, SubmitOpts{Seeds: seeds, Manual: true})
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.run", id, "test-trigger run="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": runID})
}

// sampleNode runs a partial graph that ends at the requested nodeID.
// The submitted run contains only nodeID + its transitive predecessors
// — every other node and every edge that would lead out of the subset
// is dropped before submission. This lets a graph author "preview"
// what one node emits without firing downstream side effects.
//
// Identity is preserved end-to-end: the same graph ID, tenant, and
// workspace are reused, so the run shows up in the normal RunList
// (filtering "sample vs production" runs is a follow-up). Authz
// flows through SubmitGraph unchanged — sampling a node you can't
// run is rejected at the same gate as a full run would be.
//
// Limitations called out for V1: webhook_input nodes in the subset
// will fail standalone with code=no_trigger_data (no body was POSTed
// to the webhook listener for this run). Users on a webhook flow
// should hit the trigger via curl; "sample with a synthetic body"
// is a separate follow-up.
// slackEvents dispatches a Slack Events API POST to the configured
// handler. Returns 501 if the handler isn't wired (so a misconfigured
// deployment surfaces clearly instead of silently rejecting on bad
// signature).
func (h *HTTPGateway) slackEvents(rw http.ResponseWriter, r *http.Request) {
	if h.SlackEvents == nil {
		http.Error(rw, "Slack events endpoint not configured (set --slack-signing-secret on dzd)", http.StatusNotImplemented)
		return
	}
	h.SlackEvents.ServeHTTP(rw, r)
}

func (h *HTTPGateway) githubEvents(rw http.ResponseWriter, r *http.Request) {
	if h.GitHubEvents == nil {
		http.Error(rw, "GitHub events endpoint not configured (set --github-webhook-secret on dzd)", http.StatusNotImplemented)
		return
	}
	h.GitHubEvents.ServeHTTP(rw, r)
}

// stripeTenantEvents is the tenant-scoped Stripe webhook (payment
// triggers) — not to be confused with stripeEvents, the platform
// billing webhook on the unsuffixed path.
func (h *HTTPGateway) stripeTenantEvents(rw http.ResponseWriter, r *http.Request) {
	if h.StripeEvents == nil {
		http.Error(rw, "Stripe events endpoint not configured (encrypted secret store required)", http.StatusNotImplemented)
		return
	}
	h.StripeEvents.ServeHTTP(rw, r)
}
