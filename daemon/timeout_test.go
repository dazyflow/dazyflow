// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// TestGraphTimeout_AutoCancels submits a run with a very short
// per-graph timeout and asserts the daemon auto-cancels it. The graph
// has a single noop node so the worker never actually runs (no
// engine is wired in the harness) — the only thing that should move
// the graph-record to terminal is the watchdog.
func TestGraphTimeout_AutoCancels(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	g := core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Visibility:     core.VisibilityOrg,
		TimeoutSeconds: 1, // smallest legal value; test waits a bit longer
		Nodes: []core.Node{
			{ID: "n1", Module: "noop"},
		},
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save: %v", err)
	}

	runID, err := h.svc.SubmitGraph(ctx, h.alice, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Poll the record until either it goes Cancelled (good) or the
	// deadline elapses (bad). Polling rather than sleeping unconditionally
	// keeps the test fast on machines where the goroutine scheduler
	// is generous.
	deadline := time.Now().Add(4 * time.Second)
	for {
		rec, err := h.svc.Jobs.Get(ctx, runID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rec.Status == core.JobStatusCancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("graph record stuck in %q, want cancelled", rec.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGraphTimeout_NoTimeoutLeftAlone verifies that without a per-graph
// timeout OR a daemon default, no watchdog fires — a long-running flow
// shouldn't get killed just for being slow when no timeout was asked for.
func TestGraphTimeout_NoTimeoutLeftAlone(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	ctx := context.Background()

	g := core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
		Nodes:      []core.Node{{ID: "n1", Module: "noop"}},
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	runID, err := h.svc.SubmitGraph(ctx, h.alice, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Wait longer than the smallest test-only timeout to prove no
	// cancellation fires when no timeout is configured.
	time.Sleep(1500 * time.Millisecond)
	rec, _ := h.svc.Jobs.Get(ctx, runID)
	if rec.Status == core.JobStatusCancelled {
		t.Fatalf("run was cancelled without a configured timeout")
	}
}

// TestGraphTimeout_CeilingApplies verifies the operator ceiling
// (DAZYFLOW_MAX_GRAPH_TIMEOUT) becomes the de-facto cap when a graph
// declares no TimeoutSeconds of its own.
func TestGraphTimeout_CeilingApplies(t *testing.T) {
	t.Parallel()
	h := newVisibilityHarness(t)
	h.svc.MaxGraphTimeoutSeconds = 1
	ctx := context.Background()

	g := core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityOrg,
		// No TimeoutSeconds — the ceiling fills in.
		Nodes: []core.Node{{ID: "n1", Module: "noop"}},
	}
	if _, err := h.svc.SaveGraph(ctx, h.alice, g); err != nil {
		t.Fatalf("save: %v", err)
	}
	runID, err := h.svc.SubmitGraph(ctx, h.alice, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		rec, err := h.svc.Jobs.Get(ctx, runID)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				t.Fatal("graph record disappeared")
			}
			t.Fatalf("get: %v", err)
		}
		if rec.Status == core.JobStatusCancelled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ceiling timeout did not fire; status=%q", rec.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
