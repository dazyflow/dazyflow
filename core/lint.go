// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type LintSeverity string

const (
	LintWarn  LintSeverity = "warn"
	LintError LintSeverity = "error"
)

type LintIssue struct {
	Code     string       `json:"code"`
	Severity LintSeverity `json:"severity"`
	Message  string       `json:"message"`
	NodeIDs  []string     `json:"node_ids,omitempty"`
	Fields   []string     `json:"fields,omitempty"`
	// Data the UI needs to build a LOCALISED sentence instead of falling back to the
	// English Message. Fields cannot carry these: those are param paths resolved
	// against a schema. Keys are per-code, documented by the rule setting them.
	Values map[string]string `json:"values,omitempty"`
}

// Mirrors engine.placeholderPattern, restricted to the schemes that resolve to a
// secret. Upstream and item placeholders are graph-internal, not secrets.
var secretPlaceholderPattern = regexp.MustCompile(`\$\{(secret|builtin|vault)\.[^}]*\}`)

var templatePattern = regexp.MustCompile(`\$\{[a-z0-9_-]+\.[^}]*\}`)

var secretKeyName = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|apikey|auth|authorization|credential|private[_-]?key|access[_-]?key|client[_-]?secret)`)

var knownSecretValue = regexp.MustCompile(
	`(sk_live_[0-9A-Za-z]{8,}` + // Stripe live
		`|sk_test_[0-9A-Za-z]{8,}` + // Stripe test
		`|gh[pousr]_[0-9A-Za-z]{20,}` + // GitHub tokens (ghp_, gho_, ghu_, ghs_, ghr_)
		`|github_pat_[0-9A-Za-z_]{20,}` + // GitHub fine-grained PAT
		`|xox[baprs]-[0-9A-Za-z-]{10,}` + // Slack tokens
		`|AKIA[0-9A-Z]{16}` + // AWS access key id
		`|AIza[0-9A-Za-z_\-]{30,}` + // Google API key
		`|-----BEGIN [A-Z ]*PRIVATE KEY-----` + // PEM private key
		`)`)

// A substring every alternative of knownSecretValue must contain, so a string
// holding none cannot match — a sound pre-filter, not a heuristic. Needed because
// this lint runs on every param string on every autosave: 32.7µs → 1.7µs.
var secretValueMarkers = [...]string{"sk_", "gh", "xox", "AKIA", "AIza", "-----BEGIN "}

const minKnownSecretLen = 15

func matchesKnownSecret(s string) bool {
	if len(s) < minKnownSecretLen {
		return false
	}
	for _, m := range secretValueMarkers {
		if strings.Contains(s, m) {
			return knownSecretValue.MatchString(s)
		}
	}
	return false
}

const minLiteralSecretLen = 12

var upstreamRefPattern = regexp.MustCompile(`\$\{upstream\.([^.}\[]+)`)

var templatePlaceholderPattern = regexp.MustCompile(`REPLACE_WITH_[A-Z0-9_]+`)

// Drops whose output lands somewhere re-readable. External API sends are
// deliberately absent: those exchange the secret with the service that
// legitimately holds it, and the threat model here is later re-readability.
var persistenceModules = map[string]bool{
	"file_write":           true,
	"excel_write":          true,
	"sheets_append_row":    true,
	"postgres_insert_rows": true,
	"postgres_upsert_rows": true,
	"mysql_insert_rows":    true,
	"mysql_upsert_rows":    true,
	"sqlite_insert_rows":   true,
	"sqlite_upsert_rows":   true,
	"secret_set":           true,
}

// The rules cannot tell whether a secret VALUE actually flows down a path they
// find, so they err towards warning.
func LintGraph(g Graph) []LintIssue {
	nodesByID := make(map[string]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodesByID[n.ID] = n
	}

	issues := make([]LintIssue, 0)
	issues = append(issues, lintHardcodedSecrets(g)...)
	issues = append(issues, lintSecretToPersistence(g, nodesByID)...)
	issues = append(issues, lintTemplatePlaceholders(g)...)
	issues = append(issues, lintDanglingReferences(g, nodesByID)...)
	issues = append(issues, lintScriptLanguage(g, nodesByID)...)
	issues = append(issues, lintRegexPattern(g)...)
	issues = append(issues, lintTriggers(g)...)
	issues = append(issues, lintApprovalRecipients(g)...)
	if len(issues) == 0 {
		return nil
	}
	return issues
}

func lintTemplatePlaceholders(g Graph) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		if field, marker := findTemplatePlaceholder("", n.Params); field != "" {
			issues = append(issues, placeholderIssue(n.ID, n.Module, field, marker))
			continue
		}
		flagged, marker := "", ""
		for k, v := range n.Env {
			if m := templatePlaceholderPattern.FindString(v); m != "" {
				flagged, marker = "env."+k, m
				break
			}
		}
		if flagged != "" {
			issues = append(issues, placeholderIssue(n.ID, n.Module, flagged, marker))
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].NodeIDs[0] < issues[j].NodeIDs[0]
	})
	return issues
}

func placeholderIssue(nodeID, module, field, marker string) LintIssue {
	return LintIssue{
		Code:     "template_placeholder",
		Severity: LintError,
		Message: fmt.Sprintf(
			"Node %q (module %s) still has the example value %q in its %s field. That's filler text the template left behind — fill in your own value before running, or this step will fail.",
			nodeID, module, marker, field,
		),
		NodeIDs: []string{nodeID},
		Fields:  []string{field},
	}
}

// Maps are walked in sorted key order and slices in index order, so the "first
// hit" a caller observes is deterministic.
func walkParams(keyPath string, v any, visit func(path, str string) bool) bool {
	switch t := v.(type) {
	case string:
		return visit(keyPath, t)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if walkParams(join(keyPath, k), t[k], visit) {
				return true
			}
		}
	case []any:
		for i, child := range t {
			if walkParams(fmt.Sprintf("%s[%d]", keyPath, i), child, visit) {
				return true
			}
		}
	}
	return false
}

func findTemplatePlaceholder(keyPath string, v any) (string, string) {
	field, marker := "", ""
	walkParams(keyPath, v, func(path, str string) bool {
		if m := templatePlaceholderPattern.FindString(str); m != "" {
			field, marker = orSelf(path), m
			return true
		}
		return false
	})
	return field, marker
}

func lintDanglingReferences(g Graph, nodesByID map[string]Node) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		seen := map[string]bool{}
		missing := make([]string, 0)
		fieldSeen := map[string]bool{}
		fields := make([]string, 0)
		isMissing := func(id string) bool {
			if _, ok := nodesByID[id]; ok {
				return false
			}
			if !seen[id] {
				seen[id] = true
				missing = append(missing, id)
			}
			return true
		}
		noteField := func(path string) {
			if !fieldSeen[path] {
				fieldSeen[path] = true
				fields = append(fields, path)
			}
		}
		walkParams("", n.Params, func(path, str string) bool {
			for _, id := range upstreamRefIDs(str) {
				if isMissing(id) {
					noteField(path)
				}
			}
			return false // visit every leaf
		})
		for k, v := range n.Env {
			for _, id := range upstreamRefIDs(v) {
				if isMissing(id) {
					noteField("env." + k)
				}
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		sort.Strings(fields)
		issues = append(issues, LintIssue{
			Code:     "dangling_reference",
			Severity: LintWarn,
			Message: fmt.Sprintf(
				"Node %q (module %s) references the output of %s, which isn't a step in this flow — it was likely deleted or renamed. Re-point the field with the { } picker, or the run will fail when it tries to read it.",
				n.ID, n.Module, quotedList(missing),
			),
			NodeIDs: []string{n.ID},
			Fields:  fields,
		})
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].NodeIDs[0] < issues[j].NodeIDs[0]
	})
	return issues
}

func upstreamRefIDs(s string) []string {
	m := upstreamRefPattern.FindAllStringSubmatch(s, -1)
	if m == nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, grp := range m {
		if id := strings.TrimSpace(grp[1]); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func quotedList(ids []string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%q", id)
	}
	return strings.Join(parts, ", ")
}

func lintSecretToPersistence(g Graph, nodesByID map[string]Node) []LintIssue {
	secretSources := make([]string, 0)
	for _, n := range g.Nodes {
		if nodeUsesSecret(n) {
			secretSources = append(secretSources, n.ID)
		}
	}
	if len(secretSources) == 0 {
		return nil
	}
	sort.Strings(secretSources) // reproducible issue stream

	forward := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		forward[e.From] = append(forward[e.From], e.To)
	}

	issues := make([]LintIssue, 0)
	for _, srcID := range secretSources {
		seen := map[string]bool{srcID: true}
		queue := []string{srcID}
		reached := make([]string, 0)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, next := range forward[cur] {
				if seen[next] {
					continue
				}
				seen[next] = true
				if persistenceModules[nodesByID[next].Module] {
					reached = append(reached, next)
					continue
				}
				queue = append(queue, next)
			}
		}
		sort.Strings(reached)
		for _, sinkID := range reached {
			sink := nodesByID[sinkID]
			src := nodesByID[srcID]
			issues = append(issues, LintIssue{
				Code:     "secret_to_persistence",
				Severity: LintWarn,
				Message: fmt.Sprintf(
					"Node %q (module %s) resolves a secret in its params and feeds data into %q (module %s), which persists output. If the secret value flows through the data, it could land in plaintext on disk or in a database. Inspect the wiring or insert a transform that strips the secret before it reaches the sink.",
					srcID, src.Module, sinkID, sink.Module,
				),
				NodeIDs: []string{srcID, sinkID},
			})
		}
	}
	return issues
}

func nodeUsesSecret(n Node) bool {
	if hasSecretRef(n.Params) {
		return true
	}
	for _, v := range n.Env {
		if secretPlaceholderPattern.MatchString(v) {
			return true
		}
	}
	return false
}

func hasSecretRef(v any) bool {
	return walkParams("", v, func(_, str string) bool {
		return secretPlaceholderPattern.MatchString(str)
	})
}

// Params where a literal secret IS the design — trigger bearer keys, which
// trigger_webhook_no_secret requires — so without the exemption the two lints
// contradict each other. Only the key-name heuristic is suppressed.
var hardcodedSecretExempt = map[string]map[string]bool{
	"webhook_input": {"secrets": true},
}

func lintHardcodedSecrets(g Graph) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		if field := findHardcodedSecret("", n.Params, hardcodedSecretExempt[n.Module]); field != "" {
			issues = append(issues, hardcodedIssue(n.ID, n.Module, field))
			continue
		}
		flagged := ""
		for k, v := range n.Env {
			if matchesKnownSecret(v) ||
				(secretKeyName.MatchString(k) && isLiteralSecret(v)) {
				flagged = "env." + k
				break
			}
		}
		if flagged != "" {
			issues = append(issues, hardcodedIssue(n.ID, n.Module, flagged))
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].NodeIDs[0] < issues[j].NodeIDs[0]
	})
	return issues
}

func hardcodedIssue(nodeID, module, field string) LintIssue {
	return LintIssue{
		Code:     "hardcoded_secret",
		Severity: LintWarn,
		Message: fmt.Sprintf(
			"Node %q (module %s) appears to contain a hardcoded secret in %s. Hardcoded credentials get committed to the workspace git history and shown to anyone who can read the graph. Store it with the secret store and reference it as ${secret.name} instead.",
			nodeID, module, field,
		),
		NodeIDs: []string{nodeID},
		Fields:  []string{field},
	}
}

func findHardcodedSecret(keyPath string, v any, exempt map[string]bool) string {
	field := ""
	walkParams(keyPath, v, func(path, str string) bool {
		if matchesKnownSecret(str) {
			field = orSelf(path)
			return true
		}
		if path != "" && !exempt[rootParam(path)] && secretKeyNameLeaf(path) && isLiteralSecret(str) {
			field = path
			return true
		}
		return false
	})
	return field
}

func isLiteralSecret(s string) bool {
	return len(s) >= minLiteralSecretLen && !templatePattern.MatchString(s)
}

func rootParam(keyPath string) string {
	for i := 0; i < len(keyPath); i++ {
		if keyPath[i] == '.' || keyPath[i] == '[' {
			return keyPath[:i]
		}
	}
	return keyPath
}

func secretKeyNameLeaf(keyPath string) bool {
	leaf := keyPath
	if i := lastSep(keyPath); i >= 0 {
		leaf = keyPath[i+1:]
	}
	return secretKeyName.MatchString(leaf)
}

func lastSep(s string) int {
	idx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == ']' {
			idx = i
		}
	}
	return idx
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func orSelf(keyPath string) string {
	if keyPath == "" {
		return "a param value"
	}
	return keyPath
}

// The notifier sends ONE MESSAGE PER ADDRESS, serially, twice per approval,
// through the OPERATOR'S mailer rather than an account the author authorized —
// and on the worker goroutine that parked the run, so one parked run held a
// worker slot for hours.
const MaxApprovalRecipients = 50

// Summed across every approval step, because parallel gates all park in the same
// run: the per-step cap alone let the flood back in split across STEPS. Nothing
// throttles approval mail, an unsent approval being a run nobody can unblock.
const MaxGraphApprovalRecipients = 200

func lintApprovalRecipients(g Graph) []LintIssue {
	var issues []LintIssue
	for _, n := range g.Nodes {
		if n.Module != ApprovalModuleID {
			continue
		}
		if declared := countApprovalAddresses(n.Params); declared > MaxApprovalRecipients {
			issues = append(issues, nodeTriggerIssue("approval_too_many_recipients", n.ID,
				fmt.Sprintf("This Approval step names %d people, but at most %d are emailed — the rest are never told. Email a group address instead, or narrow the list.",
					declared, MaxApprovalRecipients)))
		}
	}
	return issues
}

const ApprovalModuleID = "await_approval"

func GraphApprovalRecipients(g Graph) int {
	total := 0
	for _, n := range g.Nodes {
		if n.Module != ApprovalModuleID || n.Disabled {
			continue
		}
		total += min(countApprovalAddresses(n.Params), MaxApprovalRecipients)
	}
	return total
}

func countApprovalAddresses(params map[string]any) int {
	raw, _ := params["approvers"].(string)
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		addr := strings.ToLower(strings.TrimSpace(part))
		if addr == "" || !strings.Contains(addr, "@") || seen[addr] {
			continue
		}
		seen[addr] = true
	}
	return len(seen)
}
