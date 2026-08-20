// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// /me/flows and /me/runs are the spec-aligned wire shapes for the
// flows + runs surface. They sit on top of the existing graph + job
// service methods — translation only, no new business logic. The
// `flow_id` path parameter is a percent-encoded composite of
// `${tenant}/${workspace}/${id}` (slashes become %2F so the value
// stays in a single mux segment); the daemon decodes via PathValue
// then splits to recover the three parts. The `run_id` parameter is
// the jobID verbatim — runs are already globally unique by ID, no
// composite needed.
//
// Errors on this surface use the structured envelope from errors.go.
// Legacy /api/v1/graphs and /api/v1/jobs routes stay alongside these
// for the transition; they will be removed once the web client and
// dz-mcp have both migrated.

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// splitFlowID parses the {flow_id} path parameter back into
// (tenant, workspace, graphID). Returns an error suitable for direct
// emission via writeAPIError when the composite is malformed.
//
// Validates against the principal's scope when the principal is
// scoped: a workspace-bound key calling /me/flows/other-tenant/...
// gets a 403 (forbidden_scope) rather than a confused 404 from the
// underlying graph store.
func splitFlowID(flowID string, p core.Principal) (tenant, workspace, id string, err error) {
	parts := strings.SplitN(flowID, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("flow_id must be tenant/workspace/id (got %q)", flowID)
	}
	tenant, workspace, id = parts[0], parts[1], parts[2]
	if p.Tenant != "" && tenant != p.Tenant && !isPlatformAdmin(p) {
		return "", "", "", fmt.Errorf("cannot act on tenant %q (principal is bound to %q)", tenant, p.Tenant)
	}
	if p.Workspace != "" && workspace != p.Workspace && !isPlatformAdmin(p) {
		// p.Workspace is set on workspace-scoped keys but empty on
		// tenant-admin keys; the latter can act on any workspace in
		// their tenant.
		return "", "", "", fmt.Errorf("cannot act on workspace %q (principal is bound to %q)", workspace, p.Workspace)
	}
	return tenant, workspace, id, nil
}

// readFlowID centralizes the parse-or-401-style "extract scope and
// translate to (tenant, workspace, id)" dance every /me/flows handler
// shares. Returns false when the handler should stop (it already wrote
// the error envelope).
func (h *HTTPGateway) readFlowID(rw http.ResponseWriter, r *http.Request, p core.Principal) (string, string, string, bool) {
	tenant, workspace, id, err := splitFlowID(r.PathValue("flow_id"), p)
	if err != nil {
		if strings.HasPrefix(err.Error(), "cannot act on") {
			writeAPIError(rw, http.StatusForbidden, "forbidden_scope", err.Error())
		} else {
			writeAPIError(rw, http.StatusBadRequest, "invalid_flow_id", err.Error())
		}
		return "", "", "", false
	}
	return tenant, workspace, id, true
}

// resolveScope resolves the (tenant, workspace) a request targets: ?tenant=
// and ?workspace= override the principal's binding, and both must resolve to
// non-empty (400 "missing_scope" otherwise). A scoped principal cannot widen
// past its binding — naming another tenant or workspace is a 403
// "forbidden_scope", where action names the attempt ("act on", "read boards
// in"). Platform admins carry no binding and are exempt. Writes the error
// envelope and returns ok=false when the handler should stop.
func (h *HTTPGateway) resolveScope(rw http.ResponseWriter, r *http.Request, p core.Principal, action string) (tenant, workspace string, ok bool) {
	tenant = r.URL.Query().Get("tenant")
	workspace = r.URL.Query().Get("workspace")
	if tenant == "" {
		tenant = p.Tenant
	}
	if workspace == "" {
		workspace = p.Workspace
	}
	if tenant == "" || workspace == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_scope",
			"tenant and workspace required (no principal binding)")
		return "", "", false
	}
	if isPlatformAdmin(p) {
		return tenant, workspace, true
	}
	if p.Tenant != "" && tenant != p.Tenant {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot %s tenant %q (principal is bound to %q)", action, tenant, p.Tenant))
		return "", "", false
	}
	// p.Workspace is set on workspace-scoped keys but empty on tenant-admin
	// keys; the latter can act on any workspace in their tenant.
	if p.Workspace != "" && workspace != p.Workspace {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot %s workspace %q (principal is bound to %q)", action, workspace, p.Workspace))
		return "", "", false
	}
	return tenant, workspace, true
}

// resolveTenantWorkspaceScope is resolveScope for the workspace-scoped /me
// listings (listFlowsMe, suggestionsMe) and the /me/share handlers. Those
// service methods re-check with core.RequireWorkspace, so the guard here is
// defense in depth — but rejecting at the boundary keeps the rule in one
// place, so a future handler that reaches a store directly (as the board
// service does) can't silently inherit a cross-tenant read.
func (h *HTTPGateway) resolveTenantWorkspaceScope(rw http.ResponseWriter, r *http.Request, p core.Principal) (string, string, bool) {
	return h.resolveScope(rw, r, p, "act on")
}

// loadFlowForRequest resolves the {flow_id} path parameter, loads the flow's
// graph at ref, and returns the (tenant, workspace, id) plus the graph. On a
// bad flow_id it writes the readFlowID envelope; on a missing flow it writes
// a 404 "flow_not_found" with the storage-detail-free message. Returns
// ok=false when the handler should stop.
func (h *HTTPGateway) loadFlowForRequest(rw http.ResponseWriter, r *http.Request, p core.Principal, ref string) (tenant, workspace, id string, g core.Graph, ok bool) {
	tenant, workspace, id, ok = h.readFlowID(rw, r, p)
	if !ok {
		return "", "", "", core.Graph{}, false
	}
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ref)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return "", "", "", core.Graph{}, false
	}
	return tenant, workspace, id, g, true
}

