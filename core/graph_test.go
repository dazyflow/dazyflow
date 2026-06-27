// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"sort"
	"testing"
)

// graphFixture builds a small DAG for upstream-subset tests.
//
//	A   B          (sources)
//	 \ / \
//	  C   D        (B → both C and D)
//	  |   |
//	  E   F
//	   \ /
//	    G          (sink)
func graphFixture() Graph {
	return Graph{
		ID: "g", Tenant: "t", Workspace: "w",
		Nodes: []Node{
			{ID: "A"}, {ID: "B"}, {ID: "C"},
			{ID: "D"}, {ID: "E"}, {ID: "F"}, {ID: "G"},
		},
		Edges: []Edge{
			{From: "A", To: "C"},
			{From: "B", To: "C"},
			{From: "B", To: "D"},
			{From: "C", To: "E"},
			{From: "D", To: "F"},
			{From: "E", To: "G"},
			{From: "F", To: "G"},
		},
	}
}

// nodeIDs sorts node IDs for stable assertion against a set.
func nodeIDs(g Graph) []string {
	ids := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[i] = n.ID
	}
	sort.Strings(ids)
	return ids
}

func edgeKeys(g Graph) []string {
	keys := make([]string, len(g.Edges))
	for i, e := range g.Edges {
		keys[i] = e.From + "->" + e.To
	}
	sort.Strings(keys)
	return keys
}

func TestUpstreamSubset_LeafIncludesAllAncestors(t *testing.T) {
	sub, ok := graphFixture().UpstreamSubset("G")
	if !ok {
		t.Fatal("target G should be found")
	}
	want := []string{"A", "B", "C", "D", "E", "F", "G"}
	if got := nodeIDs(sub); !equalStrings(got, want) {
		t.Fatalf("nodes=%v want=%v", got, want)
	}
	// Every original edge is between included nodes — all should survive.
	if got, n := len(sub.Edges), 7; got != n {
		t.Fatalf("edges=%d want=%d (%v)", got, n, edgeKeys(sub))
	}
}

func TestUpstreamSubset_MidNodeDropsParallelBranch(t *testing.T) {
	// Sampling E should bring A, B, C, E (but not D, F, or G).
	sub, ok := graphFixture().UpstreamSubset("E")
	if !ok {
		t.Fatal("not found")
	}
	want := []string{"A", "B", "C", "E"}
	if got := nodeIDs(sub); !equalStrings(got, want) {
		t.Fatalf("nodes=%v want=%v", got, want)
	}
	// Edges between included nodes only.
	wantEdges := []string{"A->C", "B->C", "C->E"}
	if got := edgeKeys(sub); !equalStrings(got, wantEdges) {
		t.Fatalf("edges=%v want=%v", got, wantEdges)
	}
}

func TestUpstreamSubset_SourceNodeIsSingleton(t *testing.T) {
	sub, ok := graphFixture().UpstreamSubset("A")
	if !ok {
		t.Fatal("not found")
	}
	if got := nodeIDs(sub); !equalStrings(got, []string{"A"}) {
		t.Fatalf("nodes=%v want [A]", got)
	}
	if len(sub.Edges) != 0 {
		t.Fatalf("expected no edges, got %v", edgeKeys(sub))
	}
}

func TestUpstreamSubset_PreservesEdgeMetadata(t *testing.T) {
	// On_error semantics must survive the filter — sampling a fallback
	// edge's destination should behave the same as a full-graph run.
	g := Graph{
		ID:    "x",
		Nodes: []Node{{ID: "A"}, {ID: "B"}, {ID: "C"}},
		Edges: []Edge{
			{From: "A", To: "B", FromPort: "out", ToPort: "in", OnError: OnErrorFallback},
			{From: "A", To: "C", OnError: OnErrorSkip},
		},
	}
	sub, _ := g.UpstreamSubset("B")
	if len(sub.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(sub.Edges))
	}
	e := sub.Edges[0]
	if e.From != "A" || e.To != "B" || e.FromPort != "out" || e.ToPort != "in" || e.OnError != OnErrorFallback {
		t.Fatalf("edge metadata lost: %+v", e)
	}
}

