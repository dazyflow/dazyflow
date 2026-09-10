// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The spec-aligned wire shapes; the legacy routes remain for older clients.

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

type flowAPI struct {
	auditor
	flowLoader
	urlBuilder
	svc              *Service
	Users            auth.UserStore
	EncryptedSecrets *EncryptedSecrets
	runCtl           *runCtlAPI
	secrets          *secretsAPI
	oauth            *oauthAPI
	noCompression    bool
}

func (h *HTTPGateway) flowAPI() *flowAPI {
	return &flowAPI{auditor: h.auditor(), flowLoader: h.flows(), urlBuilder: h.urls(), svc: h.svc, Users: h.Users, EncryptedSecrets: h.EncryptedSecrets, runCtl: h.runCtlAPI(), secrets: h.secretsAPI(), oauth: h.oauthAPI(), noCompression: h.DisableCompression}
}

// Inner slashes arrive %2F-encoded, so the composite stays one mux segment.
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
		return "", "", "", fmt.Errorf("cannot act on workspace %q (principal is bound to %q)", workspace, p.Workspace)
	}
	return tenant, workspace, id, nil
}

func readFlowID(rw http.ResponseWriter, r *http.Request, p core.Principal) (string, string, string, bool) {
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

// A non-platform-admin is pinned to their own tenant whatever they ask for.
func resolveScope(rw http.ResponseWriter, r *http.Request, p core.Principal, action string) (tenant, workspace string, ok bool) {
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
	if p.Workspace != "" && workspace != p.Workspace {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot %s workspace %q (principal is bound to %q)", action, workspace, p.Workspace))
		return "", "", false
	}
	return tenant, workspace, true
}

func resolveTenantWorkspaceScope(rw http.ResponseWriter, r *http.Request, p core.Principal) (string, string, bool) {
	return resolveScope(rw, r, p, "act on")
}

// A deployment with no run-log store is 501, not 404.
func runStoreError(rw http.ResponseWriter, err error) {
	if errors.Is(err, ErrRunLogsDisabled) {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", err.Error())
		return
	}
	writeAPIError(rw, http.StatusNotFound, "run_not_found", err.Error())
}

func (h *flowAPI) deleteRunLogsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	n, err := h.svc.DeleteRunLog(r.Context(), p, runID)
	if err != nil {
		runStoreError(rw, err)
		return
	}
	h.audit(r.Context(), p, "run.logs_delete", runID, "deleted run logs (GDPR P2.1)")
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": n})
}

func (h *flowAPI) listRunLogsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *flowAPI) listFlowsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
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

func (h *flowAPI) suggestionsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
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

func (h *flowAPI) loadFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	_, _, _, g, ok := h.loadFlowForRequest(rw, r, p, r.URL.Query().Get("ref"))
	if !ok {
		return
	}
	writeJSON(rw, http.StatusOK, g)
}

// Must not disclose whether a flow exists but is invisible to this caller.
func flowNotFoundMessage(tenant, workspace, id string) string {
	return fmt.Sprintf("no flow %q in workspace %s/%s", id, tenant, workspace)
}

func (h *flowAPI) saveFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	g, ok := decodeRequestJSON[core.Graph](rw, r)
	if !ok {
		return
	}
	g.Tenant, g.Workspace, g.ID = tenant, workspace, id
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
	h.audit(r.Context(), p, "graph.save", g.ID, "commit="+commit)
	writeJSON(rw, http.StatusOK, h.flowMutationResponse(r, p, commit, g))
}

func (h *flowAPI) flowMutationResponse(r *http.Request, p core.Principal, commit string, g core.Graph) map[string]any {
	scope := g.Tenant + "/" + g.Workspace + "/" + g.ID
	base := h.effectiveBaseURL(r)
	resp := map[string]any{
		"commit":                 commit,
		"flow_id":                scope,
		"lint":                   h.lintGraph(r, p, g),
		"endpoints":              h.triggerEndpoints(base, g),
		"public_base_configured": h.svc.PublicBaseURL != "",
	}
	resp["canvas_url"] = base + "/flows/" + g.ID
	return resp
}