// runStoreError maps a run-store error onto the right status: no run-log
// store wired is a 501 "not_configured" (the operator hasn't enabled
// persistent run logs); anything else is a 404 "run_not_found" (the run id is
// unknown or cross-tenant). Classifies on the sentinel, not on message text —
// the substring form it replaced would have turned any unrelated error that
// happened to contain "not configured" into a 501.
func runStoreError(rw http.ResponseWriter, err error) {
	if errors.Is(err, ErrRunLogsDisabled) {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", err.Error())
		return
	}
	writeAPIError(rw, http.StatusNotFound, "run_not_found", err.Error())
}

// --- /me/flows --------------------------------------------------------

// listRunLogsMe is GET /api/v1/me/runs/{run_id}/logs — a page of the
// run's persisted log. ?after= resumes from a seq cursor, ?limit= caps
// the page. The web run-detail page replays history with this; live
// tailing stays on the SSE events stream.
// deleteRunLogsMe erases one run's persisted logs (GDPR P2.1). Run logs
// can carry arbitrary personal data from a flow's payloads, so a user can
// delete them per-run without waiting for the retention sweep. Authorized
// like reading them: only the owning tenant's caller can.
func (h *HTTPGateway) deleteRunLogsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	n, err := h.svc.DeleteRunLog(r.Context(), p, runID)
	if err != nil {
		runStoreError(rw, err)
		return
	}
	h.audit(r.Context(), p, "run.logs_delete", runID, "deleted run logs (GDPR P2.1)")
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": n})
}

func (h *HTTPGateway) listRunLogsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := h.svc.RunLogPage(r.Context(), p, runID, after, limit)
	if err != nil {
		runStoreError(rw, err)
		return
	}
	if entries == nil {
		entries = []RunLogEntry{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"logs": entries})
}

func (h *HTTPGateway) listFlowsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// /me/flows accepts ?tenant= and ?workspace=, falling back to the
	// principal's binding. Web clients send them explicitly today; LLM
	// clients with a workspace-scoped key can omit them.
	tenant, workspace, ok := h.resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	summaries, err := h.svc.ListFlowSummaries(r.Context(), p, tenant, workspace)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"flows": summaries})
}

// suggestionsMe is GET /api/v1/me/flows/suggestions — the directed
// module co-occurrence mined from this workspace's own flows, used by the
// editor's drag-off-pin palette to surface "drops you usually wire next".
// Same ?tenant=/?workspace= fallback as listFlowsMe.
func (h *HTTPGateway) suggestionsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	items, err := h.svc.DropSuggestions(r.Context(), p, tenant, workspace)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if items == nil {
		items = []DropAdjacency{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPGateway) loadFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	_, _, _, g, ok := h.loadFlowForRequest(rw, r, p, r.URL.Query().Get("ref"))
	if !ok {
		return
	}
	writeJSON(rw, http.StatusOK, g)
}

// flowNotFoundMessage is the user-facing 404 message for a missing flow.
// It deliberately omits the git-backed store's internals (the commit hash,
// the word "graph", "file not found") that the raw LoadGraph error
// exposes — those are storage details a public API caller shouldn't see.
func flowNotFoundMessage(tenant, workspace, id string) string {
	return fmt.Sprintf("no flow %q in workspace %s/%s", id, tenant, workspace)
}

func (h *HTTPGateway) saveFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	g, ok := decodeRequestJSON[core.Graph](rw, r)
	if !ok {
		return
	}
	// Path is source-of-truth; ignore tenant/workspace/id in the body
	// even when the client supplied them.
	g.Tenant, g.Workspace, g.ID = tenant, workspace, id
	// ?autosave=1 marks an editor autosave: consecutive autosaves of this
	// flow coalesce into a single commit so the history stays readable.
	// Explicit saves (no param) always commit their own checkpoint.
	var commit string
	var err error
	if r.URL.Query().Get("autosave") == "1" {
		commit, err = h.svc.SaveGraphCoalescing(r.Context(), p, g)
	} else {
		commit, err = h.svc.SaveGraph(r.Context(), p, g)
	}
	if err != nil {
		if errors.Is(err, core.ErrConflict) {
			writeAPIError(rw, http.StatusConflict, "flow_locked", err.Error())
			return
		}
		writeAPIError(rw, http.StatusBadRequest, "save_failed", err.Error())
		return
	}
	// Audit action stays "graph.save" — it's an internal audit-trail
	// contract that downstream alerting may key on. The public rename
	// (graphs → flows) is wire-only; audit codes are stable.
	h.audit(r.Context(), p, "graph.save", g.ID, "commit="+commit)
	writeJSON(rw, http.StatusOK, h.flowMutationResponse(r, commit, g))
}

// flowMutationResponse is the shared response shape for save + patch.
// Includes a canvas URL the LLM can hand to the user ("open this to
// see what I built"), the trigger endpoints, and a flag indicating
// whether the operator has set --public-base-url. When the flag is
// false, the trigger URLs in `endpoints` are relative — the LLM
// should warn the user instead of telling them to paste the URL.
func (h *HTTPGateway) flowMutationResponse(r *http.Request, commit string, g core.Graph) map[string]any {
	scope := g.Tenant + "/" + g.Workspace + "/" + g.ID
	base := h.effectiveBaseURL(r)
	resp := map[string]any{
		"commit":                 commit,
		"flow_id":                scope,
		"lint":                   core.LintGraph(g),
		"endpoints":              h.triggerEndpoints(base, g),
		"public_base_configured": h.svc.PublicBaseURL != "",
	}
	// canvas_url is the deep link to the in-app editor for this flow.
	resp["canvas_url"] = base + "/flows/" + g.ID
	return resp
}

