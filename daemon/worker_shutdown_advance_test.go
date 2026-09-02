// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// Once a node reaches a definite outcome, every write that ADVANCES the run —
// the terminal record, the dependent dispatch, the completion check — must be
// detached from the claim context. If a SIGTERM lands between the terminal
// write and the dispatch, the node ends terminal with its dependents never
// enqueued, and ReapStuckGraphRuns cannot recover that: maybeCompleteGraph
// bails on a MISSING node record, so the run sits in "running" forever with no
// transition left to re-fire it.
//
// processNodeJob's main terminal path always got this right. The disabled-node
// skip path and failNode did not — they dispatched on the live claim ctx. These
// tests drive both paths with an already-cancelled context, which is the
// shutdown race made deterministic.

// ctxHonoringStore models a store that fails once its context is cancelled —
// which is what production does (pgx surfaces context.Canceled on every query).
// The in-memory store ignores ctx entirely, so wrapping it is what makes a
// cancelled-context test meaningful rather than vacuously green.
type ctxHonoringStore struct {
	*jobstore.Memory
}

func (s ctxHonoringStore) Enqueue(ctx context.Context, rec core.JobRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Memory.Enqueue(ctx, rec)
}

func (s ctxHonoringStore) Get(ctx context.Context, jobID string) (core.JobRecord, error) {
	if err := ctx.Err(); err != nil {
		return core.JobRecord{}, err
	}
	return s.Memory.Get(ctx, jobID)
}

func (s ctxHonoringStore) Complete(ctx context.Context, jobID string, status core.JobStatus, result *core.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Memory.Complete(ctx, jobID, status, result)
}

func (s ctxHonoringStore) CompleteOwned(ctx context.Context, jobID, worker string, status core.JobStatus, result *core.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Memory.CompleteOwned(ctx, jobID, worker, status, result)
}

func (s ctxHonoringStore) ListNodeRecords(ctx context.Context, opts core.ListNodeRecordsOpts) ([]core.JobRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Memory.ListNodeRecords(ctx, opts)
}

// shutdownHarness builds a worker over a ctx-honoring store with one graph run
// enqueued, and claims the named node so processNodeJob/failNode see a record
// they own.
type shutdownHarness struct {
	w     *Worker
	jobs  ctxHonoringStore
	runID string
	rec   core.JobRecord
}

func newShutdownHarness(t *testing.T, g core.Graph, claimNode string) *shutdownHarness {
	t.Helper()
	jobs := ctxHonoringStore{jobstore.NewMemory()}
	bus := NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}

	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	runID := "run-" + g.ID
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
		Tenant: g.Tenant, Workspace: g.Workspace,
		Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue graph run: %v", err)
	}
	if err := jobs.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID(runID, claimNode), Kind: core.JobKindNode,
		GraphRunID: runID, GraphID: g.ID, NodeID: claimNode,
		Tenant: g.Tenant, Workspace: g.Workspace,
	}); err != nil {
		t.Fatalf("enqueue node: %v", err)
	}

	w := NewWorker(WorkerConfig{
		ID: "shutdown-w",
		// Long enough that the renew goroutine never ticks during the test.
		LeaseDuration:   time.Minute,
		LeaseRenewEvery: time.Minute,
	}, jobs, eng, bus)

	// Claim under a live context — the node was picked up just before SIGTERM.
	rec, err := jobs.Claim(t.Context(), w.cfg.ID, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if rec.NodeID != claimNode {
		t.Fatalf("claimed %q, want %q", rec.NodeID, claimNode)
	}
	return &shutdownHarness{w: w, jobs: jobs, runID: runID, rec: rec}
}

func (h *shutdownHarness) status(t *testing.T, nodeID string) core.JobStatus {
	t.Helper()
	rec, err := h.jobs.Get(context.Background(), NodeJobID(h.runID, nodeID))
	if err != nil {
		t.Fatalf("node %q has no record — its dependents were never dispatched: %v", nodeID, err)
	}
	return rec.Status
}

func (h *shutdownHarness) runStatus(t *testing.T) core.JobStatus {
	t.Helper()
	rec, err := h.jobs.Get(context.Background(), h.runID)
	if err != nil {
		t.Fatalf("get graph run: %v", err)
	}
	return rec.Status
}

// cancelledCtx is a context already past cancellation — the state the claim
// loop's ctx is in once SIGTERM has been handled.
func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// A disabled node skipped during shutdown still cascades and finalizes the run.
func TestWorker_DisabledSkipAdvancesRunDespiteCancelledCtx(t *testing.T) {
	t.Parallel()
	h := newShutdownHarness(t, core.Graph{
		ID: "disabled-shutdown", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "off", Module: "delay", Params: map[string]any{"ms": 1}, Disabled: true},
			{ID: "tail", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{{From: "off", FromPort: "pass", To: "tail", ToPort: "pass"}},
	}, "off")

	h.w.processNodeJob(cancelledCtx(), h.rec)

	if got := h.status(t, "off"); got != core.JobStatusSkipped {
		t.Errorf("off status = %q, want skipped", got)
	}
	// The load-bearing assertion: the dependent must have been recorded. With
	// the dispatch on a cancelled ctx it never is, and the run strands.
	if got := h.status(t, "tail"); got != core.JobStatusSkipped {
		t.Errorf("tail status = %q, want skipped (skip cascade)", got)
	}
	if got := h.runStatus(t); !core.IsTerminalStatus(got) {
		t.Errorf("graph run status = %q, want terminal — the run stranded", got)
	}
}

// A node failed during shutdown still propagates and finalizes the run.
func TestWorker_FailNodeAdvancesRunDespiteCancelledCtx(t *testing.T) {
	t.Parallel()
	g := core.Graph{
		ID: "fail-shutdown", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "b", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{{From: "a", FromPort: "pass", To: "b", ToPort: "pass"}},
	}
	h := newShutdownHarness(t, g, "a")

	h.w.failNode(cancelledCtx(), h.rec, "load_graph", "simulated", &g)

	if got := h.status(t, "a"); got != core.JobStatusFailed {
		t.Errorf("a status = %q, want failed", got)
	}
	// "a" has a default (non-tolerant) outgoing edge, so the failure propagates
	// and the run must be finalized rather than left running forever.
	if got := h.runStatus(t); got != core.JobStatusFailed {
		t.Errorf("graph run status = %q, want failed — the run stranded", got)
	}
}
