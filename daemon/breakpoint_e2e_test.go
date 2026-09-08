// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

func waitForPaused(t *testing.T, bus *daemon.MemoryBus, graphRunID string, timeout time.Duration) daemon.PausedEvent {
	t.Helper()
	events, cancel := bus.Subscribe(graphRunID)
	defer cancel()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for paused event on %s", graphRunID)
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event channel closed without paused")
			}
			if ev.Paused != nil {
				return *ev.Paused
			}
		}
	}
}

func TestBreakpoint_PauseThenContinue(t *testing.T) {
	t.Parallel()
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "bp", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 5}, Breakpoint: true},
			{ID: "b", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "pass", To: "b", ToPort: "pass"},
		},
	}
	graphRunID, err := h.svc.SubmitGraphOpts(t.Context(), h.principal, g, daemon.SubmitOpts{Manual: true})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	paused := waitForPaused(t, h.bus, graphRunID, 5*time.Second)
	if paused.NodeID != "a" {
		t.Fatalf("paused after %q, want a", paused.NodeID)
	}
	// "b" must not have been enqueued yet.
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "b")); err == nil {
		t.Fatal("b dispatched before resume")
	}

	if err := h.svc.ResumeGraphRun(t.Context(), h.principal, graphRunID, false); err != nil {
		t.Fatalf("ResumeGraphRun: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", terminal.Status)
	}

	if err := h.svc.ResumeGraphRun(t.Context(), h.principal, graphRunID, false); err == nil {
		t.Fatal("ResumeGraphRun on a finished run should error")
	}
}

// A run nobody started by hand runs straight through its breakpoints. The
// breakpoint lives in the saved graph, so a published flow carries it into
// every fire of its trigger — and a paused run is deliberately never reaped
// (the reaper reads its un-dispatched dependents as work still outstanding),
// so honouring one on an unattended run left a non-terminal run behind for
// good, holding a concurrency slot with it.
func TestBreakpoint_UnattendedRunDoesNotPause(t *testing.T) {
	t.Parallel()
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "bptrigger", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 5}, Breakpoint: true},
			{ID: "b", Module: "delay", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "pass", To: "b", ToPort: "pass"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 10*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded — an unattended run must not park on a breakpoint", terminal.Status)
	}
}

func TestResumeGraphRun_NotFound(t *testing.T) {
	t.Parallel()
	h := newWorkerHarness(t, 0)
	if err := h.svc.ResumeGraphRun(context.Background(), h.principal, "ghost", false); err == nil {
		t.Fatal("ResumeGraphRun(ghost) = nil, want error")
	}
}