// effectiveBaseURL returns the origin to build user-facing URLs (trigger,
// hosted form, editor link) against. The operator's --public-base-url is
// authoritative when set. Otherwise we derive it from the request — honoring
// X-Forwarded-Proto/Host so a reverse proxy's external origin wins — so the
// trigger/form links a user gets are USABLE (absolute) instead of bare paths
// like "/form/...". The derived value is best-effort: behind a proxy that
// doesn't forward those headers it may be the internal host, which is why
// public_base_configured still reports whether the authoritative value is set.
func (h *HTTPGateway) effectiveBaseURL(r *http.Request) string {
	if b := strings.TrimRight(h.svc.PublicBaseURL, "/"); b != "" {
		return b
	}
	if r == nil {
		return ""
	}
	scheme := "http"
	if h.requestIsHTTPS(r) {
		scheme = "https"
	}
	// Only trust X-Forwarded-Host when the operator has opted into proxy
	// headers (DAZYFLOW_TRUST_PROXY_HEADERS), mirroring requestIsHTTPS. Without
	// the gate a client could set X-Forwarded-Host to reflect an attacker origin
	// back into the convenience URLs (canvas/trigger/share links) it receives.
	host := r.Host
	if h.TrustProxyHeaders {
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			host = fwd
		}
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// historyFlowMe is GET /me/flows/{flow_id}/history — the commit log of a
// flow, newest first, for the editor's version-history panel.
func (h *HTTPGateway) historyFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	revs, err := h.svc.FlowHistory(r.Context(), p, tenant, workspace, id, limit)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return
	}
	// Surface which revision is currently published so the history panel
	// can flag it (and offer "rollback to here" on the others). Best
	// effort: a lookup error just omits the marker rather than failing
	// the whole listing.
	resp := map[string]any{"revisions": revs}
	if info, perr := h.svc.PublishedInfo(r.Context(), p, tenant, workspace, id); perr == nil && info.Published {
		resp["published_commit"] = info.PublishedCommit
	}
	writeJSON(rw, http.StatusOK, resp)
}

// restoreFlowMe is POST /me/flows/{flow_id}/restore {ref} — make a past
// revision the new HEAD by saving its content as a fresh commit. History is
// preserved (no rewrite); a 409 means the flow is locked by an active run.
func (h *HTTPGateway) restoreFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	body, ok := decodeRequestJSON[struct {
		Ref string `json:"ref"`
	}](rw, r)
	if !ok {
		return
	}
	body.Ref = strings.TrimSpace(body.Ref)
	if body.Ref == "" {
		writeAPIError(rw, http.StatusBadRequest, "validation_failed", "ref is required")
		return
	}
	commit, g, err := h.svc.RestoreFlow(r.Context(), p, tenant, workspace, id, body.Ref)
	if err != nil {
		if errors.Is(err, core.ErrConflict) {
			writeAPIError(rw, http.StatusConflict, "flow_locked", err.Error())
			return
		}
		writeAPIError(rw, http.StatusBadRequest, "restore_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.restore", id, "from="+body.Ref+" commit="+commit)
	writeJSON(rw, http.StatusOK, h.flowMutationResponse(r, commit, g))
}

// duplicateFlowMe is POST /me/flows/{flow_id}/duplicate {name?} — create an
// independent copy of a flow. The copy gets a fresh ID (so fresh trigger URLs
// and an empty run history) and starts as a DISABLED draft owned by the
// caller; they review and enable it when ready. See Service.DuplicateGraph.
func (h *HTTPGateway) duplicateFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	body, ok := decodeRequestJSONOptional[struct {
		Name string `json:"name"`
	}](rw, r)
	if !ok {
		return
	}
	newID, g, commit, err := h.svc.DuplicateGraph(r.Context(), p, tenant, workspace, id, strings.TrimSpace(body.Name))
	if err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound):
			writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		case errors.Is(err, core.ErrPlanLimit):
			writeAPIError(rw, http.StatusForbidden, "plan_limit", err.Error())
		case errors.Is(err, core.ErrUnauthorized):
			writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		default:
			writeAPIError(rw, http.StatusBadRequest, "duplicate_failed", err.Error())
		}
		return
	}
	h.audit(r.Context(), p, "graph.duplicate", newID, "from="+id+" commit="+commit)
	writeJSON(rw, http.StatusCreated, h.flowMutationResponse(r, commit, g))
}

// labelRevisionMe is POST /me/flows/{flow_id}/label {ref?, label} — name a
// revision without publishing it. ref defaults to HEAD ("name my draft"); an
// older commit hash names that revision. An empty label clears the existing
// label. The label is keyed to the commit, so it persists across publishes
// and rollbacks. Gated on graph:admin inside the service.
func (h *HTTPGateway) labelRevisionMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	body, ok := decodeRequestJSONOptional[struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
	}](rw, r)
	if !ok {
		return
	}
	label := strings.TrimSpace(body.Label)
	commit, err := h.svc.LabelRevision(r.Context(), p, tenant, workspace, id, strings.TrimSpace(body.Ref), label)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
			return
		}
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.label", id, "commit="+commit+" label="+label)
	writeJSON(rw, http.StatusOK, map[string]any{
		"flow_id": tenant + "/" + workspace + "/" + id,
		"commit":  commit,
		"label":   label,
	})
}

