// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"log"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

func succeededWith(ports ...string) core.JobRecord {
	out := map[string]core.Ref{}
	for _, p := range ports {
		out[p] = core.Ref{Inline: "x"}
	}
	return core.JobRecord{Status: core.JobStatusSucceeded, Result: &core.Result{Output: out}}
}

var outcomeNames = map[edgeOutcome]string{
	edgeActive:    "active",
	edgeDormant:   "dormant",
	edgeNotRouted: "not-routed",
	edgeBlocking:  "blocking",
}

func TestClassifyEdge(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		pred core.JobRecord
		edge core.Edge
		want edgeOutcome
	}{
		{"succeeded, output present → active",
			succeededWith("out"), core.Edge{FromPort: "out"}, edgeActive},
		{"succeeded, FromPort had no output → not routed",
			succeededWith("other"), core.Edge{FromPort: "out"}, edgeNotRouted},
		{"succeeded, nil result → not routed",
			core.JobRecord{Status: core.JobStatusSucceeded}, core.Edge{FromPort: "out"}, edgeNotRouted},
		{"succeeded, fallback edge → dormant (primary lived)",
			succeededWith("out"), core.Edge{FromPort: "out", OnError: core.OnErrorFallback}, edgeDormant},
		{"succeeded, pass control pin → active even with no data",
			core.JobRecord{Status: core.JobStatusSucceeded}, core.Edge{FromPort: core.PassPort}, edgeActive},

		{"failed, skip edge → active",
			core.JobRecord{Status: core.JobStatusFailed}, core.Edge{FromPort: "out", OnError: core.OnErrorSkip}, edgeActive},
		{"failed, fallback edge → active",
			core.JobRecord{Status: core.JobStatusFailed}, core.Edge{FromPort: "out", OnError: core.OnErrorFallback}, edgeActive},
		{"failed, abort (default) → blocking",
			core.JobRecord{Status: core.JobStatusFailed}, core.Edge{FromPort: "out", OnError: core.OnErrorAbort}, edgeBlocking},
		{"failed, retry → blocking",
			core.JobRecord{Status: core.JobStatusFailed}, core.Edge{FromPort: "out", OnError: core.OnErrorRetry}, edgeBlocking},

		{"skipped, skip edge → active (skip cascades)",
			core.JobRecord{Status: core.JobStatusSkipped}, core.Edge{FromPort: "out", OnError: core.OnErrorSkip}, edgeActive},
		{"skipped, fallback edge → dormant",
			core.JobRecord{Status: core.JobStatusSkipped}, core.Edge{FromPort: "out", OnError: core.OnErrorFallback}, edgeDormant},
		{"skipped, abort → blocking",
			core.JobRecord{Status: core.JobStatusSkipped}, core.Edge{FromPort: "out", OnError: core.OnErrorAbort}, edgeBlocking},

		{"cancelled → blocking",
			core.JobRecord{Status: core.JobStatusCancelled}, core.Edge{FromPort: "out", OnError: core.OnErrorSkip}, edgeBlocking},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyEdge(c.pred, c.edge); got != c.want {
				t.Errorf("classifyEdge = %s, want %s", outcomeNames[got], outcomeNames[c.want])
			}
		})
	}
}

var decisionNames = map[dependentDecision]string{
	depWaiting: "waiting",
	depEnqueue: "enqueue",
	depSkipped: "skipped",
}

