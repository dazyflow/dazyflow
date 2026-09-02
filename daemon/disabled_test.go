// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

// A disabled node is recorded as skipped without executing, and the skip
// cascades: everything downstream of it is skipped too, while independent
// branches still run and the graph completes successfully.
func TestDisabled_SkipsNodeAndPrunesDownstream(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// src1 → mid(DISABLED) → tail   (mid + tail must be skipped)
	// src2 → side                   (independent branch, must run)
	g := core.Graph{
		ID: "disabled-cascade", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src1", Module: "source"},
			{ID: "mid", Module: "delay", Params: map[string]any{"ms": 1}, Disabled: true},
			{ID: "tail", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "src2", Module: "source"},
			{ID: "side", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "src1", FromPort: "out", To: "mid", ToPort: "pass"},
			{From: "mid", FromPort: "pass", To: "tail", ToPort: "pass"},
			{From: "src2", FromPort: "out", To: "side", ToPort: "pass"},
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

	wantStatus := map[string]core.JobStatus{
		"src1": core.JobStatusSucceeded,
		"mid":  core.JobStatusSkipped,
		"tail": core.JobStatusSkipped,
		"src2": core.JobStatusSucceeded,
		"side": core.JobStatusSucceeded,
	}
	for nodeID, want := range wantStatus {
		rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, nodeID))
		if err != nil {
			t.Errorf("%s: %v", nodeID, err)
			continue
		}
		if rec.Status != want {
			t.Errorf("%s status = %q, want %q", nodeID, rec.Status, want)
		}
	}
}

// A pass→pass wire is pure sequencing: the downstream node runs after the
// upstream succeeds EVEN when no value threaded through the upstream's
// pass-in (the pass pin is a control pin, not data). Regression: this used
// to read as "all incoming edges dormant" and silently skip downstream.
func TestPassEdge_SequencesWithoutValue(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	// src feeds a's data input; a.pass → b.pass with nothing wired into
	// a's own pass-in. b must still run after a.
	g := core.Graph{
		ID: "pass-seq", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "source"},
			{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "b", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "a", ToPort: "pass"},
			{From: "a", FromPort: "pass", To: "b", ToPort: "pass"},
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
	bRec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "b"))
	if err != nil || bRec.Status != core.JobStatusSucceeded {
		t.Errorf("b status = %q (err=%v), want succeeded — pass wire must sequence without a value", bRec.Status, err)
	}
}

// A disabled ROOT (no incoming edges) is skipped at pickup and the graph
// still completes.
func TestDisabled_RootSkips(t *testing.T) {
	t.Parallel()
	h := newSkipHarness(t)

	g := core.Graph{
		ID: "disabled-root", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "off", Module: "source", Disabled: true},
			{ID: "after", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "off", FromPort: "out", To: "after", ToPort: "pass"},
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
	offRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "off"))
	if offRec.Status != core.JobStatusSkipped {
		t.Errorf("off status = %q, want skipped", offRec.Status)
	}
	afterRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "after"))
	if afterRec.Status != core.JobStatusSkipped {
		t.Errorf("after status = %q, want skipped (cascade)", afterRec.Status)
	}
}

// Disabling a node inside a loop body prunes it (and its exclusive
// downstream) from the per-item run; the loop itself still succeeds.
func TestDisabled_LoopBodyNodePruned(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	h := newLoopE2EHarness(t, rec)

	g := core.Graph{
		ID: "disabled-body", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "rows", Module: "rows"},
			{ID: "loop", Module: "for_each"},
			{ID: "send", Module: "sendfx", Disabled: true, Params: map[string]any{
				"line": "Hi ${item.name}",
			}},
		},
		Edges: []core.Edge{
			{From: "rows", FromPort: "out", To: "loop", ToPort: "items"},
			{From: "loop", FromPort: "body", To: "send", ToPort: "in"},
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
	// The disabled body node never ran for any row.
	if got := rec.sorted(); len(got) != 0 {
		t.Errorf("disabled body node ran for rows %v, want none", got)
	}
}
