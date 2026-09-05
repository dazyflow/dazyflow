// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
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
	const capacity = 8
	c := NewRunCache(capacity)
	for i := 0; i < capacity+3; i++ {
		c.put(string(rune('a'+i)), cachedRun{graph: core.Graph{ID: string(rune('a' + i))}})
	}
	c.mu.Lock()
	held := len(c.entries)
	c.mu.Unlock()
	if held > capacity {
		t.Errorf("cache holds %d runs, want at most %d", held, capacity)
	}
	if _, ok := c.get("a"); ok {
		t.Error("the oldest entry was not evicted")
	}
}

// graphFor is what makes a run window of thousands affordable: without it
// every in-flight run would hold its own decode of the same flow.
func TestRunCacheGraphFor_SharesOneDecodePerFlow(t *testing.T) {
	c := NewRunCache(0)
	payload := []byte(`{"id":"g","nodes":[{"id":"a","module":"noop"}],"edges":[]}`)

	first, err := c.graphFor(payload)
	if err != nil {
		t.Fatalf("graphFor: %v", err)
	}
	// A distinct byte slice with the same content is the same flow.
	second, err := c.graphFor(append([]byte(nil), payload...))
	if err != nil {
		t.Fatalf("graphFor again: %v", err)
	}
	if len(first.Nodes) != 1 || first.Nodes[0].ID != "a" {
		t.Fatalf("decoded graph = %+v, want one node \"a\"", first)
	}
	if &first.Nodes[0] != &second.Nodes[0] {
		t.Error("the same payload was decoded twice")
	}

	// Different bytes are a different flow, never a shared decode.
	other, err := c.graphFor([]byte(`{"id":"h","nodes":[{"id":"b","module":"noop"}],"edges":[]}`))
	if err != nil {
		t.Fatalf("graphFor other: %v", err)
	}
	if other.ID != "h" || other.Nodes[0].ID != "b" {
		t.Errorf("second flow decoded as %+v", other)
	}

	c.mu.Lock()
	interned := len(c.graphs)
	c.mu.Unlock()
	if interned != 2 {
		t.Errorf("interned %d graphs, want 2", interned)
	}

	if _, err := c.graphFor([]byte(`{`)); err == nil {
		t.Error("malformed payload decoded without error")
	}
}

func TestRunCacheGraphFor_BoundsTheParseCache(t *testing.T) {
	c := NewRunCache(0)
	for i := 0; i < maxInternedGraphs+5; i++ {
		if _, err := c.graphFor(fmt.Appendf(nil, `{"id":"g%d","nodes":[],"edges":[]}`, i)); err != nil {
			t.Fatalf("graphFor %d: %v", i, err)
		}
	}
	c.mu.Lock()
	held := len(c.graphs)
	c.mu.Unlock()
	if held > maxInternedGraphs {
		t.Errorf("interned %d graphs, want at most %d", held, maxInternedGraphs)
	}
}
