package core

import (
	"fmt"
	"regexp"
	"sort"
)

// LintSeverity classifies a finding. Validate() rejects graphs that
// don't pass; LintGraph() returns advisory issues users should see at
// save time but that don't block persistence — letting a real human
// override the lint in the rare case it's wrong is the right shape
// for a security-flavored heuristic.
type LintSeverity string

const (
	LintWarn  LintSeverity = "warn"
	LintError LintSeverity = "error"
)

// LintIssue is one finding from LintGraph. Multiple issues on the same
// rule are emitted separately so the UI can pin error markers per
// node pair (the source node that uses the secret, the persistence
// sink it reaches).
type LintIssue struct {
	Code     string       `json:"code"`
	Severity LintSeverity `json:"severity"`
	Message  string       `json:"message"`
	NodeIDs  []string     `json:"node_ids,omitempty"`
}

// secretPlaceholderPattern matches `${env|tenant|builtin:<path>}` —
// the schemes that resolve to a secret at execution time. Mirrors
// engine.placeholderPattern but restricted to the resolvers the
// lint cares about (upstream/item placeholders are graph-internal,
// not secrets).
var secretPlaceholderPattern = regexp.MustCompile(`\$\{(env|tenant|builtin):[^}]*\}`)

// persistenceModules is the set of native drops whose output is
// written to a place a third party (or the user later, with broader
// access) could retrieve. Wiring a secret-bearing node's output
// transitively into one of these is the canonical "I accidentally
// persisted a secret in plaintext" footgun the secret_to_persistence
// rule catches.
//
// External API sends (slack_send_message, gmail_send_email,
// http_request, github_*, etc.) are NOT included — those exchange
// the secret with the service that legitimately holds it. The
// threat model here is local or re-readable persistence after the
// fact.
//
// secret_set is included because while writing secrets to the
// tenant store is sometimes intentional (cursor storage, OAuth
// callbacks), it's also a place where graph wiring mistakes
// silently expose values; the lint is a nudge to confirm intent.
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

// LintGraph runs every advisory rule on g and returns the findings.
// Pure function; no I/O, no manifests required — the rule set works
// against module IDs and graph topology, which is enough for the
// security-flavored checks today.
//
// Rules in V1:
//
//   - secret_to_persistence: a node that resolves a secret in its
//     params/env has a forward edge path (any number of intermediate
//     nodes) into a persistence sink. The lint can't tell whether
//     the secret value actually flows through the data, so it errs
//     on the side of warning — the user knows their graph, the lint
//     surfaces the question.
func LintGraph(g Graph) []LintIssue {
	nodesByID := make(map[string]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		nodesByID[n.ID] = n
	}

	// Identify nodes whose params or env reference a secret.
	secretSources := make([]string, 0)
	for _, n := range g.Nodes {
		if nodeUsesSecret(n) {
			secretSources = append(secretSources, n.ID)
		}
	}
	if len(secretSources) == 0 {
		return nil
	}
	// Stable order keeps the issue stream reproducible across runs
	// (graph node order is preserved on save, but tests like to
	// pin expectations).
	sort.Strings(secretSources)

	// Forward adjacency for the BFS.
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
					// Don't traverse past a sink — the warning is
					// about the path into THAT sink. A persistence
					// node downstream of another persistence node
					// still gets its own warning via the outer
					// loop's BFS from the original source.
					continue
				}
				queue = append(queue, next)
			}
		}
		sort.Strings(reached) // stable test expectations
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

// nodeUsesSecret reports whether n's Params or Env contain any
// placeholder resolving against the env/tenant/builtin schemes.
// Walks Params recursively so secrets nested inside object/array
// param shapes still trigger.
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

// hasSecretRef walks an arbitrary param value (string, map, slice)
// looking for a secret placeholder. Other scalar types can't carry
// a placeholder (bools, numbers, nil) so they're skipped.
func hasSecretRef(v any) bool {
	switch t := v.(type) {
	case string:
		return secretPlaceholderPattern.MatchString(t)
	case map[string]any:
		for _, child := range t {
			if hasSecretRef(child) {
				return true
			}
		}
	case []any:
		for _, child := range t {
			if hasSecretRef(child) {
				return true
			}
		}
	}
	return false
}
