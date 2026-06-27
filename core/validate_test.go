// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

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
			// A required input filled by an inline param (the inline-pin UX,
			// and how a for_each body node draws ${item.…}) is satisfied even
			// without a wire.
			name: "required input satisfied by inline param",
			g: Graph{
				Nodes: []Node{{ID: "k", Module: "sink", Params: map[string]any{"in": "hello"}}},
			},
			wantSub: "",
		},
		{
			name: "required input with empty param still unconnected",
			g: Graph{
				Nodes: []Node{{ID: "k", Module: "sink", Params: map[string]any{"in": ""}}},
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

func issueCodes(issues []LintIssue) map[string]int {
	out := map[string]int{}
	for _, i := range issues {
		out[i.Code]++
	}
	return out
}

func TestValidateGraphFull_NoManifestsRunsLintOnly(t *testing.T) {
	// With no manifests, only LintGraph runs; a clean graph yields no issues.
	g := Graph{Nodes: []Node{{ID: "a", Module: "delay"}}}
	if issues := ValidateGraphFull(g, nil); len(issues) != 0 {
		t.Errorf("clean graph without manifests should yield no issues, got %v", issues)
	}
}

func TestManifestLintIssues_Cov(t *testing.T) {
	manifests := map[string]Manifest{
		"src": {ID: "src", Outputs: []Port{{Port: "out"}}},
		"dst": {ID: "dst", Inputs: []Port{{Port: "in"}}},
	}
	// Edge to a non-existent port → at least one invalid_structure issue.
	bad := Graph{
		Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "dst"}},
		Edges: []Edge{{From: "a", FromPort: "nope", To: "b", ToPort: "in"}},
	}
	issues := ManifestLintIssues(bad, manifests)
	if issueCodes(issues)["invalid_structure"] == 0 {
		t.Errorf("expected invalid_structure issue, got %v", issues)
	}
	// Clean graph → no issues.
	good := Graph{
		Nodes: []Node{{ID: "a", Module: "src"}, {ID: "b", Module: "dst"}},
		Edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
	}
	if issues := ManifestLintIssues(good, manifests); len(issues) != 0 {
		t.Errorf("clean graph should yield no manifest issues, got %v", issues)
	}
	// No manifests → nil.
	if issues := ManifestLintIssues(good, nil); issues != nil {
		t.Errorf("no manifests should yield nil, got %v", issues)
	}
}

func TestStructuredIntoTextWarnings_Cov(t *testing.T) {
	manifests := map[string]Manifest{
		"lister":  {ID: "lister", Outputs: []Port{{Port: "rows", List: true}}},
		"emailer": {ID: "emailer", Inputs: []Port{{Port: "body", MIME: []string{"text/plain"}}}},
	}
	g := Graph{
		Nodes: []Node{{ID: "a", Module: "lister"}, {ID: "b", Module: "emailer"}},
		Edges: []Edge{{From: "a", FromPort: "rows", To: "b", ToPort: "body"}},
	}
	issues := structuredIntoTextWarnings(g, manifests)
	if issueCodes(issues)["structured_into_text"] != 1 {
		t.Errorf("expected one structured_into_text warning, got %v", issues)
	}
	// No manifests → nil.
	if structuredIntoTextWarnings(g, nil) != nil {
		t.Error("no manifests should yield nil")
	}
	// JSON output into text input also flags.
	manifests2 := map[string]Manifest{
		"jsoner":  {ID: "jsoner", Outputs: []Port{{Port: "data", MIME: []string{"application/json"}}}},
		"emailer": manifests["emailer"],
	}
	g2 := Graph{
		Nodes: []Node{{ID: "a", Module: "jsoner"}, {ID: "b", Module: "emailer"}},
		Edges: []Edge{{From: "a", FromPort: "data", To: "b", ToPort: "body"}},
	}
	if issueCodes(structuredIntoTextWarnings(g2, manifests2))["structured_into_text"] != 1 {
		t.Error("json into text should warn")
	}
	// Trigger (non-text) output into text input flags too.
	manifests3 := map[string]Manifest{
		"trig":    {ID: "trig", ExecutionModel: ExecutionTrigger, Outputs: []Port{{Port: "body"}}},
		"emailer": manifests["emailer"],
	}
	g3 := Graph{
		Nodes: []Node{{ID: "a", Module: "trig"}, {ID: "b", Module: "emailer"}},
		Edges: []Edge{{From: "a", FromPort: "body", To: "b", ToPort: "body"}},
	}
	if issueCodes(structuredIntoTextWarnings(g3, manifests3))["structured_into_text"] != 1 {
		t.Error("trigger body into text should warn")
	}
}

