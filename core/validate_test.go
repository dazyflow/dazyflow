package core

import (
	"strings"
	"testing"
)

func TestValidate_HappyPath(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			{ID: "a", Module: "delay"},
			{ID: "b", Module: "delay"},
		},
		Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
	}
	if err := Validate(g); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidate_Structural(t *testing.T) {
	tests := []struct {
		name    string
		g       Graph
		wantSub string
	}{
		{
			name: "empty node ID",
			g: Graph{
				Nodes: []Node{{ID: "", Module: "x"}},
			},
			wantSub: "empty ID",
		},
		{
			name: "duplicate node ID",
			g: Graph{
				Nodes: []Node{
					{ID: "a", Module: "x"},
					{ID: "a", Module: "x"},
				},
			},
			wantSub: "duplicate node ID",
		},
		{
			name: "empty module",
			g: Graph{
				Nodes: []Node{{ID: "a", Module: ""}},
			},
			wantSub: "empty module",
		},
		{
			name: "unknown source node",
			g: Graph{
				Nodes: []Node{{ID: "b", Module: "x"}},
				Edges: []Edge{{From: "a", FromPort: "o", To: "b", ToPort: "i"}},
			},
			wantSub: "unknown source node",
		},
		{
			name: "unknown target node",
			g: Graph{
				Nodes: []Node{{ID: "a", Module: "x"}},
				Edges: []Edge{{From: "a", FromPort: "o", To: "b", ToPort: "i"}},
			},
			wantSub: "unknown target node",
		},
		{
			name: "empty from_port",
			g: Graph{
				Nodes: []Node{
					{ID: "a", Module: "x"},
					{ID: "b", Module: "x"},
				},
				Edges: []Edge{{From: "a", FromPort: "", To: "b", ToPort: "i"}},
			},
			wantSub: "empty from_port",
		},
		{
			name: "empty to_port",
			g: Graph{
				Nodes: []Node{
					{ID: "a", Module: "x"},
					{ID: "b", Module: "x"},
				},
				Edges: []Edge{{From: "a", FromPort: "o", To: "b", ToPort: ""}},
			},
			wantSub: "empty to_port",
		},
		{
			name: "self-loop",
			g: Graph{
				Nodes: []Node{{ID: "a", Module: "x"}},
				Edges: []Edge{{From: "a", FromPort: "o", To: "a", ToPort: "i"}},
			},
			wantSub: "self-loop",
		},
		{
			name: "cycle",
			g: Graph{
				Nodes: []Node{
					{ID: "a", Module: "x"},
					{ID: "b", Module: "x"},
				},
				Edges: []Edge{
					{From: "a", FromPort: "o", To: "b", ToPort: "i"},
					{From: "b", FromPort: "o", To: "a", ToPort: "i"},
				},
			},
			wantSub: "cycle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.g)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func intPtr(i int) *int { return &i }

func TestValidateWithManifests(t *testing.T) {
	src := Manifest{
		ID: "src",
		Outputs: []Port{
			{Port: "out", MIME: []string{"text/plain"}},
		},
	}
	sink := Manifest{
		ID: "sink",
		Inputs: []Port{
			{Port: "in", MIME: []string{"text/plain"}, Required: true},
		},
	}
	merge := Manifest{
		ID: "merge",
		Inputs: []Port{
			{Port: "items", MIME: []string{"text/plain"}, Variadic: true, Min: intPtr(2), Max: intPtr(3)},
		},
	}
	wildcard := Manifest{
		ID:      "wildcard",
		Outputs: []Port{{Port: "out"}},
		Inputs:  []Port{{Port: "in"}},
	}

	manifests := map[string]Manifest{
		"src":      src,
		"sink":     sink,
		"merge":    merge,
		"wildcard": wildcard,
	}

	tests := []struct {
		name    string
		g       Graph
		wantSub string // empty means expect no error
	}{
		{
			name: "happy path",
			g: Graph{
				Nodes: []Node{
					{ID: "s", Module: "src"},
					{ID: "k", Module: "sink"},
				},
				Edges: []Edge{{From: "s", FromPort: "out", To: "k", ToPort: "in"}},
			},
		},
		{
			name: "unknown module",
			g: Graph{
				Nodes: []Node{{ID: "x", Module: "nope"}},
			},
			wantSub: "unknown module",
		},
		{
			name: "missing source port",
			g: Graph{
				Nodes: []Node{
					{ID: "s", Module: "src"},
					{ID: "k", Module: "sink"},
				},
				Edges: []Edge{{From: "s", FromPort: "bogus", To: "k", ToPort: "in"}},
			},
			wantSub: "no output port",
		},
		{
			name: "missing target port",
			g: Graph{
				Nodes: []Node{
					{ID: "s", Module: "src"},
					{ID: "k", Module: "sink"},
				},
				Edges: []Edge{{From: "s", FromPort: "out", To: "k", ToPort: "bogus"}},
			},
			wantSub: "no input port",
		},
		{
			name: "required input unconnected",
			g: Graph{
				Nodes: []Node{{ID: "k", Module: "sink"}},
			},
			wantSub: "required input",
		},
		{
			name: "non-variadic fan-in",
			g: Graph{
				Nodes: []Node{
					{ID: "s1", Module: "src"},
					{ID: "s2", Module: "src"},
					{ID: "k", Module: "sink"},
				},
				Edges: []Edge{
					{From: "s1", FromPort: "out", To: "k", ToPort: "in"},
					{From: "s2", FromPort: "out", To: "k", ToPort: "in"},
				},
			},
			wantSub: "non-variadic input",
		},
		{
			name: "variadic min not met",
			g: Graph{
				Nodes: []Node{
					{ID: "s", Module: "src"},
					{ID: "m", Module: "merge"},
				},
				Edges: []Edge{
					{From: "s", FromPort: "out", To: "m", ToPort: "items"},
				},
			},
			wantSub: "min 2",
		},
		{
			name: "variadic max exceeded",
			g: Graph{
				Nodes: []Node{
					{ID: "s1", Module: "src"},
					{ID: "s2", Module: "src"},
					{ID: "s3", Module: "src"},
					{ID: "s4", Module: "src"},
					{ID: "m", Module: "merge"},
				},
				Edges: []Edge{
					{From: "s1", FromPort: "out", To: "m", ToPort: "items"},
					{From: "s2", FromPort: "out", To: "m", ToPort: "items"},
					{From: "s3", FromPort: "out", To: "m", ToPort: "items"},
					{From: "s4", FromPort: "out", To: "m", ToPort: "items"},
				},
			},
			wantSub: "max 3",
		},
		{
			name: "MIME mismatch",
			g: Graph{
				Nodes: []Node{
					{ID: "s", Module: "src"},
					{ID: "k", Module: "bin-sink"},
				},
				Edges: []Edge{{From: "s", FromPort: "out", To: "k", ToPort: "in"}},
			},
			wantSub: "MIME mismatch",
		},
		{
			name: "MIME wildcard input",
			g: Graph{
				Nodes: []Node{
					{ID: "s", Module: "src"},
					{ID: "w", Module: "wildcard"},
				},
				Edges: []Edge{{From: "s", FromPort: "out", To: "w", ToPort: "in"}},
			},
		},
	}

	binSink := Manifest{
		ID: "bin-sink",
		Inputs: []Port{
			{Port: "in", MIME: []string{"application/octet-stream"}, Required: true},
		},
	}
	manifests["bin-sink"] = binSink

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWithManifests(tt.g, manifests)
			if tt.wantSub == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}
