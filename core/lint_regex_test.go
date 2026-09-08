// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

func regexNode(params map[string]any) Graph {
	return Graph{ID: "g", Nodes: []Node{{ID: "re_1", Module: "regex", Params: params}}}
}

func hasCode(issues []LintIssue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestLintRegexPattern_FlagsAStepWithNothingToSearchFor(t *testing.T) {
	for _, params := range []map[string]any{
		{},
		{"mode": "match"},
		{"pattern": "   "},
		{"mode": "extract", "replacements": map[string]any{"Clouds": "Molnigt"}},
		{"mode": "replace", "replacements": map[string]any{"": "Molnigt"}},
	} {
		if !hasCode(LintGraph(regexNode(params)), "regex_no_pattern") {
			t.Errorf("params %+v should be flagged", params)
		}
	}
}

func TestLintRegexPattern_QuietWhenConfigured(t *testing.T) {
	for _, params := range []map[string]any{
		{"pattern": "[0-9]+"},
		{"pattern": "[0-9]+", "mode": "replace", "replacement": "-"},
		{"mode": "replace", "replacements": map[string]any{"Clouds": "Molnigt"}},
	} {
		if hasCode(LintGraph(regexNode(params)), "regex_no_pattern") {
			t.Errorf("params %+v should not be flagged", params)
		}
	}
}

func TestLintRegexPattern_NamesTheStepAndTheField(t *testing.T) {
	issues := LintGraph(regexNode(map[string]any{"mode": "replace"}))
	var got LintIssue
	for _, i := range issues {
		if i.Code == "regex_no_pattern" {
			got = i
		}
	}
	if len(got.NodeIDs) != 1 || got.NodeIDs[0] != "re_1" {
		t.Errorf("NodeIDs = %v, want [re_1]", got.NodeIDs)
	}
	if len(got.Fields) != 1 || got.Fields[0] != "pattern" {
		t.Errorf("Fields = %v, want [pattern]", got.Fields)
	}
	if got.Severity != LintError {
		t.Errorf("Severity = %v, want error", got.Severity)
	}
}

func TestLintRegexPattern_IgnoresOtherModules(t *testing.T) {
	g := Graph{ID: "g", Nodes: []Node{{ID: "n", Module: "delay"}}}
	if hasCode(LintGraph(g), "regex_no_pattern") {
		t.Error("a non-regex step must not be flagged")
	}
}