// lintGraph is the lint a save returns: LintGraph plus the catalog-aware
// WIRING warnings.
//
// The wiring rules are the ones that need to know what a step's ports are, and
// their absence is felt: a list wired into a single-item input saved clean, and
// the author found out at run time with "can't evaluate field X in type
// interface {}" — a message that names neither the edge nor the step that made
// it. many_into_one names both.
//
// Structural errors are deliberately not here; see WiringWarnings. A catalog
// that cannot be read degrades to LintGraph rather than failing the save.
// validateGraph answers an explicit "is this sound?" and so runs everything,
// structural errors included — unlike a save, which only reports on the wiring.
func (h *flowAPI) validateGraph(r *http.Request, p core.Principal, g core.Graph) []core.LintIssue {
	manifests, err := h.svc.ListDrops(r.Context(), p)
	if err != nil {
		return core.LintGraph(g)
	}
	return core.ValidateGraphFull(g, manifests)
}

func (h *flowAPI) lintGraph(r *http.Request, p core.Principal, g core.Graph) []core.LintIssue {
	manifests, err := h.svc.ListDrops(r.Context(), p)
	if err != nil {
		return core.LintGraph(g)
	}
	return append(core.LintGraph(g), core.WiringWarnings(g, manifests)...)
}

func (h *flowAPI) historyFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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
	resp := map[string]any{"revisions": revs}
	if info, perr := h.svc.PublishedInfo(r.Context(), p, tenant, workspace, id); perr == nil && info.Published {
		resp["published_commit"] = info.PublishedCommit
	}
	writeJSON(rw, http.StatusOK, resp)
}

func (h *flowAPI) restoreFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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
	writeJSON(rw, http.StatusOK, h.flowMutationResponse(r, p, commit, g))
}

func (h *flowAPI) duplicateFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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
	writeJSON(rw, http.StatusCreated, h.flowMutationResponse(r, p, commit, g))
}

func (h *flowAPI) labelRevisionMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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

func (h *flowAPI) publishFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	var body struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
	}
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

func (h *flowAPI) unpublishFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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

func (h *flowAPI) publishedFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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

// Built from the public base URL, which is what a third party can actually reach.
func (h *flowAPI) triggerEndpoints(base string, g core.Graph) []map[string]any {
	base = strings.TrimRight(base, "/")
	out := []map[string]any{}
	scope := g.Tenant + "/" + g.Workspace + "/" + g.ID
	for _, n := range g.Nodes {
		switch n.Module {
		case webhookInputModuleID:
			ep := map[string]any{
				"kind":   "webhook",
				"method": "POST",
				"url":    base + "/trigger/" + scope,
			}
			if keys := core.WebhookSecrets(n.Params); len(keys) > 0 {
				ep["auth"] = "Authorization: Bearer " + keys[0]
				ep["url_with_key"] = base + "/trigger/" + scope + "?key=" + url.QueryEscape(keys[0])
			} else if core.WebhookPublic(n.Params) {
				ep["note"] = "Open endpoint — possession of the URL is the only credential."
			}
			out = append(out, ep)
		case core.FormInputModule:
			out = append(out, map[string]any{
				"kind":   "hosted_form",
				"method": "GET (renders) / POST (submits)",
				"url":    base + "/form/" + scope,
				"note":   "Public page — possession of the URL is the only credential.",
			})
		case core.RequestInputModule:
			ep := map[string]any{
				"kind":   "request",
				"method": "POST",
				"url":    base + "/call/" + scope,
				"note":   "Holds the connection until the flow's Reply step answers; ?wait=0 returns immediately instead.",
			}
			if keys := core.WebhookSecrets(n.Params); len(keys) > 0 {
				ep["auth"] = "Authorization: Bearer " + keys[0]
				ep["url_with_key"] = base + "/call/" + scope + "?key=" + url.QueryEscape(keys[0])
			} else if core.WebhookPublic(n.Params) {
				ep["note"] = ep["note"].(string) + " Open endpoint — possession of the URL is the only credential, and it answers with the flow's Reply."
			}
			out = append(out, ep)
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

func (h *flowAPI) enableFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setFlowEnabled(rw, r, p, true)
}
func (h *flowAPI) disableFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setFlowEnabled(rw, r, p, false)
}
func (h *flowAPI) setFlowEnabled(rw http.ResponseWriter, r *http.Request, p core.Principal, enabled bool) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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

// Idempotent: deleting an already-gone flow succeeds.
func (h *flowAPI) deleteFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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

// RFC 7396 merge semantics, so a null value DELETES a key.
func (h *flowAPI) patchFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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
	// Round-trip through a map, so merge semantics apply to unknown keys too.
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
	writeJSON(rw, http.StatusOK, h.flowMutationResponse(r, p, commit, next))
}