// publishFlowMe is POST /me/flows/{flow_id}/publish {ref?} — promote a
// revision to "live". Automatic triggers run the published revision;
// manual + test runs keep using the draft (HEAD). ref defaults to HEAD
// ("publish my draft"); an older commit hash rolls back. Gated on
// graph:admin inside the service.
func (h *HTTPGateway) publishFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	var body struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
	}
	// Body is optional — an empty POST publishes HEAD with no label, and an
	// empty body decodes to io.EOF which we ignore. A *malformed* body is a
	// 400: swallowing it published HEAD instead of the ref the client asked
	// for, so a client with a serialization bug got a successful publish of
	// the wrong commit and only a server-side log line to say so.
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeAPIError(rw, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("could not read the request body: %v", err))
			return
		}
	}
	label := strings.TrimSpace(body.Label)
	commit, err := h.svc.PublishFlow(r.Context(), p, tenant, workspace, id, strings.TrimSpace(body.Ref), label)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
			return
		}
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	detail := "commit=" + commit
	if label != "" {
		detail += " label=" + label
	}
	h.audit(r.Context(), p, "graph.publish", id, detail)
	resp := map[string]any{
		"flow_id":          tenant + "/" + workspace + "/" + id,
		"published_commit": commit,
	}
	if label != "" {
		resp["published_label"] = label
	}
	writeJSON(rw, http.StatusOK, resp)
}

// unpublishFlowMe is POST /me/flows/{flow_id}/unpublish — clear the published
// pointer (the inverse of publish). Takes the flow fully offline: schedules,
// webhooks, hosted forms and provider events all refuse an unpublished flow.
// The draft is untouched. Gated on graph:admin inside the service; idempotent.
func (h *HTTPGateway) unpublishFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	if err := h.svc.UnpublishFlow(r.Context(), p, tenant, workspace, id); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
			return
		}
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.unpublish", id, "")
	writeJSON(rw, http.StatusOK, map[string]any{
		"flow_id":   tenant + "/" + workspace + "/" + id,
		"published": false,
	})
}

// publishedFlowMe is GET /me/flows/{flow_id}/published — the draft-vs-live
// state the editor's publish control renders (is there a published
// version, does the draft differ from it).
func (h *HTTPGateway) publishedFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	info, err := h.svc.PublishedInfo(r.Context(), p, tenant, workspace, id)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return
	}
	writeJSON(rw, http.StatusOK, info)
}

// triggerEndpoints returns the public URLs the user must paste into
// the upstream system (Stripe webhook UI, contact-form embed, etc.)
// to deliver events to a webhook-triggered or hosted-form flow.
// Returns an empty slice when the flow has no webhook/form triggers.
//
// Webhook trigger → `/trigger/<tenant>/<workspace>/<id>` (POST).
// Hosted form → `/form/<tenant>/<workspace>/<id>` (GET renders, POST submits).
// Same flow can have both on a single trigger when PublicForm is true.
//
// Surfaces the secret (Authorization: Bearer header) for webhook
// triggers when one is set on the trigger — the LLM can include it
// in the "how to wire this up" instructions to the user. PublicForm
// pages take no auth.
func (h *HTTPGateway) triggerEndpoints(base string, g core.Graph) []map[string]any {
	base = strings.TrimRight(base, "/")
	out := []map[string]any{}
	scope := g.Tenant + "/" + g.Workspace + "/" + g.ID
	// Trigger config lives on nodes now (the Triggers menu is gone): the
	// webhook_input node carries the secret + hosted-form opt-in, and the
	// Schedule/Poll nodes carry their schedule.
	for _, n := range g.Nodes {
		switch n.Module {
		case webhookInputModuleID:
			ep := map[string]any{
				"kind":   "webhook",
				"method": "POST",
				"url":    base + "/trigger/" + scope,
			}
			if keys := core.WebhookSecrets(n.Params); len(keys) > 0 {
				// Any active key authenticates; show the first.
				ep["auth"] = "Authorization: Bearer " + keys[0]
			}
			out = append(out, ep)
			if pf, _ := n.Params["public_form"].(bool); pf {
				out = append(out, map[string]any{
					"kind":   "hosted_form",
					"method": "GET (renders) / POST (submits)",
					"url":    base + "/form/" + scope,
					"note":   "Public page — possession of the URL is the only credential.",
				})
			}
		case "cron_trigger":
			if c, _ := n.Params["cron"].(string); c != "" {
				out = append(out, map[string]any{
					"kind": "cron",
					"cron": c,
					"note": "Server-side scheduler; no public URL.",
				})
			}
		case "poll_trigger":
			if secs := paramSeconds(n.Params, "interval_seconds"); secs > 0 {
				out = append(out, map[string]any{
					"kind":             "poll",
					"interval_seconds": secs,
					"note":             "Server-side scheduler; no public URL.",
				})
			}
		}
	}
	// Legacy graph-level cron triggers still fire, so surface them too.
	for _, t := range g.Triggers {
		if t.Type == "cron" && t.Cron != "" {
			out = append(out, map[string]any{
				"kind": "cron",
				"cron": t.Cron,
				"note": "Server-side scheduler; no public URL.",
			})
		}
	}
	return out
}

// enableFlowMe / disableFlowMe are POST /me/flows/{flow_id}/enable
// and /disable. Idempotent — pressing "enable" twice succeeds. The
// Disabled bool lives on the saved graph; toggling it produces a new
// git commit, so the action shows up in the workspace history.
func (h *HTTPGateway) enableFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setFlowEnabled(rw, r, p, true)
}
func (h *HTTPGateway) disableFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setFlowEnabled(rw, r, p, false)
}
func (h *HTTPGateway) setFlowEnabled(rw http.ResponseWriter, r *http.Request, p core.Principal, enabled bool) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	commit, err := h.svc.SetFlowEnabled(r.Context(), p, tenant, workspace, id, enabled)
	if err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	action := "graph.disable"
	if enabled {
		action = "graph.enable"
	}
	h.audit(r.Context(), p, action, id, "commit="+commit)
	writeJSON(rw, http.StatusOK, map[string]any{
		"flow_id": tenant + "/" + workspace + "/" + id,
		"enabled": enabled,
		"commit":  commit,
	})
}