func TestUpstreamSubset_MissingTargetReturnsFalse(t *testing.T) {
	_, ok := graphFixture().UpstreamSubset("ghost")
	if ok {
		t.Fatal("expected ok=false for missing node")
	}
}

func TestUpstreamSubset_CarriesGraphIdentity(t *testing.T) {
	// The submitted run needs to share tenant/workspace/visibility so
	// authz + run records line up. Verifying the shallow-copy fields.
	g := Graph{
		ID:         "promo",
		Tenant:     "acme",
		Workspace:  "ops",
		Owner:      "alice",
		Visibility: VisibilityPrivate,
		Nodes:      []Node{{ID: "n"}},
	}
	sub, _ := g.UpstreamSubset("n")
	if sub.ID != "promo" || sub.Tenant != "acme" || sub.Workspace != "ops" {
		t.Fatalf("identity not preserved: %+v", sub)
	}
	if sub.Owner != "alice" || sub.Visibility != VisibilityPrivate {
		t.Fatalf("authz fields not preserved: owner=%q vis=%q", sub.Owner, sub.Visibility)
	}
}

func TestUpstreamSubset_CyclicEdgesDontHang(t *testing.T) {
	// Cyclic graphs fail Validate(), but the subset helper itself
	// must not infinite-loop if it ever sees one (defensive — the
	// helper is called before submission, and a cycle should still
	// terminate so the caller can return a clear error downstream).
	g := Graph{
		Nodes: []Node{{ID: "A"}, {ID: "B"}},
		Edges: []Edge{{From: "A", To: "B"}, {From: "B", To: "A"}},
	}
	sub, ok := g.UpstreamSubset("A")
	if !ok {
		t.Fatal("not found")
	}
	if got := nodeIDs(sub); !equalStrings(got, []string{"A", "B"}) {
		t.Fatalf("nodes=%v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMigrateGraph_DropsLegacyHeaderEdges(t *testing.T) {
	g := Graph{Edges: []Edge{
		{From: "a", FromPort: "out", To: "b", ToPort: "in"},           // kept
		{From: "a", FromPort: "headers", To: "b", ToPort: "in"},       // dropped (from)
		{From: "a", FromPort: "out", To: "b", ToPort: "left_headers"}, // dropped (to)
		{From: "c", FromPort: "right_headers", To: "d", ToPort: "x"},  // dropped (from)
	}}
	got := MigrateGraph(g)
	if len(got.Edges) != 1 {
		t.Fatalf("expected 1 edge kept, got %d: %v", len(got.Edges), got.Edges)
	}
	if got.Edges[0].FromPort != "out" || got.Edges[0].ToPort != "in" {
		t.Errorf("wrong edge survived: %+v", got.Edges[0])
	}
}

func TestMigrateGraph_NoEdgesIsNoop(t *testing.T) {
	g := Graph{ID: "g"}
	if got := MigrateGraph(g); got.ID != "g" || len(got.Edges) != 0 {
		t.Errorf("empty-edge migrate changed graph: %+v", got)
	}
}

func TestMigrateGraph_Idempotent(t *testing.T) {
	g := Graph{Edges: []Edge{
		{From: "a", FromPort: "out", To: "b", ToPort: "in"},
		{From: "a", FromPort: "headers", To: "b", ToPort: "in"},
	}}
	once := MigrateGraph(g)
	twice := MigrateGraph(once)
	if len(twice.Edges) != len(once.Edges) {
		t.Errorf("MigrateGraph not idempotent: %d vs %d", len(twice.Edges), len(once.Edges))
	}
}

func TestEffectiveVisibility(t *testing.T) {
	tests := []struct {
		in   Visibility
		want Visibility
	}{
		{VisibilityPrivate, VisibilityPrivate},
		{VisibilityOrg, VisibilityOrg},
		{"", VisibilityOrg},
		{"unknown", VisibilityOrg},
	}
	for _, tt := range tests {
		if got := (Graph{Visibility: tt.in}).EffectiveVisibility(); got != tt.want {
			t.Errorf("EffectiveVisibility(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
