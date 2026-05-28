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
// hz-mcp have both migrated.

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
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

// --- /me/flows --------------------------------------------------------

func (h *HTTPGateway) listFlowsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// /me/flows accepts ?tenant= and ?workspace=, falling back to the
	// principal's binding. Web clients send them explicitly today; LLM
	// clients with a workspace-scoped key can omit them.
	tenant := r.URL.Query().Get("tenant")
	workspace := r.URL.Query().Get("workspace")
	if tenant == "" {
		tenant = p.Tenant
	}
	if workspace == "" {
		workspace = p.Workspace
	}
	if tenant == "" || workspace == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_scope",
			"tenant and workspace required (no principal binding)")
		return
	}
	summaries, err := h.svc.ListFlowSummaries(r.Context(), p, tenant, workspace)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Emit both `flows` (canonical) and `graphs` (legacy alias) so the
	// web client can use whichever key it reads first during the
	// transition. The legacy key disappears with the old route.
	writeJSON(rw, http.StatusOK, map[string]any{"flows": summaries, "graphs": summaries})
}

func (h *HTTPGateway) loadFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, r.URL.Query().Get("ref"))
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, g)
}

func (h *HTTPGateway) saveFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	var g core.Graph
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return
	}
	// Path is source-of-truth; ignore tenant/workspace/id in the body
	// even when the client supplied them.
	g.Tenant, g.Workspace, g.ID = tenant, workspace, id
	commit, err := h.svc.SaveGraph(r.Context(), p, g)
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
	writeJSON(rw, http.StatusOK, map[string]any{
		"commit":   commit,
		"flow_id":  tenant + "/" + workspace + "/" + g.ID,
		"graph_id": g.ID, // legacy alias
		"lint":     core.LintGraph(g),
	})
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
	writeJSON(rw, http.StatusOK, map[string]any{
		"commit":  commit,
		"flow_id": tenant + "/" + workspace + "/" + next.ID,
		"lint":    core.LintGraph(next),
	})
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
	h.runGraph(rw, r2, p)
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
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":     true,
		"issues": core.LintGraph(g),
	})
}

// --- /me/runs ---------------------------------------------------------

func (h *HTTPGateway) listRunsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.listAllRuns(rw, r, p)
}

func (h *HTTPGateway) getRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("jobID", r.PathValue("run_id"))
	h.jobSnapshot(rw, r2, p)
}

func (h *HTTPGateway) listRunNodesMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("jobID", r.PathValue("run_id"))
	h.listRunNodes(rw, r2, p)
}

func (h *HTTPGateway) getRunNodeMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("jobID", r.PathValue("run_id"))
	r2.SetPathValue("nodeID", r.PathValue("node_id"))
	h.nodeSnapshot(rw, r2, p)
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