// deleteFlowMe is the DELETE /me/flows/{flow_id} handler. Idempotent:
// missing flow → 204. Active run → 409 with code `flow_locked`.
//
// Deleting a flow is irreversible (it drops the flow's whole Git history), so
// it carries a second gate beyond the graph:edit DeleteGraph enforces — but
// WHICH gate depends on the credential kind, because the two credentials fail
// in different ways:
//
//   - A session is ambient: it rides on a cookie, so a hijacked session could
//     wipe work with no further proof of identity. Those callers re-supply
//     their account password, bcrypt-verified here so the gate can't be
//     bypassed by calling the API directly (mirrors changePasswordHandler).
//   - An API key is not ambient: it is minted deliberately, carries its own
//     capped role set, is revocable, is audited, and is unreachable by CSRF.
//     Its holder — dzctl, a script, the MCP server — has NO password to
//     re-supply, so demanding one didn't make deletion safer, it made it
//     impossible: every key-authenticated DELETE answered 401, which is why
//     the MCP `delete_flow` tool could never succeed. Keys are held to
//     graph:admin instead, which the deliberately narrow `claude-mcp` default
//     role (graph:run + graph:edit — see defaultSelfIssueRole) does NOT carry.
//     So an agent's key still cannot destroy a flow's history; a human deletes
//     from the web UI, or mints a key that says out loud it may delete.
func (h *HTTPGateway) deleteFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	via := "session"
	if auth.IsAPIKeyCredential(credentialFromRequest(r)) {
		via = "api_key"
		if err := core.Require(p, core.PermGraphAdmin); err != nil {
			writeAPIError(rw, http.StatusForbidden, "admin_scope_required",
				"this API key may not delete flows: deleting is permanent (it drops the flow's history), so a key needs the graph:admin permission. "+
					"The default MCP key carries graph:run + graph:edit only — delete the flow from the web UI, or mint a key with graph:admin.")
			return
		}
	} else {
		if h.Users == nil {
			writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password auth not configured")
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		// An empty/invalid body falls through to the password check below,
		// which rejects a blank password — no separate decode-error path
		// needed.
		_ = json.NewDecoder(r.Body).Decode(&body)
		email := strings.ToLower(strings.TrimSpace(p.Subject))
		if _, err := auth.VerifyPassword(r.Context(), h.Users, email, body.Password); err != nil {
			writeAPIError(rw, http.StatusUnauthorized, "bad_credentials", "password is incorrect")
			return
		}
	}
	if err := h.svc.DeleteGraph(r.Context(), p, tenant, workspace, id); err != nil {
		if errors.Is(err, core.ErrConflict) {
			writeAPIError(rw, http.StatusConflict, "flow_locked", err.Error())
			return
		}
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.delete", id, "via="+via)
	rw.WriteHeader(http.StatusNoContent)
}

// patchFlowMe is the RFC 7396 JSON Merge Patch entry point. The
// request body is a partial Graph document; we load HEAD, apply the
// merge, save the result. Convenience for an LLM building a flow
// incrementally without re-uploading the whole graph each turn.
//
// Conflict semantics match SaveGraph: 409 when a run is in flight on
// this flow. Validation runs on the merged graph (not the patch) so
// the user sees errors against the actual saved shape.
func (h *HTTPGateway) patchFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	patch, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "read_failed", "read body: "+err.Error())
		return
	}
	var patchDoc map[string]any
	if err := json.Unmarshal(patch, &patchDoc); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "patch must be a JSON object: "+err.Error())
		return
	}

	current, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", err.Error())
		return
	}
	// Round-trip current → map for the merge, then back to Graph after.
	// Doing the merge on map[string]any lets us reuse a generic merge
	// function rather than hand-writing per-field merge logic for every
	// Graph field.
	currentJSON, err := json.Marshal(current)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "marshal current: "+err.Error())
		return
	}
	var currentDoc map[string]any
	if err := json.Unmarshal(currentJSON, &currentDoc); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	merged := jsonMergePatch(currentDoc, patchDoc)
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "marshal merged: "+err.Error())
		return
	}
	var next core.Graph
	if err := json.Unmarshal(mergedJSON, &next); err != nil {
		writeAPIError(rw, http.StatusUnprocessableEntity, "validation_failed",
			"merged graph is not a valid Graph: "+err.Error())
		return
	}
	next.Tenant, next.Workspace, next.ID = tenant, workspace, id
	commit, err := h.svc.SaveGraph(r.Context(), p, next)
	if err != nil {
		if errors.Is(err, core.ErrConflict) {
			writeAPIError(rw, http.StatusConflict, "flow_locked", err.Error())
			return
		}
		writeAPIError(rw, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "graph.patch", next.ID, "commit="+commit)
	writeJSON(rw, http.StatusOK, h.flowMutationResponse(r, commit, next))
}

// jsonMergePatch applies RFC 7396 merge semantics: for each key in
// the patch, replace the target's value with the patch's value;
// null values delete the target's key; objects merge recursively;
// arrays + primitives replace wholesale.
//
// 30 lines of plain Go beats pulling in a dependency for this — we
// only need merge semantics on whole-document shapes, not the more
// complex JSON Patch (RFC 6902) operations.
func jsonMergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		if v == nil {
			delete(target, k)
			continue
		}
		if subPatch, ok := v.(map[string]any); ok {
			if subTarget, ok := target[k].(map[string]any); ok {
				target[k] = jsonMergePatch(subTarget, subPatch)
				continue
			}
			// Patch is object, target was not — replace.
			target[k] = jsonMergePatch(map[string]any{}, subPatch)
			continue
		}
		target[k] = v
	}
	return target
}

func (h *HTTPGateway) runFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	// Reuse the existing runGraph plumbing by spoofing the path values
	// the legacy handler reads. Cheaper than a parallel runGraph variant
	// and keeps the lifecycle/audit/notification wiring single-source.
	q := r.URL.Query()
	q.Set("tenant", tenant)
	q.Set("workspace", workspace)
	q.Set("id", id)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	// Pre-check existence with a clean 404 before delegating: the legacy
	// runGraph surfaces the raw store error ("graph \"x\" at <commit>: file
	// not found"), which leaks the git-backed storage internals. A clean
	// flow_not_found here short-circuits that.
	if _, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ""); err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return
	}
	h.runGraph(rw, r2, p)
}

