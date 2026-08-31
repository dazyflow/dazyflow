// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// One submitted run with two executed nodes meters exactly one graph run
// and two node executions for the graph's tenant — wired through the real
// SubmitGraph + worker paths, not the store in isolation.
func TestUsageMetering_CountsRunAndNodeExecutions(t *testing.T) {
	h := newSkipHarness(t)

	g := core.Graph{
		ID: "metered", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "source"},
			{ID: "dst", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "dst", ToPort: "in"},
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

	buckets, err := h.usage.Usage(t.Context(), "t", 1)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("got %d buckets, want 1: %+v", len(buckets), buckets)
	}
	if buckets[0].GraphRuns != 1 || buckets[0].NodeExecutions != 2 {
		t.Errorf("counters = %+v, want 1 run / 2 node executions", buckets[0])
	}
}

// A skipped node never executed, so it must not meter: a disabled node
// (and its downstream cascade) is recorded skipped by the dispatcher
// without a worker attempt — only the source's real execution counts.
func TestUsageMetering_SkippedNodesDoNotCount(t *testing.T) {
	h := newSkipHarness(t)

	g := core.Graph{
		ID: "metered-skip", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "source"},
			{ID: "dst", Module: "delay", Params: map[string]any{"ms": 1}, Disabled: true},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "dst", ToPort: "in"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)

	buckets, err := h.usage.Usage(t.Context(), "t", 1)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(buckets) != 1 || buckets[0].GraphRuns != 1 || buckets[0].NodeExecutions != 1 {
		t.Errorf("counters = %+v, want 1 run / 1 node execution (skip not metered)", buckets)
	}
}
