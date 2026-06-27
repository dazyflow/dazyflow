// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"reflect"
	"testing"
)

func TestTopologicalOrder(t *testing.T) {
	tests := []struct {
		name string
		g    Graph
		want []string
	}{
		{
			name: "empty",
			g:    Graph{},
			want: []string{},
		},
		{
			name: "single",
			g: Graph{
				Nodes: []Node{{ID: "a"}},
			},
			want: []string{"a"},
		},
		{
			name: "linear chain",
			g: Graph{
				Nodes: []Node{{ID: "c"}, {ID: "a"}, {ID: "b"}},
				Edges: []Edge{
					{From: "a", To: "b"},
					{From: "b", To: "c"},
				},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "diamond — deterministic tie-break by ID",
			g: Graph{
				Nodes: []Node{{ID: "d"}, {ID: "b"}, {ID: "c"}, {ID: "a"}},
				Edges: []Edge{
					{From: "a", To: "b"},
					{From: "a", To: "c"},
					{From: "b", To: "d"},
					{From: "c", To: "d"},
				},
			},
			want: []string{"a", "b", "c", "d"},
		},
		{
			name: "disconnected components",
			g: Graph{
				Nodes: []Node{{ID: "a"}, {ID: "b"}, {ID: "x"}, {ID: "y"}},
				Edges: []Edge{
					{From: "a", To: "b"},
					{From: "x", To: "y"},
				},
			},
			want: []string{"a", "b", "x", "y"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TopologicalOrder(tt.g)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) == 0 {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTopologicalOrder_Cycles(t *testing.T) {
	tests := []struct {
		name string
		g    Graph
	}{
		{
			name: "two-cycle",
			g: Graph{
				Nodes: []Node{{ID: "a"}, {ID: "b"}},
				Edges: []Edge{
					{From: "a", To: "b"},
					{From: "b", To: "a"},
				},
			},
		},
		{
			name: "self-loop",
			g: Graph{
				Nodes: []Node{{ID: "a"}},
				Edges: []Edge{{From: "a", To: "a"}},
			},
		},
		{
			name: "three-cycle with feeder",
			g: Graph{
				Nodes: []Node{{ID: "x"}, {ID: "a"}, {ID: "b"}, {ID: "c"}},
				Edges: []Edge{
					{From: "x", To: "a"},
					{From: "a", To: "b"},
					{From: "b", To: "c"},
					{From: "c", To: "a"},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TopologicalOrder(tt.g)
			if !errors.Is(err, ErrCycle) {
				t.Fatalf("got %v, want ErrCycle", err)
			}
		})
	}
}

func TestExecutionLayers(t *testing.T) {
	g := Graph{
		Nodes: []Node{{ID: "d"}, {ID: "b"}, {ID: "c"}, {ID: "a"}},
		Edges: []Edge{
			{From: "a", To: "b"},
			{From: "a", To: "c"},
			{From: "b", To: "d"},
			{From: "c", To: "d"},
		},
	}
	got, err := ExecutionLayers(g)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := [][]string{{"a"}, {"b", "c"}, {"d"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestExecutionLayers_Cycle(t *testing.T) {
	g := Graph{
		Nodes: []Node{{ID: "a"}, {ID: "b"}},
		Edges: []Edge{
			{From: "a", To: "b"},
			{From: "b", To: "a"},
		},
	}
	if _, err := ExecutionLayers(g); !errors.Is(err, ErrCycle) {
		t.Fatalf("got %v, want ErrCycle", err)
	}
}
