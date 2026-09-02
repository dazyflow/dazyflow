// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestBuildTopology_DedupesDependents(t *testing.T) {
	g := core.Graph{
		Nodes: []core.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []core.Edge{
			// Three wires between the same two steps: one dependent.
			{From: "a", FromPort: "x", To: "b", ToPort: "items"},
			{From: "a", FromPort: "y", To: "b", ToPort: "items"},
			{From: "a", FromPort: "z", To: "b", ToPort: "items"},
			{From: "a", FromPort: "x", To: "c", ToPort: "in"},
		},
	}
	topo := buildTopology(g)
	if got := topo.outgoing["a"]; len(got) != 2 {
		t.Errorf("outgoing[a] = %v, want b and c once each", got)
	}
	if got := topo.incoming["b"]; len(got) != 3 {
		t.Errorf("incoming[b] = %d edges, want all 3 (they carry different ports)", len(got))
	}
}

// countingJobStore records how many point reads a dispatch pass issues.
type countingJobStore struct {
	core.JobStore
	rec  core.JobRecord
	gets int
}

func (c *countingJobStore) Get(context.Context, string) (core.JobRecord, error) {
	c.gets++
	return c.rec, nil
}

// A step fed by many wires from the same predecessor must cost ONE read,
// not one per wire — against Postgres each read is a query, and the old
// path issued one per edge per evaluation.
func TestDispatchIndex_ReadsEachPredecessorOnce(t *testing.T) {
	g := core.Graph{
		Nodes: []core.Node{{ID: "a"}, {ID: "b"}},
		Edges: []core.Edge{
			{From: "a", FromPort: "x", To: "b", ToPort: "items"},
			{From: "a", FromPort: "y", To: "b", ToPort: "items"},
			{From: "a", FromPort: "z", To: "b", ToPort: "items"},
		},
	}
	store := &countingJobStore{rec: core.JobRecord{
		NodeID: "a",
		Status: core.JobStatusSucceeded,
		Result: &core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"x": {Inline: "1"}, "y": {Inline: "2"}, "z": {Inline: "3"},
		}},
	}}
	d := NewDispatcher(store, NewMemoryBus(), nil, nil)
	ix := d.indexFor("run-1", g)

	if decision, _ := d.analyzeDependentIndexed(context.Background(), g, "run-1", "b", ix); decision != depEnqueue {
		t.Errorf("decision = %v, want depEnqueue", decision)
	}
	if store.gets != 1 {
		t.Errorf("%d store reads for 3 wires from one predecessor, want 1", store.gets)
	}
}

// The topology of a run is fixed once submitted, so it is derived once and
// reused for the rest of the run's dispatch passes.
func TestTopologyCache_ReusesPerRun(t *testing.T) {
	g := core.Graph{Nodes: []core.Node{{ID: "a"}}}
	var c topologyCache
	first := c.get("run-1", g)
	if second := c.get("run-1", g); second != first {
		t.Error("topology rebuilt for the same run")
	}
	if other := c.get("run-2", g); other == first {
		t.Error("two runs shared one topology")
	}
	// An empty run id is a caller with nothing to key on: always fresh.
	if a, b := c.get("", g), c.get("", g); a == b {
		t.Error("an unkeyed topology was cached")
	}
}

func TestRunGraphCache_EvictsOldest(t *testing.T) {
	var c runGraphCache
	for i := 0; i < maxCachedTopologies+3; i++ {
		c.put(string(rune('a'+i)), cachedRun{graph: core.Graph{ID: string(rune('a' + i))}})
	}
	c.mu.Lock()
	held := len(c.entries)
	c.mu.Unlock()
	if held > maxCachedTopologies {
		t.Errorf("cache holds %d runs, want at most %d", held, maxCachedTopologies)
	}
	if _, ok := c.get("a"); ok {
		t.Error("the oldest entry was not evicted")
	}
}
