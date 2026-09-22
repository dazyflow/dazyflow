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

// lateTerminalBus hands WaitGraph a subscription on which, a moment after it
// subscribes, the run is cancelled and a bare Terminal (no GraphRes) arrives —
// what cancel, timeout, the reaper and promotion all publish.
type lateTerminalBus struct {
	jobs  core.JobStore
	jerr  *core.JobError
	runID string
}

func (b *lateTerminalBus) Publish(string, BusEvent) {}

func (b *lateTerminalBus) Subscribe(string) (<-chan BusEvent, func()) {
	ch := make(chan BusEvent, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = b.jobs.Complete(context.Background(), b.runID, core.JobStatusCancelled,
			&core.Result{Status: core.StatusError, Error: b.jerr})
		ch <- BusEvent{Terminal: &TerminalEvent{JobID: b.runID, Status: core.JobStatusCancelled, Error: b.jerr}}
	}()
	return ch, func() {}
}

func TestWaitGraph_BareTerminalReportsTheRecord(t *testing.T) {
	t.Parallel()
	jobs := jobstore.NewMemory()
	ctx := t.Context()
	if err := jobs.Enqueue(ctx, core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	jerr := &core.JobError{Code: CancelCodeTimeout, Message: "run exceeded its timeout"}
	svc := &Service{Jobs: jobs, Bus: &lateTerminalBus{jobs: jobs, jerr: jerr, runID: "run-1"}}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws"}

	res, err := svc.WaitGraph(ctx, p, "run-1", nil)
	if err != nil {
		t.Fatalf("WaitGraph: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != CancelCodeTimeout {
		t.Fatalf("result = %+v, want the cancelled run's error", res)
	}
}

// A node claimed after its run was cancelled must not execute: Claim does not
// look at the run, so the worker has to.
func TestWorker_DoesNotRunANodeOfACancelledRun(t *testing.T) {
	t.Parallel()
	jobs := jobstore.NewMemory()
	ctx := t.Context()
	g := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	payload, _ := json.Marshal(g)
	if err := jobs.Enqueue(ctx, core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		NodeID: "*", Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Complete(ctx, "run-1", core.JobStatusCancelled, &core.Result{Status: core.StatusError}); err != nil {
		t.Fatal(err)
	}
	// Enqueued after the cancel, as a dependent racing the sweep would be.
	if err := jobs.Enqueue(ctx, core.JobRecord{
		ID: NodeJobID("run-1", "a"), Kind: core.JobKindNode, GraphRunID: "run-1", GraphID: "g",
		NodeID: "a", Tenant: "t", Workspace: "ws",
		Job: core.Job{ID: NodeJobID("run-1", "a"), GraphID: "g", NodeID: "a"},
	}); err != nil {
		t.Fatal(err)
	}

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	w := NewWorker(WorkerConfig{ID: "w"}, jobs, eng, NewMemoryBus())
	rec, err := jobs.Claim(ctx, "w", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	w.processNodeJob(ctx, rec)

	got, _ := jobs.Get(ctx, NodeJobID("run-1", "a"))
	if got.Status != core.JobStatusCancelled {
		t.Fatalf("node of a cancelled run is %s, want cancelled (not executed)", got.Status)
	}
}
