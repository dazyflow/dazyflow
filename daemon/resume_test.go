// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// TestResumeFailedRun_ReusesUpstreamAndRerunsFrontier is the core guarantee
// of resume-from-failure: a succeeded upstream node is reused (NOT
// re-executed) via the seed path, while the failed node re-runs. Here the
// upstream "counter" records how many times it actually executes, and the
// downstream "failonce" fails the first time and succeeds the second — so a
// successful resume that doesn't re-run the upstream proves both halves.
func TestResumeFailedRun_ReusesUpstreamAndRerunsFrontier(t *testing.T) {
	t.Parallel()
	var upstreamRuns atomic.Int32
	var downstreamRuns atomic.Int32

	reg := engine.NewRegistry()
	// counter: always succeeds with an INLINE output (so it's reusable as a
	// seed — scratch Refs would be reclaimed on failure), counting runs.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "counter",
			Version:        "1.0",
			Summary:        "Test fixture: counts executions, inline output.",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs:        []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			upstreamRuns.Add(1)
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Inline: "value"}},
			}, nil
		},
	})
	// failonce: fails on its first-ever execution, succeeds thereafter.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "failonce",
			Version:        "1.0",
			Summary:        "Test fixture: fails first execution, then succeeds.",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs:         []core.Port{{Port: "in"}},
			Outputs:        []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			if downstreamRuns.Add(1) == 1 {
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusError,
					Error:  &core.JobError{Code: "boom", Message: "first run fails"},
				}, nil
			}
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Inline: "ok"}},
			}, nil
		},
	})

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
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
		Usage:      daemon.NewMemUsageStore(),
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:           "w",
		PollInterval: 5 * time.Millisecond,
		MaxRetries:   1, // no in-run retry — we test cross-run resume
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "resume-flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "up", Module: "counter"},
			{ID: "down", Module: "failonce"},
		},
		Edges: []core.Edge{
			{From: "up", FromPort: "out", To: "down", ToPort: "in"},
		},
	}

	// First run: up succeeds, down fails → graph fails.
	run1, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("submit run1: %v", err)
	}
	term1 := waitForTerminalEvent(t, bus, jobs, run1, 5*time.Second)
	if term1.Status != core.JobStatusFailed {
		t.Fatalf("run1 status = %q, want failed", term1.Status)
	}
	if got := upstreamRuns.Load(); got != 1 {
		t.Fatalf("upstream ran %d times in run1, want 1", got)
	}

	// Resume: up must be reused (seeded, not re-run); down re-runs and now
	// succeeds → graph succeeds.
	run2, err := svc.ResumeFailedRun(t.Context(), p, run1)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if run2 == run1 {
		t.Fatalf("resume returned the same run id %q, want a new run", run2)
	}
	term2 := waitForTerminalEvent(t, bus, jobs, run2, 5*time.Second)
	if term2.Status != core.JobStatusSucceeded {
		t.Fatalf("run2 status = %q, want succeeded (err=%+v)", term2.Status, term2.Error)
	}

	// The upstream must NOT have executed again — it was seeded from run1.
	if got := upstreamRuns.Load(); got != 1 {
		t.Errorf("upstream ran %d times total, want 1 (resume must reuse it, not re-run)", got)
	}
	// The downstream must have re-run (1 in run1 + 1 in run2).
	if got := downstreamRuns.Load(); got != 2 {
		t.Errorf("downstream ran %d times total, want 2 (failed node must re-run)", got)
	}
	// The seeded upstream record exists in run2, succeeded, with its output.
	upRec, err := jobs.Get(t.Context(), daemon.NodeJobID(run2, "up"))
	if err != nil {
		t.Fatalf("get seeded upstream in run2: %v", err)
	}
	if upRec.Status != core.JobStatusSucceeded {
		t.Errorf("seeded upstream status = %q, want succeeded", upRec.Status)
	}
}

// TestResumeFailedRun_RejectsRunningRun confirms only terminal-but-incomplete
// runs (failed/cancelled) can be retried — a still-running run is a conflict.
func TestResumeFailedRun_RejectsRunningRun(t *testing.T) {
	t.Parallel()
	ks := auth.NewMemKeyStore()
	jobs := jobstore.NewMemory()
	ws, _ := workspace.OpenFS("")
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
	}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}}}

	// A running graph record.
	_ = jobs.Enqueue(t.Context(), core.JobRecord{
		ID: "live-run", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*",
		Tenant: "t", Workspace: "ws", Status: core.JobStatusRunning,
	})
	_, err := svc.ResumeFailedRun(t.Context(), p, "live-run")
	if err == nil {
		t.Fatalf("ResumeFailedRun on a running run = nil error, want conflict")
	}
}
