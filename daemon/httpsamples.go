// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"reflect"

	"github.com/dazyflow/dazyflow/core"
)

// The editor's card data faces need a step's last output without a run id to
// hand: the canvas is open, nothing has run in THIS session, and the question is
// still "what does this step produce?".
//
// Nothing new is stored to answer it. A node record already holds what its node
// produced, and the run viewer serves those values to the same people behind the
// same authorization — this reads the same rows keyed by flow instead of by run,
// newest-first, keeping the first hit per node.
//
// Merging across runs is what makes it useful: sampling one step runs that
// step's upstream chain alone, so the newest run frequently covers a fraction of
// the graph while older runs hold the rest.
//
// The bound is retention. When a run's records are pruned its samples go too and
// the card falls back to "no data yet", which is the honest reading of a badge
// that says "from the last run".

const maxSampleRecords = 400

// maxSampleValueBytes is the most one port's value may carry into a card
// face. The face shows three rows and a few columns; a step that emitted a
// 40 MB spreadsheet has nothing extra to say on a 200px card, and sending it
// would cost every editor load. Oversized ports keep their port and MIME so
// the card still names what flows.
const maxSampleValueBytes = 96 << 10 // 96 KiB

// sampleRef is a Ref plus how much of it was left behind. Truncated carries the
// row count the step ACTUALLY produced whenever the served value holds fewer —
// so a reader can say "3 of 34" rather than presenting a prefix as the whole
// thing. Zero means the value is complete, which is the common case and why it
// is omitempty.
//
// It embeds core.Ref rather than adding a field to it: Ref crosses the
// daemon/runner wire on every job, and "how much did the editor get" is a
// question only this endpoint asks.
type sampleRef struct {
	core.Ref
	Truncated int `json:"truncated,omitempty"`
}

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
func latestOutputs(recs []core.JobRecord, budget int) map[string]map[string]sampleRef {
	out := make(map[string]map[string]sampleRef)
	for _, rec := range recs {
		if rec.NodeID == "" || rec.Result == nil || len(rec.Result.Output) == 0 {
			continue
		}
		if _, seen := out[rec.NodeID]; seen {
			continue
		}
		ports := make(map[string]sampleRef, len(rec.Result.Output))
		for port, ref := range rec.Result.Output {
			ports[port] = capSampleRef(ref, budget)
		}
		out[rec.NodeID] = ports
	}
	return out
}

// capSampleRef brings an oversized port under the budget, preferring a shorter
// value to no value.
//
// A row list is the case worth handling: an RSS feed or a spreadsheet read blows
// the budget on volume, not on any one row, and its FIRST rows answer every
// question the editor asks of a sample — what the columns are called, what a
// value looks like, what a template will render. So a list keeps the prefix that
// fits and reports the true length in Truncated.
//
// Anything else (a 40 MB string, a blob) can only be cut mid-value, which would
// hand a reader a half a JSON document and no way to know it. Those still drop
// to metadata alone, as does a list whose very first row already exceeds the
// budget — Truncated still carries the row count, so "34 items, too large to
// show" stays sayable.
//
// ApproxValueSize stops counting at the budget, so measuring a huge value costs
// the budget rather than the value.
func capSampleRef(ref core.Ref, budget int) sampleRef {
	if ref.Inline == nil || core.ApproxValueSize(ref.Inline, budget) < budget {
		return sampleRef{Ref: ref}
	}
	if rows, ok := sampleRows(ref.Inline); ok {
		kept, spent := 0, 0
		for _, row := range rows {
			spent += core.ApproxValueSize(row, budget)
			if spent >= budget {
				break
			}
			kept++
		}
		if kept > 0 {
			short := ref
			short.Inline = rows[:kept]
			return sampleRef{Ref: short, Truncated: len(rows)}
		}
		return sampleRef{Ref: core.Ref{MIME: ref.MIME, Ref: ref.Ref, Headers: ref.Headers}, Truncated: len(rows)}
	}
	return sampleRef{Ref: core.Ref{MIME: ref.MIME, Ref: ref.Ref, Headers: ref.Headers}}
}

// sampleRows reports whether a port's value is a list of rows, and flattens it
// to []any so a prefix can be taken.
//
// Reflection rather than a []any type assertion because the same value reaches
// here in two shapes: a drop emits its own []map[string]any, and a store that
// round-trips through JSON hands back []any. Both are the same rows to a reader,
// and a type switch that saw only one would truncate inconsistently depending on
// where the record came from.
//
// []byte is a blob, not a list — slicing it would cut a value in half, which is
// the thing this whole function exists to avoid.
func sampleRows(v any) ([]any, bool) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}
