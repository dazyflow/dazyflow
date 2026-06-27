// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
)

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
