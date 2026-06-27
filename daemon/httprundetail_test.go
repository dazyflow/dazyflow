// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestHTTPGateway_ListRunNodes_ReturnsAllNodesForRun(t *testing.T) {
	// Seed a run-record + three node-records under it, plus one
	// node-record under a DIFFERENT run. The endpoint must return
	// only the three from the requested run.
	h := newGatewayHarness(t)

	runRec := core.JobRecord{
		ID:           "run-aaa",
		Kind:         core.JobKindGraph,
		GraphID:      "g",
		Tenant:       "t",
		Workspace:    "ws",
		Status:       core.JobStatusSucceeded,
		GraphPayload: []byte(`{"id":"g","tenant":"t","workspace":"ws"}`),
	}
	_ = h.store.Enqueue(t.Context(), runRec)

	for _, nid := range []string{"step1", "step2", "step3"} {
		_ = h.store.Enqueue(t.Context(), core.JobRecord{
			ID:         NodeJobID("run-aaa", nid),
			Kind:       core.JobKindNode,
			GraphRunID: "run-aaa",
			GraphID:    "g",
			NodeID:     nid,
			Tenant:     "t",
			Workspace:  "ws",
			Status:     core.JobStatusSucceeded,
		})
	}
	// Decoy: a node under a different run that must NOT leak.
	otherRun := core.JobRecord{
		ID:           "run-bbb",
		Kind:         core.JobKindGraph,
		GraphID:      "g",
		Tenant:       "t",
		Workspace:    "ws",
		Status:       core.JobStatusSucceeded,
		GraphPayload: []byte(`{"id":"g","tenant":"t","workspace":"ws"}`),
	}
	_ = h.store.Enqueue(t.Context(), otherRun)
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID:         NodeJobID("run-bbb", "step1"),
		Kind:       core.JobKindNode,
		GraphRunID: "run-bbb",
		GraphID:    "g",
		NodeID:     "step1",
		Tenant:     "t",
		Workspace:  "ws",
		Status:     core.JobStatusSucceeded,
	})

	rw := h.do(t, "GET", "/api/v1/me/runs/run-aaa/nodes", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Nodes []nodeRunView `json:"nodes"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (must not include run-bbb's): %+v", len(resp.Nodes), resp.Nodes)
	}
	// run-bbb also has a "step1"; a cross-run leak would push the count to
	// 4 (caught above) — so membership of exactly the three expected ids is
	// the leak check here.
	got := map[string]bool{}
	for _, n := range resp.Nodes {
		got[n.NodeID] = true
	}
	for _, want := range []string{"step1", "step2", "step3"} {
		if !got[want] {
			t.Errorf("missing node %q", want)
		}
	}
}

func TestNewNodeRunView_RetrySignal(t *testing.T) {
	future := time.Now().Add(30 * time.Second)
	at := &future
	// A node between attempts: queued, attempt>0, with a future horizon.
	requeued := core.JobRecord{
		NodeID:      "n",
		Status:      core.JobStatusQueued,
		Attempt:     2,
		AvailableAt: at,
	}
	v := newNodeRunView(requeued)
	if !v.WillRetry {
		t.Error("requeued node should set will_retry")
	}
	if v.RetryAt == nil || !v.RetryAt.Equal(*at) {
		t.Errorf("retry_at = %v, want %v", v.RetryAt, at)
	}

	// A terminally failed node must NOT advertise a retry — that's "needs you".
	failed := core.JobRecord{NodeID: "n", Status: core.JobStatusFailed, Attempt: 3}
	if fv := newNodeRunView(failed); fv.WillRetry || fv.RetryAt != nil {
		t.Errorf("terminal failure must not set retry signal, got will_retry=%v retry_at=%v", fv.WillRetry, fv.RetryAt)
	}

	// A first-time queued node (attempt 0, no horizon) is not a retry.
	fresh := core.JobRecord{NodeID: "n", Status: core.JobStatusQueued}
	if fv := newNodeRunView(fresh); fv.WillRetry {
		t.Error("a fresh queued node should not set will_retry")
	}
}

func TestHTTPGateway_ListRunNodes_UnknownRunIs404(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/me/runs/never-existed/nodes", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rw.Code)
	}
}

func TestHTTPGateway_ListRunNodes_EmptyListForRunWithNoNodes(t *testing.T) {
	// A graph that ran but recorded zero node records (degenerate
	// case) should return an empty array, not nil — keeps the UI
	// code simple.
	h := newGatewayHarness(t)
	_ = h.store.Enqueue(t.Context(), core.JobRecord{
		ID:           "run-empty",
		Kind:         core.JobKindGraph,
		GraphID:      "g",
		Tenant:       "t",
		Workspace:    "ws",
		Status:       core.JobStatusSucceeded,
		GraphPayload: []byte(`{"id":"g","tenant":"t","workspace":"ws"}`),
	})
	rw := h.do(t, "GET", "/api/v1/me/runs/run-empty/nodes", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Nodes []core.JobRecord `json:"nodes"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp.Nodes == nil {
		t.Error("nodes is nil; want empty array for empty-result determinism")
	}
	if len(resp.Nodes) != 0 {
		t.Errorf("got %d nodes, want 0", len(resp.Nodes))
	}
}
