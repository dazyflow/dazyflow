package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
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

// secretPlaceholderPattern matches the `${scheme.<path>}` schemes that
// resolve to a secret at execution time: the scoped store (secret/tenant/
// workspace/flow), builtin, and vault. Mirrors engine.placeholderPattern
// (dot separator) but restricted to the resolvers the lint cares about
// (upstream/item placeholders are graph-internal, not secrets).
var secretPlaceholderPattern = regexp.MustCompile(`\$\{(secret|builtin|vault)\.[^}]*\}`)

// templatePattern matches ANY `${scheme.path}` placeholder (secret or
// upstream/item). A value containing one isn't a hardcoded literal, so
// the hardcoded-secret rule skips it.
var templatePattern = regexp.MustCompile(`\$\{[a-z0-9_-]+\.[^}]*\}`)

// secretKeyName matches param/env key names that conventionally hold a
// credential. A literal (non-placeholder) value under such a key is the
// "I pasted my token into the graph" anti-pattern.
var secretKeyName = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|apikey|auth|authorization|credential|private[_-]?key|access[_-]?key|client[_-]?secret)`)

// knownSecretValue matches values that are almost certainly real
// credentials regardless of the field they sit in: provider key
// prefixes and PEM private-key blocks. Conservative on purpose — these
// patterns don't fire on ordinary config strings.
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

// minLiteralSecretLen avoids flagging short placeholder-ish values
// ("changeme", "x") under a secret-shaped key — real pasted secrets are
// long. Content-prefix matches (knownSecretValue) ignore this floor.
const minLiteralSecretLen = 12

// upstreamRefPattern captures the node-ID segment of an
// `${upstream.<nodeID>.<port>…}` reference — the first run of characters
// after `upstream.`, up to the next `.`, `[`, or `}`. Node IDs are slugs,
// so the captured segment is the whole ID even when it carries `-`/`_`.
// `${item.…}` (loop body) and secret schemes use different prefixes and
// are intentionally not matched.
var upstreamRefPattern = regexp.MustCompile(`\$\{upstream\.([^.}\[]+)`)

// templatePlaceholderPattern matches the `REPLACE_WITH_<TOKEN>` markers
// that ship inside template graph fixtures (sheet IDs, Notion DB UUIDs,
// …) for fields a user must fill in before the flow can do real work.
// A forked template that still carries one will silently fail at run
// time — the placeholder is a literal string the API rejects only when
// the run actually fires. Flagging it at save/validate time is the
// nudge that prevents "I forked the template, hit run, got an unhelpful
// error" sequence.
var templatePlaceholderPattern = regexp.MustCompile(`REPLACE_WITH_[A-Z0-9_]+`)

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

	issues := make([]LintIssue, 0)
	issues = append(issues, lintHardcodedSecrets(g)...)
	issues = append(issues, lintSecretToPersistence(g, nodesByID)...)
	issues = append(issues, lintTemplatePlaceholders(g)...)
	issues = append(issues, lintDanglingReferences(g, nodesByID)...)
	issues = append(issues, lintTriggers(g)...)
	if len(issues) == 0 {
		return nil
	}
	return issues
}

// lintTemplatePlaceholders flags REPLACE_WITH_… markers still present
// in a node's params or env — the canonical "user forked a template
// but didn't fill in the sheet ID / DB UUID" trap. Emits one issue per
// node (first hit), severity error because the run is guaranteed to
// fail at the placeholder field.
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
			"Node %q (module %s) still has the template placeholder %q in %s. Replace it with the real value before running — the upstream service will reject it as-is.",
			nodeID, module, marker, field,
		),
		NodeIDs: []string{nodeID},
	}
}

// walkParams performs the depth-first traversal of a param value (string /
// map / slice) shared by every lint rule that scans node params. For each
// string leaf it calls visit(path, str), where path is the dotted/indexed
// key path to that leaf (keyPath for the root). Maps are visited in sorted
// key order and slices in index order, so the traversal — and therefore the
// "first hit" any caller observes — is deterministic. If visit returns true
// the walk stops immediately and walkParams returns true (first-match
// short-circuit); otherwise it returns false after visiting every leaf.
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

// findTemplatePlaceholder walks params depth-first and returns the
// first (field path, matched marker) it finds, or ("","") if none.
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

// lintDanglingReferences warns when a node interpolates
// `${upstream.<id>.…}` for an <id> that isn't a step in the graph — the
// classic "the step this field pointed at was deleted or renamed" breakage
// (e.g. a form question renamed so a saved reference now dangles). The
// reference only fails at run time, when the engine can't find the node;
// surfacing it at save time turns a silent dead end into an obvious fix.
//
// Scope: node existence only. Port- and field-level validation needs the
// upstream node's manifest or live output shape, which this pure pass
// doesn't have — so a reference to a real node's wrong/renamed FIELD isn't
// caught here (that's the reference picker's job, and a future live check).
// Conservative by construction: it only flags an id that matches no node,
// so it never warns about a valid reference.
func lintDanglingReferences(g Graph, nodesByID map[string]Node) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		seen := map[string]bool{}
		missing := make([]string, 0)
		add := func(id string) {
			if _, ok := nodesByID[id]; ok || seen[id] {
				return
			}
			seen[id] = true
			missing = append(missing, id)
		}
		collectUpstreamRefs(n.Params, add)
		for _, v := range n.Env {
			for _, id := range upstreamRefIDs(v) {
				add(id)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		issues = append(issues, LintIssue{
			Code:     "dangling_reference",
			Severity: LintWarn,
			Message: fmt.Sprintf(
				"Node %q (module %s) references the output of %s, which isn't a step in this flow — it was likely deleted or renamed. Re-point the field with the { } picker, or the run will fail when it tries to read it.",
				n.ID, n.Module, quotedList(missing),
			),
			NodeIDs: []string{n.ID},
		})
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].NodeIDs[0] < issues[j].NodeIDs[0]
	})
	return issues
}

// collectUpstreamRefs walks a param value (string / map / slice) and calls
// add for every ${upstream.<id>…} node ID it finds.
func collectUpstreamRefs(v any, add func(string)) {
	walkParams("", v, func(_, str string) bool {
		for _, id := range upstreamRefIDs(str) {
			add(id)
		}
		return false // visit every leaf
	})
}

// upstreamRefIDs returns the upstream node IDs referenced in s.
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

// quotedList renders ["a","b"] as `"a", "b"` for human-readable messages.
func quotedList(ids []string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%q", id)
	}
	return strings.Join(parts, ", ")
}

// lintSecretToPersistence is the original rule: a secret-bearing node
// with a forward path into a persistence sink.
func lintSecretToPersistence(g Graph, nodesByID map[string]Node) []LintIssue {
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
	return walkParams("", v, func(_, str string) bool {
		return secretPlaceholderPattern.MatchString(str)
	})
}

// hardcodedSecretExempt lists params (per module) where a literal
// secret value is the design, not the anti-pattern: trigger bearer
// secrets live in the graph on purpose — the /trigger endpoint
// authenticates callers against them, the editor offers a Generate
// button, and trigger_webhook_no_secret *requires* one. Without this
// exemption the two lints contradict each other ("set a secret" →
// "don't hardcode that secret"). Only the key-name heuristic is
// suppressed: a pasted provider credential (ghp_…, sk_live_…) in an
// exempted field still fires via knownSecretValue.
var hardcodedSecretExempt = map[string]map[string]bool{
	// The multi-key `secrets` list (zero-downtime rotation) holds generated
	// trigger keys by design.
	"webhook_input": {"secrets": true},
}

// lintHardcodedSecrets flags literal credentials pasted into a node's
// params/env instead of referenced via ${secret.name}. Two triggers:
//   - a value matching a known provider-key/PEM pattern, anywhere; or
//   - a long literal string under a secret-shaped key name (token,
//     password, api_key, authorization, …) that isn't a ${...} template.
//
// One issue per node (de-duplicated) so a node with several pasted keys
// doesn't spam the banner.
func lintHardcodedSecrets(g Graph) []LintIssue {
	issues := make([]LintIssue, 0)
	for _, n := range g.Nodes {
		if field := findHardcodedSecret("", n.Params, hardcodedSecretExempt[n.Module]); field != "" {
			issues = append(issues, hardcodedIssue(n.ID, n.Module, field))
			continue
		}
		flagged := ""
		for k, v := range n.Env {
			if knownSecretValue.MatchString(v) ||
				(secretKeyName.MatchString(k) && isLiteralSecret(v)) {
				flagged = "env." + k
				break
			}
		}
		if flagged != "" {
			issues = append(issues, hardcodedIssue(n.ID, n.Module, flagged))
		}
	}
	// Stable order for reproducible output/tests.
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
	}
}

// findHardcodedSecret walks params depth-first and returns the first
// field path (e.g. "headers.Authorization") that looks like a pasted
// secret, or "" if none. keyPath is the dotted path to v's container.
// exempt holds top-level param names where the key-name heuristic is
// suppressed (see hardcodedSecretExempt); provider-pattern values are
// flagged regardless.
func findHardcodedSecret(keyPath string, v any, exempt map[string]bool) string {
	field := ""
	walkParams(keyPath, v, func(path, str string) bool {
		if knownSecretValue.MatchString(str) {
			field = orSelf(path)
			return true
		}
		// Key-name heuristic only applies when we know the key (path
		// non-empty) — a bare top-level string param has no key context.
		// Exemption matches the ROOT param so a list like `secrets`
		// covers its elements (`secrets[0]`, `secrets[1]`, …).
		if path != "" && !exempt[rootParam(path)] && secretKeyNameLeaf(path) && isLiteralSecret(str) {
			field = path
			return true
		}
		return false
	})
	return field
}

// isLiteralSecret reports whether s is a non-template literal long
// enough to plausibly be a real credential.
func isLiteralSecret(s string) bool {
	return len(s) >= minLiteralSecretLen && !templatePattern.MatchString(s)
}

// rootParam returns the top-level param name of a key path — the part
// before the first "." or "[". So "secrets[0]" → "secrets" and
// "headers.Authorization" → "headers". Used to match field-level
// exemptions against whole params (a `secrets` exemption covers every
// `secrets[i]` element).
func rootParam(keyPath string) string {
	for i := 0; i < len(keyPath); i++ {
		if keyPath[i] == '.' || keyPath[i] == '[' {
			return keyPath[:i]
		}
	}
	return keyPath
}

// secretKeyNameLeaf checks the LAST path segment against the secret-key
// pattern (so "headers.Authorization" matches on "Authorization", not
// the whole path).
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