func TestCardinalityMismatchWarnings_Cov(t *testing.T) {
	manifests := map[string]Manifest{
		"lister": {ID: "lister", Outputs: []Port{{Port: "rows", List: true, MIME: []string{"application/json"}}}},
		"single": {ID: "single", Inputs: []Port{{Port: "item", MIME: []string{"application/json"}}}},
	}
	g := Graph{
		Nodes: []Node{{ID: "a", Module: "lister"}, {ID: "b", Module: "single"}},
		Edges: []Edge{{From: "a", FromPort: "rows", To: "b", ToPort: "item"}},
	}
	if issueCodes(cardinalityMismatchWarnings(g, manifests))["many_into_one"] != 1 {
		t.Error("many into one should warn")
	}
	// Variadic input is skipped.
	manifests2 := map[string]Manifest{
		"lister":   manifests["lister"],
		"varinput": {ID: "varinput", Inputs: []Port{{Port: "item", Variadic: true, MIME: []string{"application/json"}}}},
	}
	g2 := Graph{
		Nodes: []Node{{ID: "a", Module: "lister"}, {ID: "b", Module: "varinput"}},
		Edges: []Edge{{From: "a", FromPort: "rows", To: "b", ToPort: "item"}},
	}
	if len(cardinalityMismatchWarnings(g2, manifests2)) != 0 {
		t.Error("variadic input should not warn on many-into-one")
	}
	// KindAny input is skipped (wildcard MIME).
	manifests3 := map[string]Manifest{
		"lister": manifests["lister"],
		"anyin":  {ID: "anyin", Inputs: []Port{{Port: "item"}}},
	}
	g3 := Graph{
		Nodes: []Node{{ID: "a", Module: "lister"}, {ID: "b", Module: "anyin"}},
		Edges: []Edge{{From: "a", FromPort: "rows", To: "b", ToPort: "item"}},
	}
	if len(cardinalityMismatchWarnings(g3, manifests3)) != 0 {
		t.Error("KindAny input should not warn on many-into-one")
	}
	// No manifests → nil.
	if cardinalityMismatchWarnings(g, nil) != nil {
		t.Error("no manifests should yield nil")
	}
}

func TestMimeContains(t *testing.T) {
	if !mimeContains([]string{"a", "application/json"}, "application/json") {
		t.Error("should find present mime")
	}
	if mimeContains([]string{"a"}, "b") {
		t.Error("should not find absent mime")
	}
	if mimeContains(nil, "x") {
		t.Error("nil set contains nothing")
	}
}

func TestMimeIsTextOnly(t *testing.T) {
	if !mimeIsTextOnly([]string{"text/plain"}) {
		t.Error("single text/plain is text-only")
	}
	if mimeIsTextOnly([]string{"text/plain", "application/json"}) {
		t.Error("mixed set is not text-only")
	}
	if mimeIsTextOnly(nil) {
		t.Error("empty set is a wildcard, not text-only")
	}
}

func TestHasInlineParamValue(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		port   string
		want   bool
	}{
		{"nil map", nil, "x", false},
		{"missing key", map[string]any{}, "x", false},
		{"nil value", map[string]any{"x": nil}, "x", false},
		{"empty string", map[string]any{"x": ""}, "x", false},
		{"non-empty string", map[string]any{"x": "v"}, "x", true},
		{"template string", map[string]any{"x": "${a.b}"}, "x", true},
		{"non-string value", map[string]any{"x": 42}, "x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasInlineParamValue(tt.params, tt.port); got != tt.want {
				t.Errorf("hasInlineParamValue = %v, want %v", got, tt.want)
			}
		})
	}
}
