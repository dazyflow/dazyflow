// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Server-sent event streams and single-node sampling — the routes the editor
// holds open to watch a run progress, as opposed to the request/response
// routes everywhere else in the gateway.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func (h *HTTPGateway) sampleNode(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	if mans, mErr := h.svc.ListDrops(r.Context(), p); mErr == nil {
		for _, n := range sub.Nodes {
			if m, ok := mans[n.Module]; ok && m.ExecutionModel == core.ExecutionTrigger {
				writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf(
					"can't sample %q: it depends on trigger node %q (%s), which has no data outside a real firing — use the flow's test-trigger with a payload instead",
					nodeID, n.ID, n.Module))
				return
			}
		}
	}
	// The inspector's "what does this step emit?" preview. Nobody wants an
	// email because a preview of one step failed while they were looking at it.
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

// jobEvents streams bus events for jobID as Server-Sent Events. Each
// frame is `event: <kind>\ndata: <json>\n\n` where kind is "progress",
// "terminal", or "snapshot" (the initial frame containing the current
// JobRecord).
//
// The stream closes when the job reaches a terminal state. The handler
// also flushes on every event so browsers see updates promptly.
func (h *HTTPGateway) jobEvents(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

	// Snapshot first so the UI has the current state without racing
	// against subscriber delivery. Emit the same clean runView the REST
	// /me/runs/{id} endpoint returns — not the raw JobRecord.
	writeSSE(rw, "snapshot", newRunView(rec))
	// Followed by per-node status snapshots — late subscribers (the UI
	// that connects after Submit returns) catch up on transitions that
	// already happened.
	h.emitNodeSnapshots(rw, r.Context(), rec)
	flusher.Flush()

	// Re-read the status now that we're subscribed. If the run reached a
	// terminal state at or before subscribe time, emit terminal and stop; a
	// terminal that lands after this point instead arrives on `events`.
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

	// Keep-alive ping every 25s — proxies time out idle SSE streams
	// faster than that without a heartbeat.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			// SSE comment lines (starting with ":") are dropped by the
			// EventSource API but keep the TCP connection alive.
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

// watchFlowMe streams `flow_updated` Server-Sent Events for a flow: one
// frame each time the flow's graph is saved, by anyone (the web editor, the
// MCP server, a direct API call). An open editor subscribes so it can
// live-reflect external edits — e.g. an AI assistant restructuring the flow
// through MCP — animating the new graph onto its canvas.
//
// The frame carries only {flow_id, commit, author, autosave} — no graph
// content. The client re-fetches the graph through the normal authorized
// load path on receipt, and uses `commit` to ignore the echo of its own
// save. Mirrors jobEvents' SSE plumbing (headers, flush, 25s keep-alive,
// disconnect on context cancel).
func (h *HTTPGateway) watchFlowMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	// An initial comment opens the stream so the client's fetch resolves its
	// response promptly even before the first edit lands.
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
func (h *HTTPGateway) emitNodeSnapshots(rw http.ResponseWriter, ctx context.Context, graphRec core.JobRecord) {
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