// resetNodeStateMe clears the persisted per-node state of one node — the
// dedupe cursor / poll watermark / cache that the editor surfaces via
// Manifest.NodeState. It's the first-class replacement for the old
// "delete the drop and re-add it so the key changes" workaround: same node
// id, memory wiped, so the next run baselines afresh. The reserved key(s) to
// delete come from the drop's own registration (engine.StateResetKeys), so the
// daemon never hard-codes a key format. Gated on graph:edit (a state mutation,
// not just a run).
func (h *HTTPGateway) resetNodeStateMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	if !h.requireSecretStore(rw, p) {
		return
	}
	tenant, _, id, g, ok := h.loadFlowForRequest(rw, r, p, "")
	if !ok {
		return
	}
	nodeID := r.PathValue("node_id")
	var module string
	found := false
	for _, n := range g.Nodes {
		if n.ID == nodeID {
			module, found = n.Module, true
			break
		}
	}
	if !found {
		writeAPIError(rw, http.StatusNotFound, "node_not_found",
			fmt.Sprintf("no node %q in flow %q", nodeID, id))
		return
	}
	keys := engine.StateResetKeys(module, id, nodeID)
	if len(keys) == 0 {
		writeAPIError(rw, http.StatusBadRequest, "no_resettable_state",
			fmt.Sprintf("node %q (%s) keeps no resettable state", nodeID, module))
		return
	}
	// Delete each reserved key. A missing key is not an error — resetting an
	// already-clear node (never run, or reset twice) is a no-op success.
	cleared := 0
	for _, k := range keys {
		if err := h.EncryptedSecrets.Delete(r.Context(), tenant, k); err != nil {
			if errors.Is(err, ErrSecretNotFound) {
				continue
			}
			writeAPIError(rw, http.StatusInternalServerError, "reset_failed", err.Error())
			return
		}
		cleared++
	}
	h.audit(r.Context(), p, "flow.node.reset_state", id+"/"+nodeID, module)
	writeJSON(rw, http.StatusOK, map[string]any{"reset": true, "node_id": nodeID, "cleared": cleared})
}

func (h *HTTPGateway) testTriggerFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	h.testTrigger(rw, r2, p)
}

func (h *HTTPGateway) sampleFlowNodeMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	// nodeID rides through unchanged (mux already extracted it).
	r2.SetPathValue("nodeID", r.PathValue("node_id"))
	h.sampleNode(rw, r2, p)
}

func (h *HTTPGateway) listFlowRunsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	h.listRuns(rw, r2, p)
}

// validateGraphLiteral is POST /api/v1/validate/graph. Body is a
// Graph document; we lint it without touching the store. Lets an LLM
// compose a graph in chat, dry-run it, and only call create_flow
// when the lint is clean. Distinct from validateFlowMe which lints
// the HEAD of an already-saved flow.
func (h *HTTPGateway) validateGraphLiteral(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	g, ok := decodeRequestJSON[core.Graph](rw, r)
	if !ok {
		return
	}
	// Workspace scoping isn't required since we're not touching the
	// store, but stamp the principal's scope onto the graph so lint
	// rules that reference (tenant, workspace) behave as the saved
	// flow would. Caller can override these in the body if they need
	// to lint as a different scope.
	if g.Tenant == "" {
		g.Tenant = p.Tenant
	}
	if g.Workspace == "" {
		g.Workspace = p.Workspace
	}
	// Drop obsolete edges (e.g. folded-away `headers` wires) before validating,
	// so a graph authored against an older model lints against the current one.
	g = core.MigrateGraph(g)
	// Run the SAME two gates the AI generator applies (core.ValidateGraphFull:
	// the security/placeholder linter PLUS the manifest-level structural
	// validator) so a graph an MCP host hand-authors here validates identically
	// to one the server LLM drafts. Loading the catalog is best-effort — if it's
	// unavailable, ValidateGraphFull degrades to LintGraph-only.
	manifests, _ := h.svc.ListDrops(r.Context(), p)
	issues := core.ValidateGraphFull(g, manifests)
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":     !hasLintError(issues),
		"issues": issues,
	})
}

// validateFlowMe is the spec's /me/flows/{flow_id}/validate — lint
// without saving. The current daemon doesn't have a save-less linter
// over a remote graph (only LintGraph on a local Graph object), so
// the flow must already exist; we load HEAD and lint.
func (h *HTTPGateway) validateFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", err.Error())
		return
	}
	issues := core.LintGraph(g)
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":     !hasLintError(issues),
		"issues": issues,
	})
}

// hasLintError reports whether any issue in xs is a hard error (vs an
// advisory warning). The validate endpoints flip `ok` to false on the
// first error so callers can gate "run" / "publish" on a clean slate
// while still tolerating warn-level findings.
func hasLintError(xs []core.LintIssue) bool {
	for _, x := range xs {
		if x.Severity == core.LintError {
			return true
		}
	}
	return false
}

// --- /me/connections --------------------------------------------------

// listConnectionsMe is GET /api/v1/me/connections — the LLM-friendly
// shape of "which OAuth providers does the daemon offer + which has
// this caller connected." Delegates to the legacy oauthListProviders
// handler since the underlying logic is identical.
func (h *HTTPGateway) listConnectionsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.oauthListProviders(rw, r, p)
}

