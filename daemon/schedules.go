package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// scheduleEntry is one automatic-trigger schedule, as the Schedules page
// consumes it: which flow + node, the cadence, and a small next-run
// preview. One entry per cron_trigger / poll_trigger node — a flow with
// two triggers yields two entries. The `next_fires` preview is computed
// with the SAME cron parser the scheduler fires on, so the times shown
// match what will actually run.
type scheduleEntry struct {
	FlowID          string `json:"flow_id"` // tenant/workspace/graph_id
	GraphID         string `json:"graph_id"`
	FlowName        string `json:"flow_name,omitempty"`
	Icon            string `json:"icon,omitempty"`
	NodeID          string `json:"node_id"`
	Kind            string `json:"kind"`           // "cron" | "poll"
	Cron            string `json:"cron,omitempty"` // for kind=cron
	TZ              string `json:"tz,omitempty"`   // for kind=cron
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	// Disabled is the per-trigger pause (node Params["disabled"]).
	Disabled bool `json:"disabled"`
	// FlowDisabled is the whole-flow pause (graph.Disabled); when true the
	// trigger won't fire regardless of its own Disabled flag. Surfaced so
	// the UI can explain why a schedule shows "paused" even if the trigger
	// itself is enabled.
	FlowDisabled bool `json:"flow_disabled"`
	// NextFires is up to a few upcoming fire times (RFC3339 UTC). Empty
	// when the schedule is paused or the expression never fires. For poll
	// triggers it's a simple interval projection from now.
	NextFires []string `json:"next_fires,omitempty"`
}

const scheduleNextFiresPreview = 5

