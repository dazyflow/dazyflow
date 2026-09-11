// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

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
	Disabled        bool   `json:"disabled"`
	FlowDisabled    bool   `json:"flow_disabled"`
	// NextFires is up to a few upcoming fire times (RFC3339 UTC). Empty
	// when the schedule is paused or the expression never fires. For poll
	// triggers it's a simple interval projection from now.
	NextFires []string `json:"next_fires,omitempty"`
}

const scheduleNextFiresPreview = 5

func (s *Service) ListSchedules(ctx context.Context, p core.Principal, tenant, ws string) ([]scheduleEntry, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	flows, err := store.ListHeadersAtHead("")
	if err != nil {
		return nil, err
	}
	isAdmin := core.IsFlowAdminPrincipal(p)
	now := time.Now()
	out := []scheduleEntry{}
	for _, f := range flows {
		id, g := f.ID, f.Graph
		if !isAdmin && core.AuthorizeGraphView(p, g) != nil {
			continue
		}
		flowID := tenant + "/" + ws + "/" + id
		for _, node := range g.Nodes {
			switch {
			case node.Module == "cron_trigger":
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
			case core.IsPollTriggerModule(node.Module):
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
	return s.saveGraph(ctx, p, g, false)
}

func (h *flowAPI) listSchedulesMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	// Reject a cross-tenant/workspace scope up front with a clean 403, like the
	// sibling /me handlers (boards, flows). The service layer's RequireTenant
	// also rejects it, but as an ErrUnauthorized that maps to a 500 with the
	// internal error echoed — both a status and an info-leak inconsistency.
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot act on tenant %q (principal is bound to %q)", tenant, p.Tenant))
		return
	}
	if workspace != p.Workspace && !isPlatformAdmin(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot act on workspace %q (principal is bound to %q)", workspace, p.Workspace))
		return
	}
	entries, err := h.svc.ListSchedules(r.Context(), p, tenant, workspace)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"schedules": entries})
}

func (h *flowAPI) enableTriggerMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setTriggerEnabled(rw, r, p, true)
}
func (h *flowAPI) disableTriggerMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	h.setTriggerEnabled(rw, r, p, false)
}
func (h *flowAPI) setTriggerEnabled(rw http.ResponseWriter, r *http.Request, p core.Principal, enabled bool) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
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
