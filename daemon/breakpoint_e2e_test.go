// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
)

// waitForPaused subscribes and blocks until a Paused event arrives.
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

// TestBreakpoint_PauseThenContinue covers the breakpoint pause path
// (shouldPauseAfter, addPaused) and Service.ResumeGraphRun → resumeFrom: a
// node carrying a breakpoint holds the run until Continue re-drives it.
func TestBreakpoint_PauseThenContinue(t *testing.T) {
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
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The run pauses after "a" (breakpoint) without dispatching "b".
	paused := waitForPaused(t, h.bus, graphRunID, 5*time.Second)
	if paused.NodeID != "a" {
		t.Fatalf("paused after %q, want a", paused.NodeID)
	}
	// "b" must not have been enqueued yet.
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "b")); err == nil {
		t.Fatal("b dispatched before resume")
	}

	// Continue (step=false): resumeFrom dispatches b and the graph completes.
	if err := h.svc.ResumeGraphRun(t.Context(), h.principal, graphRunID, false); err != nil {
		t.Fatalf("ResumeGraphRun: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", terminal.Status)
	}

	// Resuming a run that's no longer paused is a conflict.
	if err := h.svc.ResumeGraphRun(t.Context(), h.principal, graphRunID, false); err == nil {
		t.Fatal("ResumeGraphRun on a finished run should error")
	}
}

// TestResumeGraphRun_NotFound covers the early-return guards of
// ResumeGraphRun: an unknown run id surfaces an error.
func TestResumeGraphRun_NotFound(t *testing.T) {
	h := newWorkerHarness(t, 0)
	if err := h.svc.ResumeGraphRun(context.Background(), h.principal, "ghost", false); err == nil {
		t.Fatal("ResumeGraphRun(ghost) = nil, want error")
	}
}