// startConnectionMe is POST /api/v1/me/connections/{provider}/authorize.
// Unlike the legacy /api/v1/oauth/{provider}/authorize (which 302s the
// browser straight to the provider), this returns JSON
// `{"authorize_url":"..."}` so an LLM client can hand the URL to the
// user and have them open it manually. The provider's callback still
// lands at /api/v1/oauth/{provider}/callback (one endpoint, one shape)
// and finalizes the connection.
//
// Accepts ?account= (defaults "default") and ?return_to= (defaults
// /integrations) — same semantics as the legacy redirect path.
func (h *HTTPGateway) startConnectionMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	provider := r.PathValue("provider")
	// No browser binding here: the JSON authorize URL is handed to another
	// agent/browser to open, so a cookie set on this response wouldn't be
	// present at the callback. The unguessable single-use state still applies.
	target, status, msg := h.buildAuthorizeURL(p,
		provider,
		r.URL.Query().Get("account"),
		r.URL.Query().Get("return_to"),
		scopeSubsetForIntegration(provider, r.URL.Query().Get("integration")),
		"",
	)
	if status != http.StatusOK {
		// Code mapping: 501 = OAuth subsystem not configured;
		// 503 = a known provider exists but its client_id/secret are
		// not wired (operator config gap); 403 = principal can't act;
		// 404 = no such provider; 400 = bad input.
		writeAPIError(rw, status, oauthErrorCode(status), msg)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"authorize_url": target})
}

// disconnectConnectionMe is the inverse of the connect/authorize flow:
// it deletes the stored oauth.<provider>.<account> token for the
// caller's tenant, so flows stop using it and the Connections page shows
// the account disconnected. Account defaults to "default"; idempotent.
//
// This forgets the token locally; it does not revoke the grant at the
// provider (the user can also remove access in the provider's own
// account settings). Gated on secret:write, the same permission the
// connect flow requires.
func (h *HTTPGateway) disconnectConnectionMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	provider := r.PathValue("provider")
	// Disconnecting forgets the stored token. Base bar is secret:write —
	// except Google, an org-shared credential managed by org admins on the
	// /admin/google page; disconnecting affects everyone's flows, so it takes
	// the same org-admin bar as connecting (see buildAuthorizeURL).
	if provider == "google" {
		if !core.CanAdminOrg(p) {
			writeAPIError(rw, http.StatusForbidden, "forbidden", "disconnecting a Google account requires organization:admin")
			return
		}
	} else if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	if !h.requireSecretStore(rw, p) {
		return
	}
	if providerDefault(provider) == nil {
		writeAPIError(rw, http.StatusNotFound, "unknown_provider",
			fmt.Sprintf("unknown OAuth provider %q", provider))
		return
	}
	account := r.URL.Query().Get("account")
	if account == "" {
		account = "default"
	}
	name := secretNameFor(provider, account)
	if err := h.EncryptedSecrets.Delete(r.Context(), p.Tenant, name); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "oauth.connection.disconnect", name, "")
	rw.WriteHeader(http.StatusNoContent)
}

func oauthErrorCode(status int) string {
	switch status {
	case http.StatusNotImplemented:
		return "oauth_not_configured"
	case http.StatusServiceUnavailable:
		return "provider_not_configured"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "provider_not_found"
	case http.StatusBadRequest:
		return "invalid_request"
	default:
		return "internal_error"
	}
}

// --- /me/runs ---------------------------------------------------------