// ListSchedules enumerates every cron/poll trigger node across the flows
// the principal can view in a workspace. Mirrors ListFlowSummaries'
// visibility handling (open store, load each graph, authorize) and reuses
// the scheduler's parser + nextCronFires so the preview matches reality.
func (s *Service) ListSchedules(ctx context.Context, p core.Principal, tenant, ws string) ([]scheduleEntry, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	ids, err := store.ListGraphs()
	if err != nil {
		return nil, err
	}
	isAdmin := core.IsFlowAdminPrincipal(p)
	now := time.Now()
	out := []scheduleEntry{}
	for _, id := range ids {
		g, err := store.Load(id)
		if err != nil {
			continue
		}
		if !isAdmin && core.AuthorizeGraphView(p, g) != nil {
			continue
		}
		flowID := tenant + "/" + ws + "/" + id
		for _, node := range g.Nodes {
			switch node.Module {
			case "cron_trigger":
				expr, _ := node.Params["cron"].(string)
				expr = strings.TrimSpace(expr)
				if expr == "" {
					continue // unscheduled — manual only
				}
				tz, _ := node.Params["tz"].(string)
				e := scheduleEntry{
					FlowID:       flowID,
					GraphID:      id,
					FlowName:     g.Name,
					Icon:         g.Icon,
					NodeID:       node.ID,
					Kind:         "cron",
					Cron:         expr,
					TZ:           tz,
					Disabled:     triggerNodeDisabled(node),
					FlowDisabled: g.Disabled,
				}
				if !e.Disabled && !e.FlowDisabled {
					if sched, perr := parseCronInTZ(cronValidator, expr, tz); perr == nil {
						e.NextFires = nextCronFires(sched, now, scheduleNextFiresPreview)
					}
				}
				out = append(out, e)
			case "poll_trigger", "google_form_trigger":
				secs := paramSeconds(node.Params, "interval_seconds")
				if secs <= 0 || secs > core.MaxPollIntervalSeconds {
					continue
				}
				e := scheduleEntry{
					FlowID:          flowID,
					GraphID:         id,
					FlowName:        g.Name,
					Icon:            g.Icon,
					NodeID:          node.ID,
					Kind:            "poll",
					IntervalSeconds: secs,
					Disabled:        triggerNodeDisabled(node),
					FlowDisabled:    g.Disabled,
				}
				if !e.Disabled && !e.FlowDisabled {
					e.NextFires = pollNextFires(now, time.Duration(secs)*time.Second, scheduleNextFiresPreview)
				}
				out = append(out, e)
			}
		}
	}
	// Stable order: by flow name/id then node id, so the list doesn't
	// reshuffle between polls.
	sort.Slice(out, func(i, j int) bool {
		if out[i].GraphID != out[j].GraphID {
			return out[i].GraphID < out[j].GraphID
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out, nil
}

// pollNextFires projects a poll trigger's upcoming fires as a simple
// interval series from now. Poll triggers are interval-anchored to the
// last fire (not a wall clock), so this is an approximation for preview
// purposes — close enough to answer "how often / roughly when".
func pollNextFires(from time.Time, interval time.Duration, n int) []string {
	fires := make([]string, 0, n)
	t := from
	for i := 0; i < n; i++ {
		t = t.Add(interval)
		fires = append(fires, t.UTC().Format(time.RFC3339))
	}
	return fires
}

// SetTriggerEnabled pauses or resumes a single trigger node by toggling
// its `disabled` param and saving the graph. Reuses SaveGraph's edit
// authorization + active-run lock, so pausing a trigger on a locked flow
// 409s like any other edit. Returns the new commit hash.
func (s *Service) SetTriggerEnabled(ctx context.Context, p core.Principal, tenant, ws, id, nodeID string, enabled bool) (string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return "", err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return "", err
	}
	g, err := store.Load(id)
	if err != nil {
		return "", err
	}
	found := false
	for i := range g.Nodes {
		if g.Nodes[i].ID != nodeID {
			continue
		}
		found = true
		if g.Nodes[i].Params == nil {
			g.Nodes[i].Params = map[string]any{}
		}
		if enabled {
			// Remove the key entirely so the node reads as a clean
			// "active" trigger rather than carrying disabled:false noise.
			delete(g.Nodes[i].Params, "disabled")
		} else {
			g.Nodes[i].Params["disabled"] = true
		}
		break
	}
	if !found {
		return "", fmt.Errorf("node %q: %w", nodeID, core.ErrNotFound)
	}
	g.Tenant, g.Workspace, g.ID = tenant, ws, id
	// Explicit (non-coalescing) save: pausing a trigger is an intentional
	// checkpoint that should show up as its own entry in the history.
	return s.saveGraph(ctx, p, g, false)
}

// --- HTTP handlers ----------------------------------------------------

// listSchedulesMe is GET /api/v1/me/schedules?tenant&workspace — every
// cron/poll trigger in the workspace with its next-run preview. Backs the
// Schedules page (list + calendar). Falls back to the principal's binding
// when tenant/workspace query params are omitted.
func (h *HTTPGateway) listSchedulesMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	entries, err := h.svc.ListSchedules(r.Context(), p, tenant, workspace)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"schedules": entries})
}

// enableTriggerMe / disableTriggerMe are
// POST /me/flows/{flow_id}/triggers/{node_id}/{enable|disable}. Idempotent
// per the underlying param toggle. A 409 means the flow is locked by an
// active run (same as any edit).
func (h *HTTPGateway) enableTriggerMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setTriggerEnabled(rw, r, p, true)
}
func (h *HTTPGateway) disableTriggerMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setTriggerEnabled(rw, r, p, false)
}
func (h *HTTPGateway) setTriggerEnabled(rw http.ResponseWriter, r *http.Request, p core.Principal, enabled bool) {
	tenant, workspace, id, ok := h.readFlowID(rw, r, p)
	if !ok {
		return
	}
	nodeID := r.PathValue("node_id")
	commit, err := h.svc.SetTriggerEnabled(r.Context(), p, tenant, workspace, id, nodeID, enabled)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrConflict):
			writeAPIError(rw, http.StatusConflict, "flow_locked", err.Error())
		case errors.Is(err, core.ErrNotFound):
			writeAPIError(rw, http.StatusNotFound, "node_not_found", err.Error())
		default:
			writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		}
		return
	}
	action := "trigger.disable"
	if enabled {
		action = "trigger.enable"
	}
	h.audit(r.Context(), p, action, id, "node="+nodeID+" commit="+commit)
	writeJSON(rw, http.StatusOK, map[string]any{
		"flow_id": tenant + "/" + workspace + "/" + id,
		"node_id": nodeID,
		"enabled": enabled,
		"commit":  commit,
	})
}
