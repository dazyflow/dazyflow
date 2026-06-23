package core

import (
	"errors"
	"fmt"
)

// Validate runs structural checks that do not require module manifests:
// non-empty/unique node IDs, edges reference existing nodes, edge ports
// are named, no cycles. Returns a joined error containing every problem
// found, or nil if the graph is structurally sound.
func Validate(g Graph) error {
	var errs []error

	ids := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.ID == "" {
			errs = append(errs, errors.New("node with empty ID"))
			continue
		}
		if _, dup := ids[n.ID]; dup {
			errs = append(errs, fmt.Errorf("duplicate node ID %q", n.ID))
			continue
		}
		ids[n.ID] = struct{}{}
		if n.Module == "" {
			errs = append(errs, fmt.Errorf("node %q has empty module", n.ID))
		}
	}

	for i, e := range g.Edges {
		if _, ok := ids[e.From]; !ok {
			errs = append(errs, fmt.Errorf("edge %d: unknown source node %q", i, e.From))
		}
		if _, ok := ids[e.To]; !ok {
			errs = append(errs, fmt.Errorf("edge %d: unknown target node %q", i, e.To))
		}
		if e.FromPort == "" {
			errs = append(errs, fmt.Errorf("edge %d: empty from_port", i))
		}
		if e.ToPort == "" {
			errs = append(errs, fmt.Errorf("edge %d: empty to_port", i))
		}
		if e.From == e.To {
			errs = append(errs, fmt.Errorf("edge %d: self-loop on node %q", i, e.From))
		}
	}

	if _, err := TopologicalOrder(g); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// ValidateWithManifests runs structural checks plus port-level rules that
// require knowing each module's manifest: port existence, required inputs,
// variadic min/max, fan-in against non-variadic inputs, MIME compatibility.
// The manifests map is keyed by module ID.
func ValidateWithManifests(g Graph, manifests map[string]Manifest) error {
	errs := []error{}
	if err := Validate(g); err != nil {
		errs = append(errs, err)
	}

	nodeManifest := make(map[string]Manifest, len(g.Nodes))
	for _, n := range g.Nodes {
		m, ok := manifests[n.Module]
		if !ok {
			errs = append(errs, fmt.Errorf("node %q references unknown module %q", n.ID, n.Module))
			continue
		}
		nodeManifest[n.ID] = m
	}

	type inputKey struct{ node, port string }
	incoming := make(map[inputKey]int)

	for i, e := range g.Edges {
		src, srcOK := nodeManifest[e.From]
		dst, dstOK := nodeManifest[e.To]
		if !srcOK || !dstOK {
			continue
		}
		outPort, hasOut := src.Output(e.FromPort)
		if !hasOut {
			errs = append(errs, fmt.Errorf("edge %d: node %q (%s) has no output port %q",
				i, e.From, src.ID, e.FromPort))
			continue
		}
		inPort, hasIn := dst.Input(e.ToPort)
		if !hasIn {
			errs = append(errs, fmt.Errorf("edge %d: node %q (%s) has no input port %q",
				i, e.To, dst.ID, e.ToPort))
			continue
		}
		if !mimeCompatible(outPort.MIME, inPort.MIME) {
			errs = append(errs, fmt.Errorf(
				"edge %d: MIME mismatch %v → %v (%s.%s → %s.%s)",
				i, outPort.MIME, inPort.MIME,
				e.From, e.FromPort, e.To, e.ToPort))
		}
		incoming[inputKey{e.To, e.ToPort}]++
	}

	// Per-edge OnError sanity check: retrying a non-idempotent module is a
	// foot-gun (the partial side effects from a failed run may not be safe
	// to replay). The spec flags this at validation time.
	for i, e := range g.Edges {
		if e.OnError != OnErrorRetry {
			continue
		}
		src, ok := nodeManifest[e.From]
		if !ok {
			continue
		}
		if !src.Idempotent {
			errs = append(errs, fmt.Errorf(
				"edge %d (%s→%s): on_error=retry on a non-idempotent module %q is unsafe",
				i, e.From, e.To, src.ID))
		}
	}

	for _, n := range g.Nodes {
		m, ok := nodeManifest[n.ID]
		if !ok {
			continue
		}
		for _, p := range m.Inputs {
			count := incoming[inputKey{n.ID, p.Port}]
			if p.Required && count == 0 && !hasInlineParamValue(n.Params, p.Port) {
				// A required input is satisfied either by a wire OR by an
				// inline param of the same name — many drops read
				// input-or-param (e.g. gmail's `to` via textInputOr), and the
				// inline-pin editor fills required inputs without a wire. Only
				// flag it when there's no value from either source. This also
				// lets a for_each body node (run standalone via Engine.Run)
				// draw a required input from its ${item.…} param.
				errs = append(errs, fmt.Errorf("node %q: required input %q is unconnected",
					n.ID, p.Port))
			}
			if !p.Variadic && count > 1 {
				errs = append(errs, fmt.Errorf("node %q: non-variadic input %q has %d connections",
					n.ID, p.Port, count))
			}
			if p.Variadic {
				if p.Min != nil && count < *p.Min {
					errs = append(errs, fmt.Errorf("node %q: variadic input %q has %d connections, min %d",
						n.ID, p.Port, count, *p.Min))
				}
				if p.Max != nil && count > *p.Max {
					errs = append(errs, fmt.Errorf("node %q: variadic input %q has %d connections, max %d",
						n.ID, p.Port, count, *p.Max))
				}
			}
		}
	}

	return errors.Join(errs...)
}

