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

type samplesResponse struct {
	Flow  string                         `json:"flow"`
	Nodes map[string]map[string]core.Ref `json:"nodes"`
}

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

// bigRows builds n rows each carrying roughly `each` bytes, so a caller can
// place a list either side of the budget deliberately.
func bigRows(n, each int) []any {
	rows := make([]any, n)
	for i := range rows {
		rows[i] = map[string]any{"title": strings.Repeat("x", each)}
	}
	return rows
}

func rowsRef(rows any) *core.Result {
	return &core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{"items": {MIME: "application/json", Inline: rows, Headers: []string{"title"}}},
	}
}

func TestLatestOutputs_OversizedRowsKeepAPrefixAndReportTheTotal(t *testing.T) {
	t.Parallel()
	// The case that matters: a feed read blows the budget on volume, not on any
	// one row. The first rows answer what the editor asks of a sample, so they
	// survive — and Truncated says how many there really were.
	budget := 1000
	recs := []core.JobRecord{{NodeID: "n", Result: rowsRef(bigRows(40, 100))}}

	got := latestOutputs(recs, budget)["n"]["items"]
	rows, ok := got.Inline.([]any)
	if !ok {
		t.Fatalf("value = %T, want a kept prefix of rows", got.Inline)
	}
	if len(rows) == 0 || len(rows) >= 40 {
		t.Errorf("kept %d rows, want a prefix shorter than the 40 emitted", len(rows))
	}
	if core.ApproxValueSize(rows, budget) >= budget {
		t.Errorf("kept prefix is still over the %d-byte budget", budget)
	}
	if got.Truncated != 40 {
		t.Errorf("Truncated = %d, want the true row count 40", got.Truncated)
	}
	if len(got.Headers) != 1 || got.Headers[0] != "title" {
		t.Errorf("Headers = %v, want the column order carried onto the prefix", got.Headers)
	}
}

func TestLatestOutputs_TruncatesDropNativeRowsToo(t *testing.T) {
	t.Parallel()
	// A drop emits []map[string]any and a JSON round-trip yields []any. Both are
	// the same rows to a reader, so both must truncate — otherwise how much a
	// sample keeps would depend on which store served it.
	budget := 1000
	native := make([]map[string]any, 40)
	for i := range native {
		native[i] = map[string]any{"title": strings.Repeat("x", 100)}
	}
	recs := []core.JobRecord{{NodeID: "n", Result: rowsRef(native)}}

	got := latestOutputs(recs, budget)["n"]["items"]
	rows, ok := got.Inline.([]any)
	if !ok {
		t.Fatalf("value = %T, want a kept prefix of rows", got.Inline)
	}
	if len(rows) == 0 || len(rows) >= 40 {
		t.Errorf("kept %d rows, want a prefix shorter than the 40 emitted", len(rows))
	}
	if got.Truncated != 40 {
		t.Errorf("Truncated = %d, want the true row count 40", got.Truncated)
	}
}

func TestLatestOutputs_ASingleOversizedRowDropsTheValueButStillCounts(t *testing.T) {
	t.Parallel()
	// No prefix fits, so there is nothing honest to serve — but the count still
	// lets a reader say "3 items, too large to show" instead of "no data yet".
	budget := 1000
	recs := []core.JobRecord{{NodeID: "n", Result: rowsRef(bigRows(3, budget*2))}}

	got := latestOutputs(recs, budget)["n"]["items"]
	if got.Inline != nil {
		t.Errorf("value = %v, want it dropped: not even one row fits", got.Inline)
	}
	if got.Truncated != 3 {
		t.Errorf("Truncated = %d, want the row count 3", got.Truncated)
	}
	if got.MIME != "application/json" {
		t.Errorf("MIME = %q, want the port's own to survive", got.MIME)
	}
}

func TestLatestOutputs_AnOversizedStringIsNeverCutInHalf(t *testing.T) {
	t.Parallel()
	// A prefix of a JSON document or a CSV is not a shorter document — it is a
	// broken one, with nothing in the payload to say so.
	budget := 1000
	recs := []core.JobRecord{{NodeID: "n", Result: textResult("out", strings.Repeat("x", budget*2))}}

	got := latestOutputs(recs, budget)["n"]["out"]
	if got.Inline != nil {
		t.Errorf("value = %v, want an oversized string dropped whole", got.Inline)
	}
	if got.Truncated != 0 {
		t.Errorf("Truncated = %d, want 0: a string has no row count to report", got.Truncated)
	}
}

func TestLatestOutputs_AListUnderBudgetIsNotMarkedTruncated(t *testing.T) {
	t.Parallel()
	recs := []core.JobRecord{{NodeID: "n", Result: rowsRef(bigRows(3, 10))}}
	got := latestOutputs(recs, maxSampleValueBytes)["n"]["items"]
	if got.Truncated != 0 {
		t.Errorf("Truncated = %d on a complete value, want 0", got.Truncated)
	}
	if rows, _ := got.Inline.([]any); len(rows) != 3 {
		t.Errorf("kept %d rows, want all 3 passed through untouched", len(rows))
	}
}
