// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func (h *flowAPI) sampleNode(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	g, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, "")
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	sub, ok := g.UpstreamSubset(nodeID)
	if !ok {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("node %q not in graph %q", nodeID, id))
		return
	}
	// Sampling re-runs the upstream chain, so a trigger node in the subset would
	// fail with no_trigger_data (a trigger has no data outside a real firing).
	// Detect that here and return an actionable error pointing at test-trigger,
	// instead of submitting a run that dies cryptically.
	{
		mans := h.svc.manifestsForGraph(p.Tenant, sub)
		for _, n := range sub.Nodes {
			if m, ok := mans[n.Module]; ok && m.ExecutionModel == core.ExecutionTrigger {
				writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf(
					"can't sample %q: it depends on trigger node %q (%s), which has no data outside a real firing — use the flow's test-trigger with a payload instead",
					nodeID, n.ID, n.Module))
				return
			}
		}
	}
	runID, err := h.svc.SubmitGraphOpts(r.Context(), p, sub, SubmitOpts{Manual: true})
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(rw, http.StatusAccepted, map[string]string{
		"job_id":       runID,
		"sampled_node": nodeID,
	})
}

func (h *flowAPI) jobEvents(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	jobID := r.PathValue("jobID")
	rec, err := h.svc.GetJob(r.Context(), p, jobID)
	if err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}

	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Subscribe BEFORE deciding whether the run is already terminal. A short
	// run can reach its terminal state in the gap between the GetJob above and
	// the subscription taking effect; if we checked the snapshot's status and
	// only subscribed afterwards, the terminal bus event published in that gap
	// would reach no subscriber and the stream would hang until the client's
	// deadline (a flaky 30s stall in tests; a wedged "Upgrade…"-style spinner
	// for a UI that reconnects to a just-finished run). Subscribing first means
	// any such event is buffered on our channel, and the status re-read below
	// catches a run that finished at or before subscribe time.
	events, cancel := h.svc.bus().Subscribe(jobID)
	defer cancel()

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no") // for nginx
	rw.WriteHeader(http.StatusOK)

	writeSSE(rw, "snapshot", newRunView(core.SummarizeRun(rec)))
	h.emitNodeSnapshots(rw, r.Context(), rec)
	flusher.Flush()

	if cur, err := h.svc.GetJob(r.Context(), p, jobID); err == nil {
		rec = cur
	}
	if core.IsTerminalStatus(rec.Status) {
		writeSSE(rw, "terminal", sseTerminalView{
			RunID:  rec.ID,
			Status: rec.Status,
			Error:  resultError(rec.Result),
		})
		flusher.Flush()
		return
	}

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprintf(rw, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Progress != nil {
				writeSSE(rw, "progress", ev.Progress)
				flusher.Flush()
			}
			if ev.NodeStatus != nil {
				writeSSE(rw, "node", ev.NodeStatus)
				flusher.Flush()
			}
			if ev.Paused != nil {
				writeSSE(rw, "paused", ev.Paused)
				flusher.Flush()
			}
			if ev.Terminal != nil {
				writeSSE(rw, "terminal", newSSETerminalView(ev.Terminal))
				flusher.Flush()
				return
			}
		}
	}
}

func (h *flowAPI) watchFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// Validate scope + readability up front (and resolve the id parts) the
	// same way a load would — a 403/404 here is clearer than a silent stream
	// that never emits. The graph itself is discarded; only the key matters.
	tenant, workspace, id, _, ok := h.loadFlowForRequest(rw, r, p, "")
	if !ok {
		return
	}

	flusher, ok := rw.(http.Flusher)
	if !ok {
		writeJSONError(rw, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	// Subscribe BEFORE writing the ": watching" comment that signals the
	// stream is live. If we opened the stream first and subscribed after, an
	// edit landing in that gap would be published to no subscriber and missed;
	// a client that treats ": watching" as "I'm now receiving updates" (and
	// any test that publishes right after it) would then lose the event.
	// Subscribing first makes ": watching" a truthful readiness signal.
	events, cancel := h.svc.bus().Subscribe(flowBusKey(tenant, workspace, id))
	defer cancel()

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no") // for nginx
	rw.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(rw, ": watching\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprintf(rw, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.FlowUpdated != nil {
				writeSSE(rw, "flow_updated", ev.FlowUpdated)
				flusher.Flush()
			}
		}
	}
}

// emitNodeSnapshots walks the graph payload from the graph-record and
// emits one `node` SSE frame per node that already has a stored record.
// This catches up subscribers that connect after the worker has already
// processed some nodes — without it, the canvas would show stale
// statuses until the next live transition.
func (h *flowAPI) emitNodeSnapshots(rw http.ResponseWriter, ctx context.Context, graphRec core.JobRecord) {
	if graphRec.Kind != core.JobKindGraph || len(graphRec.GraphPayload) == 0 {
		return
	}
	var g core.Graph
	if err := json.Unmarshal(graphRec.GraphPayload, &g); err != nil {
		return
	}
	for _, n := range g.Nodes {
		nodeRec, err := h.svc.Jobs.Get(ctx, NodeJobID(graphRec.ID, n.ID))
		if err != nil {
			continue
		}
		var jerr *core.JobError
		if nodeRec.Result != nil {
			jerr = nodeRec.Result.Error
		}
		writeSSE(rw, "node", NodeStatusEvent{
			NodeID: n.ID,
			Status: nodeRec.Status,
			Error:  jerr,
		})
	}
}