// ValidateGraphFull is the single authoring gate: it runs BOTH production
// checks over g and returns their union as lint issues — the security/
// placeholder linter (LintGraph) and the manifest-level structural validator
// (ValidateWithManifests: unknown module, missing ports, unsatisfied required
// inputs, fan-in/variadic/MIME rules).
//
// Every author goes through this one function — the server-side AI generator
// and the POST /validate/graph endpoint both call it — so a graph that lints
// clean here is acceptable no matter which orchestrator composed it (the
// server's connected LLM, or an MCP host such as Claude Code authoring through
// the catalog tools). The two paths cannot drift because there is only one.
//
// When manifests is empty the structural gate is skipped (LintGraph only): the
// caller has no catalog to validate against (e.g. a unit harness), so manifest
// validation is impossible and we degrade to the placeholder/security rules.
func ValidateGraphFull(g Graph, manifests map[string]Manifest) []LintIssue {
	issues := LintGraph(g)
	return append(issues, ManifestLintIssues(g, manifests)...)
}

// ManifestLintIssues runs ValidateWithManifests and converts each structural
// problem into a repairable LintError (one issue per problem, so a repair
// prompt or UI can list them individually). Returns nil when no catalog is
// supplied — manifest validation is impossible without one.
func ManifestLintIssues(g Graph, manifests map[string]Manifest) []LintIssue {
	if len(manifests) == 0 {
		return nil
	}
	err := ValidateWithManifests(g, manifests)
	if err == nil {
		return nil
	}
	// errors.Join exposes the individual problems via Unwrap() []error — surface
	// each as its own issue so a repair prompt lists them one per line.
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		out := make([]LintIssue, 0, len(joined.Unwrap()))
		for _, e := range joined.Unwrap() {
			out = append(out, LintIssue{Code: "invalid_structure", Severity: LintError, Message: e.Error()})
		}
		return out
	}
	return []LintIssue{{Code: "invalid_structure", Severity: LintError, Message: err.Error()}}
}

// hasInlineParamValue reports whether params carries a usable value for the
// given input port name — the inline-param fallback for a required input that
// isn't wired. A nil map, missing key, nil value, or empty string counts as
// "no value"; anything else (including a "${…}" template, resolved later) is
// treated as supplied.
func hasInlineParamValue(params map[string]any, port string) bool {
	if params == nil {
		return false
	}
	v, ok := params[port]
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return s != ""
	}
	return true
}

// mimeCompatible returns true when the two MIME sets overlap. An empty set
// on either side acts as a wildcard so modules can opt out of MIME typing.
func mimeCompatible(out, in []string) bool {
	if len(out) == 0 || len(in) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(in))
	for _, m := range in {
		set[m] = struct{}{}
	}
	for _, m := range out {
		if _, ok := set[m]; ok {
			return true
		}
	}
	return false
}
