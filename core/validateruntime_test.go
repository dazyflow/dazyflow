// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// runtimeManifests is the small catalog the run-path rules are checked
// against: a source, a single-value sink, and a variadic one with a max.
func runtimeManifests() map[string]Manifest {
	return map[string]Manifest{
		"src": {
			ID:      "src",
			Outputs: []Port{{Port: "out", MIME: []string{"text/plain"}}},
		},
		"sink": {
			ID:     "sink",
			Inputs: []Port{{Port: "in", MIME: []string{"text/plain"}, Required: true}},
		},
		"merge": {
			ID:     "merge",
			Inputs: []Port{{Port: "items", Variadic: true, Max: intPtr(2)}},
		},
		"dyn": {
			ID:           "dyn",
			DynamicPorts: true,
			Inputs:       []Port{{Port: "in"}},
			Outputs:      []Port{{Port: "out"}},
		},
	}
}

func srcNode(id string) Node  { return Node{ID: id, Module: "src"} }
func sinkNode(id string) Node { return Node{ID: id, Module: "sink"} }

// ValidateRuntime is the gate on the STORE and RUN paths. The rules it
// enforces are the ones the run path cannot honour if broken: a second wire
// into a single-value input silently replaces the first, so it must not be
// storable in the first place.
func TestValidateRuntime(t *testing.T) {
	tests := []struct {
		name    string
		g       Graph
		wantSub string // "" = must be accepted
	}{
		{
			name: "single wire into a single-value input",
			g: Graph{
				Nodes: []Node{srcNode("a"), sinkNode("b")},
				Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
			},
		},
		{
			name: "two wires into a single-value input",
			g: Graph{
				Nodes: []Node{srcNode("a"), srcNode("a2"), sinkNode("b")},
				Edges: []Edge{
					{From: "a", FromPort: "out", To: "b", ToPort: "in"},
					{From: "a2", FromPort: "out", To: "b", ToPort: "in"},
				},
			},
			wantSub: `non-variadic input "in" has 2 connections`,
		},
		{
			name: "duplicate identical wires",
			g: Graph{
				Nodes: []Node{srcNode("a"), sinkNode("b")},
				Edges: []Edge{
					{From: "a", FromPort: "out", To: "b", ToPort: "in"},
					{From: "a", FromPort: "out", To: "b", ToPort: "in"},
				},
			},
			wantSub: "has 2 connections",
		},
		{
			name: "past a variadic input's max",
			g: Graph{
				Nodes: []Node{srcNode("a"), srcNode("b"), srcNode("c"), {ID: "m", Module: "merge"}},
				Edges: []Edge{
					{From: "a", FromPort: "out", To: "m", ToPort: "items"},
					{From: "b", FromPort: "out", To: "m", ToPort: "items"},
					{From: "c", FromPort: "out", To: "m", ToPort: "items"},
				},
			},
			wantSub: "max 2",
		},
		{
			name: "wire into a port that does not exist",
			g: Graph{
				Nodes: []Node{srcNode("a"), sinkNode("b")},
				Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "ghost"}},
			},
			wantSub: `has no input port "ghost"`,
		},
		{
			name: "unknown on_error",
			g: Graph{
				Nodes: []Node{srcNode("a"), sinkNode("b")},
				Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in", OnError: "fallbcak"}},
			},
			wantSub: "unknown on_error",
		},
		{
			// A tenant's runner and MCP drops are absent from the default
			// catalog; "unknown here" is not "invalid".
			name: "module missing from the catalog is tolerated",
			g: Graph{
				Nodes: []Node{{ID: "a", Module: "some-runner-drop"}, sinkNode("b")},
				Edges: []Edge{{From: "a", FromPort: "whatever", To: "b", ToPort: "in"}},
			},
		},
		{
			// The editor nags about this; at run time the step fails on its
			// own with a better message than the validator's.
			name: "unconnected required input is not a run-path error",
			g:    Graph{Nodes: []Node{sinkNode("b")}},
		},
		{
			// subgraph's input_map/output_map name its real ports.
			name: "dynamic-port step wired to a mapped port",
			g: Graph{
				Nodes: []Node{{ID: "call", Module: "dyn"}, sinkNode("b")},
				Edges: []Edge{{From: "call", FromPort: "mapped_result", To: "b", ToPort: "in"}},
			},
		},
		{
			name: "structural rules still apply",
			g: Graph{
				Nodes: []Node{srcNode("a"), sinkNode("b")},
				Edges: []Edge{
					{From: "a", FromPort: "out", To: "b", ToPort: "in"},
					{From: "b", FromPort: "out", To: "a", ToPort: "in"},
				},
			},
			wantSub: "cycle",
		},
	}

	manifests := runtimeManifests()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRuntime(tc.g, manifests)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("ValidateRuntime = %v, want accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateRuntime accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("ValidateRuntime = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

// With no catalog the port rules are impossible; the structural ones still hold.
func TestValidateRuntime_NoCatalogDegradesToStructural(t *testing.T) {
	g := Graph{
		Nodes: []Node{srcNode("a"), sinkNode("b")},
		Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "ghost"}},
	}
	if err := ValidateRuntime(g, nil); err != nil {
		t.Errorf("ValidateRuntime with no catalog = %v, want accepted", err)
	}
	cyclic := Graph{
		Nodes: []Node{srcNode("a")},
		Edges: []Edge{{From: "a", FromPort: "out", To: "a", ToPort: "in"}},
	}
	if err := ValidateRuntime(cyclic, nil); err == nil {
		t.Error("ValidateRuntime with no catalog accepted a self-loop")
	}
}

// The editor keeps the two authoring rules the run path drops.
func TestValidateWithManifests_KeepsAuthoringRules(t *testing.T) {
	manifests := runtimeManifests()
	unconnected := Graph{Nodes: []Node{sinkNode("b")}}
	if err := ValidateWithManifests(unconnected, manifests); err == nil {
		t.Error("editor gate accepted an unconnected required input")
	}
	unknown := Graph{Nodes: []Node{{ID: "a", Module: "some-runner-drop"}}}
	if err := ValidateWithManifests(unknown, manifests); err == nil {
		t.Error("editor gate accepted an unknown module")
	}
}
