// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

// The editor's card data faces need a step's last output without a run id to
// hand: the canvas is open, nothing has run in THIS session, and the question
// is still "what does this step produce?".
//
// Nothing new is stored to answer it. A node record already holds what its
// node produced, and the run viewer already serves those values to the same
// people behind the same authorization — this reads the same rows keyed by
// flow instead of by run, newest-first, and keeps the first hit per node.
//
// Merging across runs rather than reading only the newest run is what makes
// it useful: sampling one step (POST .../nodes/{id}/sample) runs that step's
// upstream chain alone, so the newest run frequently covers a fraction of the
// graph while older runs hold the rest.
//
// The bound is retention. When a run's records are pruned or its logs
// deleted, its samples go with them and the card falls back to "no data yet"
// — which is the honest reading of a badge that says "from the last run". A
// sample that must outlive retention has to be pinned, and a pin is storage.

// maxSampleRecords bounds how far back the merge walks. Enough to cover a
// large graph plus several partial runs, and a hard stop on a flow with
// months of history.
const maxSampleRecords = 400

// maxSampleValueBytes is the most one port's value may carry into a card
// face. The face shows three rows and a few columns; a step that emitted a
// 40 MB spreadsheet has nothing extra to say on a 200px card, and sending it
// would cost every editor load. Oversized ports keep their port and MIME so
// the card still names what flows, and drop the value.
const maxSampleValueBytes = 96 << 10 // 96 KiB

// flowSamples answers GET /api/v1/me/flows/{flow_id}/samples: the most recent
// output of each of the flow's steps, keyed by node id then port.
func (h *flowAPI) flowSamples(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, id, ok := readFlowID(rw, r, p)
	if !ok {
		return
	}
	// Same gate as the run list: the flow must exist and be visible to this
	// principal. Without it, a node id plus a guessed flow id would read
	// another workspace's values.
	if _, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ""); err != nil {
		writeAPIError(rw, http.StatusNotFound, "flow_not_found", err.Error())
		return
	}
	if h.svc.Jobs == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"flow": id, "nodes": map[string]any{}})
		return
	}
	recs, err := h.svc.Jobs.ListNodeRecords(r.Context(), core.ListNodeRecordsOpts{
		Tenant:    tenant,
		Workspace: workspace,
		GraphID:   id,
		Limit:     maxSampleRecords,
	})
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"flow":  id,
		"nodes": latestOutputs(recs, maxSampleValueBytes),
	})
}

// latestOutputs folds node records into one output per node. `recs` must be
// newest-first — the store's own order — because the first record seen for a
// node is the one that wins.
//
// That order is by enqueue time, not finish time, which differ only when two
// runs of one flow overlap: a step queued earlier can finish later. Enqueue
// order is the graph index's own (jobs_graph_idx), and a flow already holds a
// run lock, so the case is rare enough not to trade the index for it.
//
// A record with no result is skipped rather than recorded as empty: a step
// that failed this morning should still show what it produced yesterday.
func latestOutputs(recs []core.JobRecord, budget int) map[string]map[string]core.Ref {
	out := make(map[string]map[string]core.Ref)
	for _, rec := range recs {
		if rec.NodeID == "" || rec.Result == nil || len(rec.Result.Output) == 0 {
			continue
		}
		if _, seen := out[rec.NodeID]; seen {
			continue
		}
		ports := make(map[string]core.Ref, len(rec.Result.Output))
		for port, ref := range rec.Result.Output {
			ports[port] = capSampleRef(ref, budget)
		}
		out[rec.NodeID] = ports
	}
	return out
}

// capSampleRef strips the inline value of an oversized port, keeping the
// metadata that says what the port carries. ApproxValueSize stops counting at
// the budget, so measuring a huge value costs the budget rather than the
// value.
func capSampleRef(ref core.Ref, budget int) core.Ref {
	if ref.Inline == nil || core.ApproxValueSize(ref.Inline, budget) < budget {
		return ref
	}
	return core.Ref{MIME: ref.MIME, Ref: ref.Ref, Headers: ref.Headers}
}