func TestAnalyzeDependent(t *testing.T) {
	t.Parallel()
	const runID = "run1"
	mkGraph := func(edges ...core.Edge) core.Graph {
		return core.Graph{ID: "g", Nodes: []core.Node{{ID: "A"}, {ID: "B"}, {ID: "D"}}, Edges: edges}
	}
	seed := func(store core.JobStore, nodeID string, status core.JobStatus, ports ...string) {
		out := map[string]core.Ref{}
		for _, p := range ports {
			out[p] = core.Ref{Inline: "x"}
		}
		_ = store.Enqueue(context.Background(), core.JobRecord{
			ID: NodeJobID(runID, nodeID), GraphRunID: runID, NodeID: nodeID, Status: status,
			Result: &core.Result{Output: out},
		})
	}

	cases := []struct {
		name       string
		edges      []core.Edge
		seed       func(core.JobStore)
		wantDecide dependentDecision
	}{
		{
			name:       "predecessor not recorded → waiting",
			edges:      []core.Edge{{From: "A", To: "D", FromPort: "out"}},
			seed:       func(core.JobStore) {},
			wantDecide: depWaiting,
		},
		{
			name:  "predecessor still running → waiting",
			edges: []core.Edge{{From: "A", To: "D", FromPort: "out"}},
			seed: func(s core.JobStore) {
				_ = s.Enqueue(context.Background(), core.JobRecord{
					ID: NodeJobID(runID, "A"), GraphRunID: runID, NodeID: "A", Status: core.JobStatusRunning,
				})
			},
			wantDecide: depWaiting,
		},
		{
			name:  "one active edge → enqueue",
			edges: []core.Edge{{From: "A", To: "D", FromPort: "out"}},
			seed: func(s core.JobStore) {
				seed(s, "A", core.JobStatusSucceeded, "out")
			},
			wantDecide: depEnqueue,
		},
		{
			name:  "sole not-routed edge → skipped",
			edges: []core.Edge{{From: "A", To: "D", FromPort: "out"}},
			seed: func(s core.JobStore) {
				seed(s, "A", core.JobStatusSucceeded) // succeeded but FromPort had no output
			},
			wantDecide: depSkipped,
		},
		{
			name:  "all dormant → skipped",
			edges: []core.Edge{{From: "A", To: "D", FromPort: "out", OnError: core.OnErrorFallback}},
			seed: func(s core.JobStore) {
				seed(s, "A", core.JobStatusSucceeded, "out") // primary lived → fallback dormant
			},
			wantDecide: depSkipped,
		},
		{
			// The double-send bug: an If sends the payload down `then`, and
			// the else-side step ALSO takes an address from a value node that
			// hangs off to the side. The live address wire must not run the
			// branch the If declined, or both branches send.
			name: "not routed beats an active sibling → skipped",
			edges: []core.Edge{
				{From: "A", To: "D", FromPort: "else"}, // the If's untaken port
				{From: "B", To: "D", FromPort: "out"},  // the address value node
			},
			seed: func(s core.JobStore) {
				seed(s, "A", core.JobStatusSucceeded, "then") // routed the other way
				seed(s, "B", core.JobStatusSucceeded, "out")  // active
			},
			wantDecide: depSkipped,
		},
		{
			name: "taken branch still enqueues alongside a value wire",
			edges: []core.Edge{
				{From: "A", To: "D", FromPort: "then"},
				{From: "B", To: "D", FromPort: "out"},
			},
			seed: func(s core.JobStore) {
				seed(s, "A", core.JobStatusSucceeded, "then")
				seed(s, "B", core.JobStatusSucceeded, "out")
			},
			wantDecide: depEnqueue,
		},
		{
			name: "one blocking edge → skipped (even with an active sibling)",
			edges: []core.Edge{
				{From: "A", To: "D", FromPort: "out"},
				{From: "B", To: "D", FromPort: "out", OnError: core.OnErrorAbort},
			},
			seed: func(s core.JobStore) {
				seed(s, "A", core.JobStatusSucceeded, "out") // active
				seed(s, "B", core.JobStatusFailed)           // abort → blocking
			},
			wantDecide: depSkipped,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := jobstore.NewMemory()
			c.seed(store)
			d := NewDispatcher(store, NewMemoryBus(), &engine.Engine{}, log.New(log.Writer(), "", 0))
			got, _ := d.analyzeDependent(context.Background(), mkGraph(c.edges...), runID, "D")
			if got != c.wantDecide {
				t.Errorf("analyzeDependent = %s, want %s",
					decisionNames[got], decisionNames[c.wantDecide])
			}
		})
	}
}

func TestFailurePropagates_ContinueOnError(t *testing.T) {
	t.Parallel()
	d := &Dispatcher{}
	graph := core.Graph{
		Nodes: []core.Node{
			{ID: "src", Module: "webhook_input"},
			{ID: "slack", Module: "slack_send_message"},
			{ID: "discord", Module: "discord_send_message", ContinueOnError: true},
			{ID: "middle", Module: "render_text", ContinueOnError: true},
			{ID: "after", Module: "slack_send_message"},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "body", To: "slack", ToPort: "text"},
			{From: "src", FromPort: "body", To: "discord", ToPort: "content"},
			{From: "src", FromPort: "body", To: "middle", ToPort: "rows"},
			{From: "middle", FromPort: "text", To: "after", ToPort: "text"},
		},
	}
	if !d.failurePropagates(graph, "slack") {
		t.Error("an ordinary terminal step failing must still fail the run")
	}
	if d.failurePropagates(graph, "discord") {
		t.Error("a terminal step marked non-critical must not fail the run")
	}
	if d.failurePropagates(graph, "middle") {
		t.Error("a non-critical step with dependents must not fail the run either")
	}
	// An unknown node keeps the old behaviour rather than silently tolerating.
	if !d.failurePropagates(graph, "nope") {
		t.Error("an unknown node should propagate, as before")
	}
}

func TestClassifyEdge_AwaitingPublishesWhatItHas(t *testing.T) {
	t.Parallel()
	parked := core.JobRecord{
		Status: core.JobStatusAwaiting,
		Result: &core.Result{Output: map[string]core.Ref{
			"pending_url": {Inline: "https://host/approve/run/node?sig=x"},
		}},
	}
	if got := classifyEdge(parked, core.Edge{FromPort: "pending_url"}); got != edgeActive {
		t.Errorf("the approval link should reach its notifier while parked, got %v", got)
	}
	for _, port := range []string{"approved", "rejected"} {
		if got := classifyEdge(parked, core.Edge{FromPort: port}); got != edgeBlocking {
			t.Errorf("%q must not fire before the decision, got %v", port, got)
		}
	}
	if got := classifyEdge(parked, core.Edge{FromPort: core.PassPort}); got != edgeBlocking {
		t.Errorf("the pass pin should wait for the step to finish, got %v", got)
	}
}
