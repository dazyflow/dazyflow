// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

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
