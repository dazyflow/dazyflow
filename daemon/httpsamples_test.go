// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// The editor asks "what did each step of this flow last produce?" with no run
// id — the canvas is open and nothing has run in this session. The answer is
// folded out of the node records the runs already wrote.

type samplesResponse struct {
	Flow  string                         `json:"flow"`
	Nodes map[string]map[string]core.Ref `json:"nodes"`
}

// seedSampleFlow saves a two-step flow so the endpoint's visibility gate has
// something to load.
func seedSampleFlow(t *testing.T, h *gatewayHarness) {
	t.Helper()
	if _, err := h.ws.Save(core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws", Name: "Invoice triage",
		Nodes: []core.Node{
			{ID: "src", Module: "json", Params: map[string]any{"json": "[]"}},
			{ID: "sink", Module: "sort_rows", Params: map[string]any{"by": "name"}},
		},
		Edges: []core.Edge{{From: "src", FromPort: "out", To: "sink", ToPort: "rows"}},
	}, "alice"); err != nil {
		t.Fatalf("save flow: %v", err)
	}
}

// seedNodeRecord writes one node record for a run of flow "g".
func seedNodeRecord(t *testing.T, h *gatewayHarness, runID, nodeID string, res *core.Result) {
	t.Helper()
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID(runID, nodeID), Kind: core.JobKindNode, Tenant: "t", Workspace: "ws",
		GraphID: "g", GraphRunID: runID, NodeID: nodeID, Status: core.JobStatusSucceeded,
		Job:    core.Job{GraphID: "g", NodeID: nodeID},
		Result: res,
	}); err != nil {
		t.Fatalf("seed node %s/%s: %v", runID, nodeID, err)
	}
}

func textResult(port, s string) *core.Result {
	return &core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{port: {MIME: "text/plain", Inline: s}},
	}
}

func getSamples(t *testing.T, h *gatewayHarness) samplesResponse {
	t.Helper()
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fg/samples", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("samples = %d; body=%s", rw.Code, rw.Body.String())
	}
	var out samplesResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestFlowSamples_ServesEachStepsLastOutput(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedSampleFlow(t, h)
	seedNodeRecord(t, h, "run-1", "src", rowsResult())
	seedNodeRecord(t, h, "run-1", "sink", textResult("out", "Ada"))

	got := getSamples(t, h)
	if got.Flow != "g" {
		t.Errorf("flow = %q, want g", got.Flow)
	}
	if _, ok := got.Nodes["src"]["out"]; !ok {
		t.Fatalf("no src output: %+v", got.Nodes)
	}
	if v := got.Nodes["sink"]["out"].Inline; v != "Ada" {
		t.Errorf("sink value = %v, want Ada", v)
	}
}

func TestFlowSamples_MergesAcrossRunsNewestWins(t *testing.T) {
	t.Parallel()
	// This is the case reading only the newest run gets wrong: sampling one
	// step runs its upstream chain alone, so the newest run covers `src` and
	// only an older run ever covered `sink`.
	h := newGatewayHarness(t)
	seedSampleFlow(t, h)
	seedNodeRecord(t, h, "run-old", "src", textResult("out", "stale"))
	seedNodeRecord(t, h, "run-old", "sink", textResult("out", "kept"))
	seedNodeRecord(t, h, "run-new", "src", textResult("out", "fresh"))

	got := getSamples(t, h)
	if v := got.Nodes["src"]["out"].Inline; v != "fresh" {
		t.Errorf("src = %v, want the newest run's value", v)
	}
	if v := got.Nodes["sink"]["out"].Inline; v != "kept" {
		t.Errorf("sink = %v, want the older run's value to survive", v)
	}
}

func TestFlowSamples_SkipsRecordsThatProducedNothing(t *testing.T) {
	t.Parallel()
	// A step that failed this morning should still show yesterday's output
	// rather than being blanked by the newer, empty record.
	h := newGatewayHarness(t)
	seedSampleFlow(t, h)
	seedNodeRecord(t, h, "run-old", "src", textResult("out", "yesterday"))
	seedNodeRecord(t, h, "run-new", "src", &core.Result{Status: core.StatusError})

	got := getSamples(t, h)
	if v := got.Nodes["src"]["out"].Inline; v != "yesterday" {
		t.Errorf("src = %v, want the last output it actually produced", v)
	}
}

func TestFlowSamples_IsScopedToItsOwnFlow(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedSampleFlow(t, h)
	seedNodeRecord(t, h, "run-1", "src", textResult("out", "mine"))
	// A node record belonging to a different flow, same workspace.
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-other", "src"), Kind: core.JobKindNode, Tenant: "t", Workspace: "ws",
		GraphID: "other", GraphRunID: "run-other", NodeID: "src", Status: core.JobStatusSucceeded,
		Result: textResult("out", "theirs"),
	}); err != nil {
		t.Fatalf("seed other: %v", err)
	}

	got := getSamples(t, h)
	if v := got.Nodes["src"]["out"].Inline; v != "mine" {
		t.Errorf("src = %v; another flow's record leaked in", v)
	}
}

func TestFlowSamples_UnknownFlowIsNotFound(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/flows/t%2Fws%2Fnope/samples", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("samples on a missing flow = %d, want 404; body=%s", rw.Code, rw.Body.String())
	}
}

func TestFlowSamples_DropsAnOversizedValueButKeepsThePort(t *testing.T) {
	t.Parallel()
	// A step that emitted tens of megabytes has nothing extra to say on a
	// 200px card, and shipping it would cost every editor load. The port and
	// its MIME survive so the card still names what flows.
	h := newGatewayHarness(t)
	seedSampleFlow(t, h)
	seedNodeRecord(t, h, "run-1", "src", textResult("out", strings.Repeat("x", maxSampleValueBytes+1)))

	got := getSamples(t, h)
	ref, ok := got.Nodes["src"]["out"]
	if !ok {
		t.Fatalf("port dropped entirely: %+v", got.Nodes)
	}
	if ref.Inline != nil {
		t.Errorf("oversized value was served (%d bytes)", len(ref.Inline.(string)))
	}
	if ref.MIME != "text/plain" {
		t.Errorf("MIME = %q, want the port's own", ref.MIME)
	}
}

func TestLatestOutputs_KeepsAValueUnderBudgetIntact(t *testing.T) {
	t.Parallel()
	recs := []core.JobRecord{{NodeID: "n", Result: textResult("out", "small")}}
	got := latestOutputs(recs, maxSampleValueBytes)
	if v := got["n"]["out"].Inline; v != "small" {
		t.Errorf("value = %v, want it passed through untouched", v)
	}
}
