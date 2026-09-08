// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "fmt"

// A step that EXECUTES a script it is handed, wired to a step declaring a
// language the runner will not run. Run time cannot tell either: a script arrives
// as a string, and a string carries no language.
//
// A LINT rather than automatic behaviour: reading the language off the wire and
// switching the interpreter would make the most consequential fact in the product
// — which program runs on a machine you own — depend on a node you cannot see from
// the step.

const scriptRunnerModule = "run_on_runner"

// The PROGRAM port, deliberately not "in", which carries its data: linting that
// would flag a Python script fed a YAML document, which is fine and common.
const scriptPortName = "script"

const (
	languageParam    = "language"
	interpreterParam = "shell"
)

// Exported because the vocabularies live in the DROPS and core cannot import
// them; tests there assert every value they offer is known here.
type ScriptLanguage struct {
	Family   string
	Runnable bool
	Known    bool
}

func ClassifyScriptLanguage(name string) ScriptLanguage {
	switch name {
	case "default", "sh", "bash", "shell", "zsh":
		return ScriptLanguage{Family: "shell", Runnable: true, Known: true}
	case "python":
		return ScriptLanguage{Family: "python", Runnable: true, Known: true}
	case "powershell", "pwsh":
		return ScriptLanguage{Family: "powershell", Runnable: true, Known: true}
	case "node", "javascript", "js":
		return ScriptLanguage{Family: "javascript", Runnable: true, Known: true}
	case "sql", "yaml", "yml", "json":
		return ScriptLanguage{Family: name, Runnable: false, Known: true}
	case "plain", "":
		return ScriptLanguage{Known: true}
	default:
		return ScriptLanguage{}
	}
}

func lintScriptLanguage(g Graph, nodesByID map[string]Node) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		if n.Module != scriptRunnerModule {
			continue
		}
		interpreter := stringParam(n.Params, interpreterParam)
		runs := ClassifyScriptLanguage(interpreter)
		if !runs.Known || runs.Family == "" {
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

func stringParam(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	s, _ := params[key].(string)
	return s
}