// RFC 7396: a null value deletes, an object merges recursively.
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
			target[k] = jsonMergePatch(map[string]any{}, subPatch)
			continue
		}
		target[k] = v
	}
	return target
}

func (h *flowAPI) runFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	q := r.URL.Query()
	q.Set("tenant", tenant)
	q.Set("workspace", workspace)
	q.Set("id", id)
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = q.Encode()
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	// A clean 404 before delegating, so the legacy path's error is not surfaced.
	if _, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ""); err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return
	}
	h.runCtl.runGraph(rw, r2, p)
}

func (h *flowAPI) resetNodeStateMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	if !h.secrets.requireSecretStore(rw, p) {
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

func (h *flowAPI) testTriggerFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	h.runCtl.testTrigger(rw, r2, p)
}

func (h *flowAPI) sampleFlowNodeMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	r2.SetPathValue("nodeID", r.PathValue("node_id"))
	h.sampleNode(rw, r2, p)
}

func (h *flowAPI) listFlowRunsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("tenant", tenant)
	r2.SetPathValue("workspace", workspace)
	r2.SetPathValue("id", id)
	h.listRuns(rw, r2, p)
}

func (h *flowAPI) validateGraphLiteral(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	g, ok := decodeRequestJSON[core.Graph](rw, r)
	if !ok {
		return
	}
	// Not touching the workspace, so tenant scoping is enough.
	if g.Tenant == "" {
		g.Tenant = p.Tenant
	}
	if g.Workspace == "" {
		g.Workspace = p.Workspace
	}
	manifests, _ := h.svc.ListDrops(r.Context(), p)
	issues := core.ValidateGraphFull(g, manifests)
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":     !hasLintError(issues),
		"issues": issues,
	})
}

func (h *flowAPI) validateFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	// A posted graph is a candidate the author has not saved yet — "is this
	// wiring sound?" asked BEFORE committing to it, which is the only order in
	// which the answer can change what they do. An empty body keeps the older
	// meaning and lints what is stored.
	body, ok := decodeRequestJSONOptional[core.Graph](rw, r)
	if !ok {
		return
	}
	g := body
	if len(g.Nodes) == 0 {
		var err error
		if g, err = h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ""); err != nil {
			writeAPIError(rw, http.StatusNotFound, "flow_not_found", err.Error())
			return
		}
	}
	// The path names the flow; a body claiming another one must not redirect the
	// scoping that readFlowID already authorized.
	g.Tenant, g.Workspace, g.ID = tenant, workspace, id
	issues := h.validateGraph(r, p, g)
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":     !hasLintError(issues),
		"issues": issues,
	})
}

func hasLintError(xs []core.LintIssue) bool {
	for _, x := range xs {
		if x.Severity == core.LintError {
			return true
		}
	}
	return false
}

func (h *flowAPI) listConnectionsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.oauth.oauthListProviders(rw, r, p)
}

func (h *flowAPI) startConnectionMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	provider := r.PathValue("provider")
	target, status, msg := h.oauth.buildAuthorizeURL(p,
		provider,
		r.URL.Query().Get("account"),
		r.URL.Query().Get("return_to"),
		scopeSubsetForIntegration(provider, r.URL.Query().Get("integration")),
		"",
		h.oauth.originHost(r),
	)
	if status != http.StatusOK {
		// 501 means the OAuth subsystem is not configured, not that the call was wrong.
		writeAPIError(rw, status, oauthErrorCode(status), msg)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"authorize_url": target})
}

func (h *flowAPI) disconnectConnectionMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	provider := r.PathValue("provider")
	// Forgets the stored token; gated on secret:write.
	if provider == "google" {
		if !core.CanAdminOrg(p) {
			writeAPIError(rw, http.StatusForbidden, "forbidden", "disconnecting a Google account requires organization:admin")
			return
		}
	} else if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	if !h.secrets.requireSecretStore(rw, p) {
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
	// So the run view can say "retrying" rather than showing a bare failure.
	WillRetry bool       `json:"will_retry,omitempty"`
	RetryAt   *time.Time `json:"retry_at,omitempty"`
}

