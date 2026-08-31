// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// The run viewer's "Inputs" section reads `inputs` off a node record, and a node
// record has never carried them: the dispatcher enqueues a Job holding only the
// graph and node id, and the engine assembles the inputs in memory when it
// executes. So the section was dead from the day it was written.
//
// The API now rebuilds them from what the run DOES store — its graph payload
// plus the upstream nodes' outputs — through the engine's own AssembleInput, so
// the explanation can't drift from what ran.

// seedRun writes a graph run and its node records: `src` produced rows, `sink`
// consumed them over an edge.
func seedInputsRun(t *testing.T, h *gatewayHarness, runID string, srcResult *core.Result) core.Graph {
	t.Helper()
	g := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "json", Params: map[string]any{"json": "[]"}},
			{ID: "sink", Module: "sort_rows", Params: map[string]any{"by": "name"}},
		},
		Edges: []core.Edge{{From: "src", FromPort: "out", To: "sink", ToPort: "rows"}},
	}
	payload, _ := json.Marshal(g)
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusSucceeded, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	for _, n := range []struct {
		id  string
		res *core.Result
	}{{"src", srcResult}, {"sink", &core.Result{Status: core.StatusOK}}} {
		rec := core.JobRecord{
			ID: NodeJobID(runID, n.id), Kind: core.JobKindNode, Tenant: "t", Workspace: "ws",
			GraphID: "g", GraphRunID: runID, NodeID: n.id, Status: core.JobStatusSucceeded,
			Job:    core.Job{GraphID: "g", NodeID: n.id},
			Result: n.res,
		}
		if err := h.store.Enqueue(t.Context(), rec); err != nil {
			t.Fatalf("seed node %s: %v", n.id, err)
		}
	}
	return g
}

func rowsResult() *core.Result {
	return &core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: "application/json", Inline: []any{map[string]any{"name": "Ada"}}},
		},
	}
}

type nodesListResponse struct {
	Nodes []struct {
		NodeID string              `json:"node_id"`
		Inputs map[string]core.Ref `json:"inputs"`
	} `json:"nodes"`
}

func TestRunNodes_InputsRebuiltFromUpstreamOutputs(t *testing.T) {
	h := newGatewayHarness(t)
	seedInputsRun(t, h, "run-in", rowsResult())

	rw := h.do(t, "GET", "/api/v1/me/runs/run-in/nodes", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list nodes = %d; body=%s", rw.Code, rw.Body.String())
	}
	var out nodesListResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sink map[string]core.Ref
	for _, n := range out.Nodes {
		if n.NodeID == "sink" {
			sink = n.Inputs
		}
	}
	// The consumer received the producer's rows on the port the edge names.
	if _, ok := sink["rows"]; !ok {
		t.Fatalf("sink has no `rows` input: %s", rw.Body.String())
	}
	if got := sink["rows"].MIME; got != "application/json" {
		t.Errorf("input MIME = %q, want the upstream ref's own", got)
	}
}

func TestRunNode_InputsRebuiltForOneNode(t *testing.T) {
	// The single-node endpoint has to read the predecessors itself.
	h := newGatewayHarness(t)
	seedInputsRun(t, h, "run-one", rowsResult())

	rw := h.do(t, "GET", "/api/v1/me/runs/run-one/nodes/sink", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get node = %d; body=%s", rw.Code, rw.Body.String())
	}
	var view struct {
		Inputs map[string]core.Ref `json:"inputs"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := view.Inputs["rows"]; !ok {
		t.Fatalf("no `rows` input: %s", rw.Body.String())
	}
}

func TestRunNode_NoInputsWhenUpstreamProducedNothing(t *testing.T) {
	// A predecessor with no output (failed, skipped, or pruned by retention)
	// leaves the section absent rather than inventing an empty port.
	h := newGatewayHarness(t)
	seedInputsRun(t, h, "run-empty", &core.Result{Status: core.StatusOK})

	rw := h.do(t, "GET", "/api/v1/me/runs/run-empty/nodes/sink", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get node = %d; body=%s", rw.Code, rw.Body.String())
	}
	if body := rw.Body.String(); jsonHasKey(t, body, "inputs") {
		t.Errorf("expected no inputs key, got %s", body)
	}
}

func TestRunNodes_SourceNodeHasNoInputs(t *testing.T) {
	// A node with no inbound edges consumed nothing; the section must not
	// appear on it just because the run has inputs elsewhere.
	h := newGatewayHarness(t)
	seedInputsRun(t, h, "run-src", rowsResult())

	rw := h.do(t, "GET", "/api/v1/me/runs/run-src/nodes/src", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get node = %d; body=%s", rw.Code, rw.Body.String())
	}
	if body := rw.Body.String(); jsonHasKey(t, body, "inputs") {
		t.Errorf("a source node should have no inputs: %s", body)
	}
}

func TestRunNode_InputsSurviveAMissingGraphPayload(t *testing.T) {
	// An old run whose payload is gone must still return its node, without the
	// inputs — best-effort, not a 500.
	h := newGatewayHarness(t)
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-bare", Kind: core.JobKindGraph, Tenant: "t", Workspace: "ws",
		GraphID: "g", NodeID: "*", Status: core.JobStatusSucceeded,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := h.store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-bare", "sink"), Kind: core.JobKindNode, Tenant: "t", Workspace: "ws",
		GraphID: "g", GraphRunID: "run-bare", NodeID: "sink", Status: core.JobStatusSucceeded,
		Job: core.Job{GraphID: "g", NodeID: "sink"},
	}); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	rw := h.do(t, "GET", "/api/v1/me/runs/run-bare/nodes/sink", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get node = %d; body=%s", rw.Code, rw.Body.String())
	}
}

// jsonHasKey reports whether the top-level object has the key at all — the
// distinction that matters here is present-and-empty versus omitted.
func jsonHasKey(t *testing.T, body, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, ok := m[key]
	return ok
}
