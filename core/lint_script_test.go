// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// A Text step set to Python feeding a runner step set to bash is a flow that
// fails on someone's machine with a pile of syntax errors, and each node looks
// correct on its own. Nothing at run time can tell them apart — a script
// arrives as a string, and a string carries no language — so the contradiction
// has to be caught while it is still being written.

// scriptGraph wires a text node into a runner step's script port.
func scriptGraph(language, interpreter string) Graph {
	return Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Nodes: []Node{
			{ID: "src", Module: "text", Params: map[string]any{
				"text": "print(1)", "language": language,
			}},
			{ID: "run", Module: "run_on_runner", Params: map[string]any{
				"tags": []any{"box"}, "shell": interpreter,
			}},
		},
		Edges: []Edge{{From: "src", FromPort: "out", To: "run", ToPort: "script"}},
	}
}

func scriptIssues(t *testing.T, g Graph) []LintIssue {
	t.Helper()
	var out []LintIssue
	for _, i := range LintGraph(g) {
		if strings.HasPrefix(i.Code, "script_language") {
			out = append(out, i)
		}
	}
	return out
}

func TestLintScriptLanguage_FlagsAContradiction(t *testing.T) {
	got := scriptIssues(t, scriptGraph("python", "bash"))
	if len(got) != 1 || got[0].Code != "script_language_mismatch" {
		t.Fatalf("issues = %+v, want one mismatch", got)
	}
	// Both nodes are named so the editor can highlight the pair; the fixable
	// param is the runner's own, since the step is what decides what runs.
	if len(got[0].NodeIDs) != 2 || got[0].NodeIDs[0] != "run" {
		t.Errorf("node_ids = %v, want the runner first then its source", got[0].NodeIDs)
	}
	if strings.Join(got[0].Fields, ",") != "shell" {
		t.Errorf("fields = %v, want the interpreter param", got[0].Fields)
	}
	// The two names travel as data so the UI can build a localised sentence
	// rather than falling back to the English message.
	if got[0].Values["language"] != "python" || got[0].Values["interpreter"] != "bash" {
		t.Errorf("values = %v, want both names", got[0].Values)
	}
}

func TestLintScriptLanguage_StaysQuietWhenTheyAgree(t *testing.T) {
	for _, tc := range []struct{ language, interpreter string }{
		{"python", "python"},
		// The same thing said twice: the runner calls it "node", the text step
		// calls it "javascript".
		{"javascript", "node"},
		// sh, bash and "the machine's own shell" are all a shell.
		{"shell", "bash"},
		{"shell", "sh"},
		{"shell", "default"},
		{"powershell", "powershell"},
	} {
		t.Run(tc.language+"/"+tc.interpreter, func(t *testing.T) {
			if got := scriptIssues(t, scriptGraph(tc.language, tc.interpreter)); len(got) != 0 {
				t.Errorf("issues = %+v, want none", got)
			}
		})
	}
}

func TestLintScriptLanguage_SaysWhenTheScriptIsNotAProgram(t *testing.T) {
	// A distinct finding, because the fix is different: no interpreter choice
	// makes SQL runnable, so the advice is not "pick another one".
	got := scriptIssues(t, scriptGraph("sql", "bash"))
	if len(got) != 1 || got[0].Code != "script_language_unrunnable" {
		t.Fatalf("issues = %+v, want one unrunnable finding", got)
	}
	if !strings.Contains(got[0].Message, "data format") {
		t.Errorf("message = %q, want it to say why nothing can run it", got[0].Message)
	}
}

func TestLintScriptLanguage_SaysNothingWithoutAClaim(t *testing.T) {
	// Most of what gets wired into that port makes no claim at all — a
	// template, a table cell, the AI step — and that is not a problem.
	for _, language := range []string{"", "plain"} {
		if got := scriptIssues(t, scriptGraph(language, "python")); len(got) != 0 {
			t.Errorf("language %q produced %+v, want silence", language, got)
		}
	}
	// A source node with no language param at all.
	g := scriptGraph("python", "bash")
	g.Nodes[0].Module = "render_template"
	g.Nodes[0].Params = map[string]any{"template": "x"}
	if got := scriptIssues(t, g); len(got) != 0 {
		t.Errorf("a node making no claim produced %+v", got)
	}
}

// An unknown word on either side is left alone. A flow built through the API can
// carry anything, and a wrong warning is worse than none.
func TestLintScriptLanguage_DoesNotGuess(t *testing.T) {
	if got := scriptIssues(t, scriptGraph("klingon", "bash")); len(got) != 0 {
		t.Errorf("unknown language produced %+v", got)
	}
	if got := scriptIssues(t, scriptGraph("python", "klingon")); len(got) != 0 {
		t.Errorf("unknown interpreter produced %+v", got)
	}
}

// The DATA port is not the program port. A Python script being fed a YAML
// document is ordinary, and flagging it would make the rule noise.
func TestLintScriptLanguage_IgnoresTheDataPort(t *testing.T) {
	g := scriptGraph("yaml", "python")
	g.Edges[0].ToPort = "in"
	if got := scriptIssues(t, g); len(got) != 0 {
		t.Errorf("the data port produced %+v", got)
	}
}

func TestClassifyScriptLanguage_KnowsWhatItKnows(t *testing.T) {
	// "plain" is a claim of prose, which contradicts nothing — known, but with
	// no family, so the rule skips it rather than treating it as a language.
	if c := ClassifyScriptLanguage("plain"); !c.Known || c.Family != "" {
		t.Errorf("plain = %+v, want known with no family", c)
	}
	if c := ClassifyScriptLanguage("sql"); !c.Known || c.Runnable {
		t.Errorf("sql = %+v, want known and not runnable", c)
	}
	if c := ClassifyScriptLanguage("nonsense"); c.Known {
		t.Errorf("nonsense = %+v, want unknown", c)
	}
}
