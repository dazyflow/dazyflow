// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
)

// The Regex step's pattern is required for three of its four modes, and in the
// fourth a Replacements table can stand in for it — a rule the params schema
// cannot express, since `required` knows nothing about `mode`.
//
// So the schema doesn't claim it, and this does. The point is WHEN the author
// hears about it: a step with nothing to search for fails the moment it runs,
// and the whole value of saying so here is that it's said while the flow is
// still being written.
const (
	regexModule       = "regex"
	regexPatternParam = "pattern"
	regexTableParam   = "replacements"
)

func lintRegexPattern(g Graph) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		if n.Module != regexModule {
			continue
		}
		if strings.TrimSpace(stringParam(n.Params, regexPatternParam)) != "" {
			continue
		}
		// In replace mode the table supplies the words to look for, so a step
		// with one is fully configured without a pattern.
		mode := stringParam(n.Params, "mode")
		if mode == "replace" && hasEntries(n.Params[regexTableParam]) {
			continue
		}
		msg := fmt.Sprintf("Node %q has no pattern, so there is nothing for it to search for.", n.ID)
		if mode == "" || mode == "replace" {
			msg += " Give it a regular expression, or fill in its Replacements table."
		}
		issues = append(issues, LintIssue{
			Code:     "regex_no_pattern",
			Severity: LintError,
			Message:  msg,
			NodeIDs:  []string{n.ID},
			Fields:   []string{regexPatternParam},
		})
	}
	return issues
}

// hasEntries reports whether a params value is a map holding at least one
// non-blank key — an editor row half-typed is not a configured table.
func hasEntries(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for k := range m {
		if strings.TrimSpace(k) != "" {
			return true
		}
	}
	return false
}
