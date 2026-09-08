// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
)

// The pattern is required for three of four modes, and in the fourth a
// Replacements table can stand in — a rule the params schema cannot express,
// `required` knowing nothing about `mode`.
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
