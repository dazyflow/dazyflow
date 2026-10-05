// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// Canvas focus is what the user is looking at right now: the flow open on
// their canvas and the step they last tapped. The canvas reports it; an
// assistant reads it, so "change this step" or "what does this one do?" —
// said while pointing at a phone — resolves to a real node instead of a guess.
//
// One record per user, in the EphemeralStore so any replica can answer, and
// short-lived: an hour-old selection is not what anyone means by "this".

const (
	ephemeralCanvasFocus = "canvas_focus"
	canvasFocusTTL       = 15 * time.Minute
)

type canvasFocus struct {
	FlowID    string    `json:"flow_id"` // tenant/workspace/id
	NodeID    string    `json:"node_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type canvasFocusAPI struct {
	urlBuilder
	svc       *Service
	Ephemeral auth.EphemeralStore
}

func (h *HTTPGateway) canvasFocusAPI() *canvasFocusAPI {
	return &canvasFocusAPI{urlBuilder: h.urls(), svc: h.svc, Ephemeral: h.Ephemeral}
}

// putCanvasFocus is session-only: it is the person's own pointer, and an
// assistant that could set it could make "this" mean whatever it liked.
func (h *canvasFocusAPI) putCanvasFocus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireSessionCredential(rw, r, "reporting canvas focus") {
		return
	}
	body, ok := decodeRequestJSON[canvasFocus](rw, r)
	if !ok {
		return
	}
	if parts := strings.Split(body.FlowID, "/"); len(parts) != 3 || parts[0] != p.Tenant || core.ValidGraphID(parts[2]) != nil {
		writeAPIError(rw, http.StatusBadRequest, "validation_failed", "flow_id must be tenant/workspace/id in your organization")
		return
	}
	if len(body.NodeID) > 128 {
		writeAPIError(rw, http.StatusBadRequest, "validation_failed", "node_id is too long")
		return
	}
	body.UpdatedAt = time.Now().UTC()
	raw, _ := json.Marshal(body)
	if err := h.Ephemeral.Put(r.Context(), ephemeralCanvasFocus, p.Subject, raw, body.UpdatedAt.Add(canvasFocusTTL)); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "store canvas focus: "+err.Error())
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// getCanvasFocus answers with the selected step as it is saved now, read with
// the caller's own access — focus names a flow, it never grants one.
func (h *canvasFocusAPI) getCanvasFocus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	raw, _, err := h.Ephemeral.Get(r.Context(), ephemeralCanvasFocus, p.Subject)
	var f canvasFocus
	if err != nil || json.Unmarshal(raw, &f) != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"open": false})
		return
	}
	parts := strings.SplitN(f.FlowID, "/", 3)
	if len(parts) != 3 {
		writeJSON(rw, http.StatusOK, map[string]any{"open": false})
		return
	}
	g, err := h.svc.LoadGraph(r.Context(), p, parts[0], parts[1], parts[2], "")
	if err != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"open": false})
		return
	}
	out := map[string]any{
		"open":       true,
		"flow_id":    parts[2],
		"flow_name":  g.Name,
		"workspace":  parts[0] + "/" + parts[1],
		"updated_at": f.UpdatedAt,
		"canvas_url": h.effectiveBaseURL(r) + "/flows/" + parts[2],
	}
	if n, ok := g.Node(f.NodeID); ok {
		out["selected"] = map[string]any{"id": n.ID, "module": n.Module, "label": n.Label, "params": n.Params, "disabled": n.Disabled}
	}
	writeJSON(rw, http.StatusOK, out)
}