type sseTerminalView struct {
	RunID  string         `json:"run_id"`
	Status core.JobStatus `json:"status"`
	Error  *core.JobError `json:"error,omitempty"`
}

func newSSETerminalView(ev *TerminalEvent) sseTerminalView {
	return sseTerminalView{RunID: ev.JobID, Status: ev.Status, Error: ev.Error}
}

func durationMS(start, end *time.Time) int64 {
	if start == nil || end == nil {
		return 0
	}
	return end.Sub(*start).Milliseconds()
}

func resultError(res *core.Result) *core.JobError {
	if res == nil {
		return nil
	}
	return res.Error
}

func newRunView(rec core.RunSummary) runView {
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
		Error:      rec.Error,
	}
}

func newNodeRunView(rec core.NodeRun) nodeRunView {
	v := nodeRunView{
		NodeID:     rec.NodeID,
		Status:     rec.Status,
		Attempts:   rec.Attempt,
		StartedAt:  rec.StartedAt,
		FinishedAt: rec.FinishedAt,
		DurationMS: durationMS(rec.StartedAt, rec.FinishedAt),
		Inputs:     rec.Inputs,
		Error:      resultError(rec.Result),
	}
	if rec.Result != nil {
		v.Outputs = rec.Result.Output
	}
	// Queued with a future availability is what "between attempts" looks like.
	if rec.Status == core.JobStatusQueued && rec.Attempt > 0 && rec.AvailableAt != nil {
		v.WillRetry = true
		v.RetryAt = rec.AvailableAt
	}
	return v
}

// Enforces the caller's tenant, so a run id alone is not an authorization.
func (h *flowAPI) loadRunScoped(rw http.ResponseWriter, r *http.Request, p core.Principal, runID string) (core.JobRecord, bool) {
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

func (h *flowAPI) loadRunSummaryScoped(rw http.ResponseWriter, r *http.Request, p core.Principal, runID string) (core.RunSummary, bool) {
	sum, err := h.svc.GetRunSummary(r.Context(), p, runID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrUnauthorized) {
			writeAPIError(rw, http.StatusNotFound, "run_not_found", "no run with that id")
			return core.RunSummary{}, false
		}
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return core.RunSummary{}, false
	}
	return sum, true
}

func (h *flowAPI) listRunsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.listAllRuns(rw, r, p)
}

func (h *flowAPI) getRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	sum, ok := h.loadRunSummaryScoped(rw, r, p, r.PathValue("run_id"))
	if !ok {
		return
	}
	writeJSON(rw, http.StatusOK, newRunView(sum))
}

func (h *flowAPI) listRunNodesMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	if _, ok := h.loadRunSummaryScoped(rw, r, p, runID); !ok {
		return
	}
	// The timeline renders eight fields per step; the full record is far larger.
	nodes, err := core.ListNodeRuns(r.Context(), h.svc.Jobs, runID, 1000) // typical graphs have <100 nodes; cap defensively
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]nodeRunView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, newNodeRunView(n))
	}
	h.fillRunNodeInputs(r.Context(), p, runID, nodes, out)
	writeJSON(rw, http.StatusOK, map[string]any{"nodes": out})
}

// A node record stores what a node PRODUCED, never what it received, so the
// inputs are rebuilt here through engine.AssembleInput — using the engine's own
// assembly keeps variadic fan-in and the one→many lift honest.
func (h *flowAPI) fillRunNodeInputs(
	ctx context.Context,
	p core.Principal,
	runID string,
	recs []core.NodeRun,
	views []nodeRunView,
) {
	need := false
	for i := range views {
		if len(views[i].Inputs) == 0 {
			need = true
			break
		}
	}
	if !need || h.svc == nil || h.svc.Jobs == nil {
		return
	}
	graph, err := h.svc.RunCache().graphForRun(ctx, h.svc.Jobs, runID)
	if err != nil {
		return
	}
	manifests := h.svc.manifestsForGraph(p.Tenant, graph)
	prior := make(map[string]core.Result, len(recs))
	for _, rec := range recs {
		if rec.Result != nil {
			prior[rec.NodeID] = *rec.Result
		}
	}
	for i := range views {
		if len(views[i].Inputs) > 0 {
			continue
		}
		in := engine.AssembleInput(graph, views[i].NodeID, manifests[nodeModule(graph, views[i].NodeID)], prior)
		if len(in) > 0 {
			views[i].Inputs = in
		}
	}
}

