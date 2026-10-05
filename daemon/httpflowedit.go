// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// saveOptionsFor reads what a save request says about itself: If-Match as the
// base ETag, and whether the caller is software acting for the user, which
// decides if the save is announced to open canvases as an assistant's.
func saveOptionsFor(r *http.Request) SaveOptions {
	return SaveOptions{
		BaseETag:  strings.Trim(strings.TrimPrefix(r.Header.Get("If-Match"), "W/"), `"`),
		Assistant: auth.IsAPIKeyCredential(credentialFromRequest(r)),
	}
}

// writeSaveConflict answers the two refusals a save can hit for reasons other
// than the graph itself, reporting whether it wrote a response.
func writeSaveConflict(rw http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, ErrFlowChanged):
		writeAPIError(rw, http.StatusConflict, "flow_changed",
			"someone saved this flow after you read it; read it again and reapply your change")
	case errors.Is(err, core.ErrConflict):
		writeAPIError(rw, http.StatusConflict, "flow_locked", err.Error())
	default:
		return false
	}
	return true
}

// editFlowMe applies a batch of core.GraphOps to the stored flow and saves it
// once: the conversational way to build a flow, where each turn is "add this,
// connect that" rather than a whole graph. The batch is applied to the flow as
// stored now, so ops from a caller who read it a while ago still land where
// their ids say; base (an ETag from GET or an earlier edit) makes the call
// refuse instead if anything changed in between.
//
// The reply is deliberately small — what changed, a one-line view of every
// node, the lint — so a voice assistant can answer from it without re-reading
// the graph, and undo_ref restores the flow to just before this edit.
func (h *flowAPI) editFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, current, ok := h.loadFlowForRequest(rw, r, p, "")
	if !ok {
		return
	}
	body, ok := decodeRequestJSON[struct {
		Ops  []core.GraphOp `json:"ops"`
		Note string         `json:"note"`
		Base string         `json:"base"`
	}](rw, r)
	if !ok {
		return
	}
	if len(body.Ops) == 0 {
		writeAPIError(rw, http.StatusBadRequest, "validation_failed", "ops must list at least one change")
		return
	}
	before := core.GraphETag(current)
	if body.Base != "" && body.Base != before {
		writeSaveConflict(rw, ErrFlowChanged)
		return
	}
	manifests, err := h.svc.ListDrops(r.Context(), p)
	if err != nil {
		manifests = nil
	}
	next, rep, err := core.ApplyGraphOps(current, body.Ops, manifests)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "edit_failed", err.Error()+"; nothing was changed")
		return
	}
	var undoRef string
	if revs, err := h.svc.FlowHistory(r.Context(), p, tenant, workspace, id, 1); err == nil && len(revs) > 0 {
		undoRef = revs[0].Commit
	}
	opts := saveOptionsFor(r)
	opts.BaseETag, opts.Note = before, body.Note
	commit, err := h.svc.SaveGraphWith(r.Context(), p, next, opts)
	if err != nil {
		if writeSaveConflict(rw, err) {
			return
		}
		writeAPIError(rw, http.StatusUnprocessableEntity, "validation_failed", err.Error()+"; nothing was changed")
		return
	}
	saved, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		saved = next
	}
	h.audit(r.Context(), p, "graph.edit", id, "commit="+commit)
	resp := h.flowMutationResponse(r, p, commit, saved)
	resp["report"] = rep
	resp["etag"] = core.GraphETag(saved)
	resp["undo_ref"] = undoRef
	resp["nodes"] = nodeLines(saved)
	writeJSON(rw, http.StatusOK, resp)
}

// nodeLines is the flow at a glance: each node with what it is and where its
// wires go, e.g. "post: slack.post_message → done".
func nodeLines(g core.Graph) []string {
	out := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		line := n.ID + ": " + n.Module
		if n.Label != "" {
			line += " (" + n.Label + ")"
		}
		var next []string
		for _, e := range g.Edges {
			if e.From == n.ID {
				next = append(next, e.To)
			}
		}
		if len(next) > 0 {
			line += " → " + strings.Join(next, ", ")
		}
		if n.Disabled {
			line += " [disabled]"
		}
		out = append(out, line)
	}
	return out
}
