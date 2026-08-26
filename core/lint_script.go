// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "fmt"

// The script-language rule: a step that EXECUTES a script it is handed, wired
// to a step that says what language that script is in, where the two disagree.
//
// A Text step set to Python feeding a Run-on-your-machine step set to run with
// bash is a flow that will fail on someone's machine with a pile of syntax
// errors, and the two nodes each look correct on their own. Nothing at run time
// can tell them apart either: the script arrives as a string, and a string does
// not carry a language.
//
// Which is why this is a LINT and not automatic behaviour. Reading the language
// off the wire and quietly switching the interpreter was the obvious
// alternative, and it would have made the most consequential fact in the
// product — which program runs on a machine you own — depend on a node you
// cannot see from the step. The step keeps saying what it runs; the editor says
// when that contradicts what it was given.
//
// Static, on the graph: the language is a param on the upstream node, so there
// is nothing to carry at run time and no new value on the wire to get wrong.

// scriptRunnerModule is the one step that takes a script as DATA and executes
// it. Named here (like persistenceModules above) because core cannot import the
// drops that define it, and the alternative — a registry the lint consults — is
// a lot of indirection for one module id.
const scriptRunnerModule = "run_on_runner"

// scriptPortName is the input that carries the PROGRAM. Deliberately not "in",
// which carries the program's data: linting that one would flag a Python script
// being fed a YAML document, which is fine and common.
const scriptPortName = "script"

// languageParam is the param an upstream step uses to say what its text is
// written in, and interpreterParam is the one the runner step uses to say what
// it will start. Both are read as plain strings off the graph.
const (
	languageParam    = "language"
	interpreterParam = "shell"
)

// ScriptLanguage classifies a language or interpreter name into what will
// actually run, so the two sides of the wire can be compared.
//
// Exported because the vocabularies live in the DROPS — runner.Shells, the Text
// step's `language` enum — and core cannot import them. Tests in those packages
// assert every value they offer is known here, which is what keeps this from
// silently going stale when a step gains a language.
type ScriptLanguage struct {
	// Family is what actually executes: two names in the same family agree.
	// "node" and "javascript" are the same thing said twice, and sh, bash and
	// "the machine's own shell" are all a shell.
	Family string
	// Runnable is false for a language a machine does not execute at all — SQL,
	// YAML, JSON. Those are data formats; as a PROGRAM they are a mistake.
	Runnable bool
	// Known is false for a word this does not recognise. An unknown name is
	// left alone rather than guessed at: a flow built through the API can carry
	// anything, and a wrong warning is worse than none.
	Known bool
}

// ClassifyScriptLanguage maps a language or interpreter name onto its family.
func ClassifyScriptLanguage(name string) ScriptLanguage {
	switch name {
	// The runner step's own vocabulary. "default" is the machine's own shell.
	case "default", "sh", "bash", "shell", "zsh":
		return ScriptLanguage{Family: "shell", Runnable: true, Known: true}
	case "python":
		return ScriptLanguage{Family: "python", Runnable: true, Known: true}
	case "powershell", "pwsh":
		return ScriptLanguage{Family: "powershell", Runnable: true, Known: true}
	case "node", "javascript", "js":
		return ScriptLanguage{Family: "javascript", Runnable: true, Known: true}
	// Data formats. Known, so a mismatch can be reported precisely, but not
	// something a machine runs.
	case "sql", "yaml", "yml", "json":
		return ScriptLanguage{Family: name, Runnable: false, Known: true}
	// "plain" is a text step saying it is prose — it makes no claim about a
	// language, so there is nothing to contradict.
	case "plain", "":
		return ScriptLanguage{Known: true}
	default:
		return ScriptLanguage{}
	}
}

// lintScriptLanguage flags a script-running step whose interpreter contradicts
// the language of the step feeding it.
func lintScriptLanguage(g Graph, nodesByID map[string]Node) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		if n.Module != scriptRunnerModule {
			continue
		}
		interpreter := stringParam(n.Params, interpreterParam)
		runs := ClassifyScriptLanguage(interpreter)
		if !runs.Known || runs.Family == "" {
			// An interpreter this does not recognise, or none chosen. Either
			// way there is nothing to contradict.
			continue
		}
		for _, e := range g.Edges {
			if e.To != n.ID || e.ToPort != scriptPortName {
				continue
			}
			src, ok := nodesByID[e.From]
			if !ok {
				continue
			}
			claimed := stringParam(src.Params, languageParam)
			says := ClassifyScriptLanguage(claimed)
			// Silent unless the upstream node actually claims a language: most
			// things wired in here (a template, a table cell, the AI step) make
			// no claim at all, and that is not a problem.
			if !says.Known || claimed == "" || claimed == "plain" {
				continue
			}
			if !says.Runnable {
				issues = append(issues, LintIssue{
					Code:     "script_language_unrunnable",
					Severity: LintWarn,
					Message: fmt.Sprintf(
						"Node %q is given a script that says it is %s, which is a data format rather "+
							"than a program — a machine has nothing to run it with. Wire it a script, "+
							"or correct the language on node %q.",
						n.ID, claimed, src.ID),
					NodeIDs: []string{n.ID, src.ID},
					Fields:  []string{interpreterParam},
					Values:  map[string]string{"language": claimed, "interpreter": interpreter},
				})
				continue
			}
			if says.Family != runs.Family {
				issues = append(issues, LintIssue{
					Code:     "script_language_mismatch",
					Severity: LintWarn,
					Message: fmt.Sprintf(
						"Node %q runs its script with %s, but node %q says that script is written in "+
							"%s. The step decides what actually runs, so this will fail on the machine "+
							"— set one of them to match the other.",
						n.ID, interpreter, src.ID, claimed),
					NodeIDs: []string{n.ID, src.ID},
					Fields:  []string{interpreterParam},
					Values:  map[string]string{"language": claimed, "interpreter": interpreter},
				})
			}
		}
	}
	return issues
}

// stringParam reads one param as a string, or "" when it is absent or of
// another type. A graph built through the API can carry anything.
func stringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	s, _ := params[key].(string)
	return s
}