func nodeModule(g core.Graph, nodeID string) string {
	n, ok := g.Node(nodeID)
	if !ok {
		return ""
	}
	return n.Module
}

func (h *flowAPI) getRunNodeMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
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
	view := newNodeRunView(core.SummarizeNodeRun(nodeRec))
	if len(view.Inputs) == 0 {
		if graph, err := h.svc.RunCache().graphForRun(r.Context(), h.svc.Jobs, runID); err == nil {
			recs := []core.NodeRun{core.SummarizeNodeRun(nodeRec)}
			for _, e := range graph.Edges {
				if e.To != nodeRec.NodeID {
					continue
				}
				pred, perr := h.svc.Jobs.Get(r.Context(), NodeJobID(runID, e.From))
				if perr == nil {
					recs = append(recs, core.SummarizeNodeRun(pred))
				}
			}
			views := []nodeRunView{view}
			h.fillRunNodeInputs(r.Context(), p, runID, recs, views)
			view = views[0]
		}
	}
	writeJSON(rw, http.StatusOK, view)
}

func (h *flowAPI) runEventsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("jobID", r.PathValue("run_id"))
	h.jobEvents(rw, r2, p)
}

func (h *flowAPI) cancelRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("runID", r.PathValue("run_id"))
	h.runCtl.cancelRun(rw, r2, p)
}

func (h *flowAPI) resumeRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	r2 := r.Clone(r.Context())
	r2.SetPathValue("runID", r.PathValue("run_id"))
	h.runCtl.resumeRun(rw, r2, p)
}

func (h *flowAPI) replayRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	runID := r.PathValue("run_id")
	newRunID, err := h.svc.ReplayRun(r.Context(), p, runID)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrNotFound), errors.Is(err, core.ErrUnauthorized):
			writeAPIError(rw, http.StatusNotFound, "run_not_found", "no run with that id")
		case errors.Is(err, ErrReplayNoTriggerData):
			writeAPIError(rw, http.StatusConflict, "replay_no_trigger_data", err.Error())
		case errors.Is(err, ErrReplayTriggerChanged):
			writeAPIError(rw, http.StatusConflict, "replay_trigger_changed", err.Error())
		case errors.Is(err, ErrReplayTriggerOff):
			writeAPIError(rw, http.StatusConflict, "replay_trigger_off", err.Error())
		case errors.Is(err, core.ErrConflict):
			writeAPIError(rw, http.StatusConflict, "run_not_replayable", err.Error())
		case errors.Is(err, core.ErrPlanLimit):
			writeAPIError(rw, http.StatusPaymentRequired, "plan_limit", err.Error())
		case errors.Is(err, core.ErrOrgSuspended):
			writeAPIError(rw, http.StatusForbidden, "org_suspended", err.Error())
		default:
			writeAPIError(rw, http.StatusBadRequest, "replay_failed", err.Error())
		}
		return
	}
	h.audit(r.Context(), p, "graph.run", newRunID, "replay-of="+runID)
	writeJSON(rw, http.StatusAccepted, map[string]string{"job_id": newRunID})
}

func (h *flowAPI) retryRunMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

type flowLoader struct{ svc *Service }

func (l flowLoader) loadFlowForRequest(rw http.ResponseWriter, r *http.Request, p core.Principal, ref string) (tenant, workspace, id string, g core.Graph, ok bool) {
	tenant, workspace, id, ok = readFlowID(rw, r, p)
	if !ok {
		return "", "", "", core.Graph{}, false
	}
	g, err := l.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ref)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", flowNotFoundMessage(tenant, workspace, id))
		return "", "", "", core.Graph{}, false
	}
	return tenant, workspace, id, g, true
}

func (h *HTTPGateway) flows() flowLoader { return flowLoader{svc: h.svc} }
