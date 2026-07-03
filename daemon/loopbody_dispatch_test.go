// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// loopHarness wires a fake "for_each" (succeeds, emits results) plus a body
// fixture that counts its invocations, so we can assert the dispatcher never
// runs loop-body nodes standalone while the parent run still completes.
type loopHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       *daemon.MemoryBus
	principal core.Principal
	bodyRuns  *atomic.Int32
}

func newLoopHarness(t *testing.T) *loopHarness {
	t.Helper()

	bodyRuns := &atomic.Int32{}
	reg := engine.NewRegistry()

	// source — emits a constant ref on "out".
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "source", Version: "1.0", Summary: "src",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Outputs: []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: "items-" + job.NodeID}}}, nil
		},
	})

	// for_each — succeeds and emits a results ref. The "body" output pin is
	// what the dispatcher keys on (via the edge's FromPort), so the manifest
	// just needs the module ID to be "for_each".
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "for_each", Version: "1.0", Summary: "loop",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:  []core.Port{{Port: "items"}},
			Outputs: []core.Port{{Port: "body"}, {Port: "results"}, {Port: "errors"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK,
				Output: map[string]core.Ref{"results": {Ref: "results-" + job.NodeID}}}, nil
		},
	})

	// body — counts invocations. Must NEVER run in Phase 1 (no per-item
	// execution yet), so a non-zero count is a dispatch-exclusion bug.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "bodyfx", Version: "1.0", Summary: "body fixture",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:  []core.Port{{Port: "in"}},
			Outputs: []core.Port{{Port: "meta"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			bodyRuns.Add(1)
			return core.Result{JobID: job.ID, Status: core.StatusOK}, nil
		},
	})

	// after — a downstream consumer of loop.results; a normal node that
	// MUST run once the loop completes.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "after", Version: "1.0", Summary: "after",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			Inputs:  []core.Port{{Port: "in"}},
			Outputs: []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK}, nil
		},
	})

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}

	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	return &loopHarness{svc: svc, jobs: jobs, bus: bus, principal: p, bodyRuns: bodyRuns}
}

// A graph with a for_each "body" pin must complete, the loop body must never
// run standalone, and a normal node fed from loop.results must still run.
func TestLoopBody_NotDispatchedStandalone(t *testing.T) {
	h := newLoopHarness(t)

	g := core.Graph{
		ID: "loop-body", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "source"},
			{ID: "loop", Module: "for_each"},
			{ID: "body", Module: "bodyfx"},
			{ID: "tail", Module: "bodyfx"}, // downstream of body — also loop-owned
			{ID: "after", Module: "after"},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "body", ToPort: "in"},
			{From: "body", FromPort: "meta", To: "tail", ToPort: "in"},
			{From: "loop", FromPort: "results", To: "after", ToPort: "in"},
		},
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}

	if n := h.bodyRuns.Load(); n != 0 {
		t.Errorf("loop body ran %d time(s) standalone; want 0", n)
	}
	// Body nodes hold no record in the parent run.
	for _, id := range []string{"body", "tail"} {
		if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, id)); err == nil {
			t.Errorf("%q should have no parent-run record (loop-owned)", id)
		}
	}
	// Normal nodes ran.
	for _, id := range []string{"src", "loop", "after"} {
		rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, id))
		if err != nil || rec.Status != core.JobStatusSucceeded {
			t.Errorf("%q status = %q (err=%v), want succeeded", id, rec.Status, err)
		}
	}
}