// runView is the public, stable shape of a graph run for the /me/runs
// endpoints — the implementation of the OpenAPI `Run` schema. The legacy
// /api/v1/graphs + /api/v1/jobs inspector routes still serialize the raw
// core.JobRecord (the web UI inspector depends on that shape, GraphPayload
// and all); runView is the clean, snake_case, storage-detail-free view the
// public API documents and promises. `enqueued_at` + `graph_id` are kept
// for parity with the list shape (runSummary).
type runView struct {
	ID         string         `json:"id"`
	FlowID     string         `json:"flow_id"` // tenant/workspace/graph_id composite
	GraphID    string         `json:"graph_id"`
	Status     core.JobStatus `json:"status"`
	EnqueuedAt time.Time      `json:"enqueued_at"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Error      *core.JobError `json:"error,omitempty"`
}

// nodeRunView is the public shape of a single node-record within a run —
// the OpenAPI `NodeRun` schema: status, timing, attempts, the inputs it
// received, the outputs it emitted, and its structured error if any. No
// internal/storage fields.
type nodeRunView struct {
	NodeID     string              `json:"node_id"`
	Status     core.JobStatus      `json:"status"`
	Attempts   int                 `json:"attempts,omitempty"`
	StartedAt  *time.Time          `json:"started_at,omitempty"`
	FinishedAt *time.Time          `json:"finished_at,omitempty"`
	DurationMS int64               `json:"duration_ms,omitempty"`
	Inputs     map[string]core.Ref `json:"inputs,omitempty"`
	Outputs    map[string]core.Ref `json:"outputs,omitempty"`
	Error      *core.JobError      `json:"error,omitempty"`
	// WillRetry + RetryAt expose the engine's auto-retry state so the run
	// viewer can say "retrying — next attempt in Ns" instead of leaving a
	// node that's between attempts looking stuck. Set only while a node is
	// requeued for a future retry (an earlier attempt failed transiently);
	// a terminally failed node leaves them unset, which the UI reads as
	// "needs you". See newNodeRunView.
	WillRetry bool       `json:"will_retry,omitempty"`
	RetryAt   *time.Time `json:"retry_at,omitempty"`
}

// sseTerminalView is the clean payload of the `terminal` SSE frame on the
// /me/runs/{id}/events stream — run_id + final status + structured error.
// Replaces the raw TerminalEvent (PascalCase JobID/GraphRes) that used to
// be serialized straight onto the wire.
type sseTerminalView struct {
	RunID  string         `json:"run_id"`
	Status core.JobStatus `json:"status"`
	Error  *core.JobError `json:"error,omitempty"`
}

func newSSETerminalView(ev *TerminalEvent) sseTerminalView {
	return sseTerminalView{RunID: ev.JobID, Status: ev.Status, Error: ev.Error}
}

// durationMS returns the run/node wall-clock in milliseconds when both
// ends are known, else 0 (omitted by the DTO's omitempty).
func durationMS(start, end *time.Time) int64 {
	if start == nil || end == nil {
		return 0
	}
	return end.Sub(*start).Milliseconds()
}

// resultError returns the structured error from a result, or nil. The
// {code, message, details} shape is what machine clients branch on and
// what the run-detail UI renders.
func resultError(res *core.Result) *core.JobError {
	if res == nil {
		return nil
	}
	return res.Error
}

func newRunView(rec core.JobRecord) runView {
	// Graph-records don't carry a distinct started_at (only node-records
	// do), so the run's end-to-end duration is enqueue → finish.
	runStart := rec.StartedAt
	if runStart == nil {
		runStart = &rec.EnqueuedAt
	}
	return runView{
		ID:         rec.ID,
		FlowID:     rec.Tenant + "/" + rec.Workspace + "/" + rec.GraphID,
		GraphID:    rec.GraphID,
		Status:     rec.Status,
		EnqueuedAt: rec.EnqueuedAt,
		StartedAt:  rec.StartedAt,
		FinishedAt: rec.FinishedAt,
		DurationMS: durationMS(runStart, rec.FinishedAt),
		Error:      resultError(rec.Result),
	}
}

func newNodeRunView(rec core.JobRecord) nodeRunView {
	v := nodeRunView{
		NodeID:     rec.NodeID,
		Status:     rec.Status,
		Attempts:   rec.Attempt,
		StartedAt:  rec.StartedAt,
		FinishedAt: rec.FinishedAt,
		DurationMS: durationMS(rec.StartedAt, rec.FinishedAt),
		Inputs:     rec.Job.Input,
		Error:      resultError(rec.Result),
	}
	if rec.Result != nil {
		v.Outputs = rec.Result.Output
	}
	// A node between attempts is requeued: queued, with a future availability
	// horizon and a non-zero attempt count (the worker's maybeScheduleRetry
	// set AvailableAt = now + backoff). Surface that as an explicit auto-retry
	// signal so the UI distinguishes "the engine will try again at T" from a
	// terminal failure the user must act on.
	if rec.Status == core.JobStatusQueued && rec.Attempt > 0 && rec.AvailableAt != nil {
		v.WillRetry = true
		v.RetryAt = rec.AvailableAt
	}
	return v
}

// loadRunScoped fetches a run-record by id and enforces the caller's
// tenant scope. A missing run and a cross-tenant run both report 404, so
// run existence never leaks across tenants. On failure it writes the
// structured error and returns ok=false.
func (h *HTTPGateway) loadRunScoped(rw http.ResponseWriter, r *http.Request, p core.Principal, runID string) (core.JobRecord, bool) {
	rec, err := h.svc.GetJob(r.Context(), p, runID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrUnauthorized) {
			writeAPIError(rw, http.StatusNotFound, "run_not_found", "no run with that id")
			return core.JobRecord{}, false
		}
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return core.JobRecord{}, false
	}
	return rec, true
}

func (h *HTTPGateway) listRunsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.listAllRuns(rw, r, p)
}

func (h *HTTPGateway) getRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	rec, ok := h.loadRunScoped(rw, r, p, r.PathValue("run_id"))
	if !ok {
		return
	}
	writeJSON(rw, http.StatusOK, newRunView(rec))
}

func (h *HTTPGateway) listRunNodesMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	runRec, ok := h.loadRunScoped(rw, r, p, runID)
	if !ok {
		return
	}
	nodes, err := h.svc.Jobs.ListNodeRecords(r.Context(), core.ListNodeRecordsOpts{
		Tenant:     runRec.Tenant,
		Workspace:  runRec.Workspace,
		GraphRunID: runID,
		Limit:      1000, // typical graphs have <100 nodes; cap defensively
	})
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]nodeRunView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, newNodeRunView(n))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"nodes": out})
}

func (h *HTTPGateway) getRunNodeMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	// The run-record scope check gates access to its node records.
	if _, ok := h.loadRunScoped(rw, r, p, runID); !ok {
		return
	}
	nodeRec, err := h.svc.Jobs.Get(r.Context(), NodeJobID(runID, r.PathValue("node_id")))
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "node_not_found", "no such node in this run")
			return
		}
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, newNodeRunView(nodeRec))
}

func (h *HTTPGateway) runEventsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("jobID", r.PathValue("run_id"))
	h.jobEvents(rw, r2, p)
}

func (h *HTTPGateway) cancelRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("runID", r.PathValue("run_id"))
	h.cancelRun(rw, r2, p)
}

func (h *HTTPGateway) resumeRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("runID", r.PathValue("run_id"))
	h.resumeRun(rw, r2, p)
}

// retryRunMe is POST /api/v1/me/runs/{run_id}/retry — resume a failed run
// from where it failed, reusing the work that already succeeded. Returns
// the new run's id (same shape as a fresh submission). 409 when the run is
// still in progress, 404 when it doesn't exist or isn't a graph run.
func (h *HTTPGateway) retryRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	newRunID, err := h.svc.ResumeFailedRun(r.Context(), p, runID)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound), errors.Is(err, core.ErrUnauthorized):
			writeAPIError(rw, http.StatusNotFound, "run_not_found", "no run with that id")
		case errors.Is(err, core.ErrConflict):
			writeAPIError(rw, http.StatusConflict, "run_not_retryable", err.Error())
		case errors.Is(err, core.ErrPlanLimit):
			writeAPIError(rw, http.StatusPaymentRequired, "plan_limit", err.Error())
		default:
			writeAPIError(rw, http.StatusBadRequest, "retry_failed", err.Error())
		}
		return
	}
	h.audit(r.Context(), p, "graph.run", newRunID, "retry-of="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": newRunID})
}
