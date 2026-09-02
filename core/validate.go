// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"fmt"
	"slices"
)

// Validate runs structural checks that do not require module manifests:
// non-empty/unique node IDs, edges reference existing nodes, edge ports
// are named, no cycles. Returns a joined error containing every problem
// found, or nil if the graph is structurally sound.
// maxReportedPerRule bounds how many individual problems one rule lists before
// it collapses to a count. Every problem is still reported; the report just
// can't grow with the graph.
const maxReportedPerRule = 10

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

	if len(g.Frames) > MaxGraphFrames {
		errs = append(errs, fmt.Errorf("graph has %d frames, limit is %d", len(g.Frames), MaxGraphFrames))
	}
	if len(g.Triggers) > MaxGraphTriggers {
		errs = append(errs, fmt.Errorf("graph has %d triggers, limit is %d", len(g.Triggers), MaxGraphTriggers))
	}
	// Every approval step in a flow parks in the SAME run and mails its whole
	// list, through the operator's transactional mailer rather than an account
	// the author connected — so the ceiling that matters is the run's, not the
	// step's. Structural (it reads a module name and a param, no manifest), so
	// every path that stores or runs a graph reaches it.
	if total := GraphApprovalRecipients(g); total > MaxGraphApprovalRecipients {
		errs = append(errs, fmt.Errorf("graph notifies %d approvers in one run, limit is %d",
			total, MaxGraphApprovalRecipients))
	}
	// The walk stops at the ceiling, so the report is "more than", not a size.
	if ApproxGraphBytes(g, MaxGraphBytes) > MaxGraphBytes {
		errs = append(errs, fmt.Errorf("graph carries more than %d bytes of settings, labels and notes",
			MaxGraphBytes))
	}

	// An identical wire drawn twice is never meaningful — the second carries
	// the same value from the same port — but only a non-variadic input used
	// to catch it, so duplicates accumulated freely on a variadic pin.
	//
	// Reported in bounded volume: a graph at the connection ceiling can be
	// entirely duplicates, and one error per wire would make the report
	// costlier than the graph.
	type edgeKey struct{ from, fromPort, to, toPort string }
	seen := make(map[edgeKey]int, len(g.Edges))
	dupes, waypointOverruns, waypoints := 0, 0, 0

	for i, e := range g.Edges {
		k := edgeKey{e.From, e.FromPort, e.To, e.ToPort}
		if first, dup := seen[k]; dup {
			dupes++
			if dupes <= maxReportedPerRule {
				errs = append(errs, fmt.Errorf("edge %d duplicates edge %d (%s.%s → %s.%s)",
					i, first, e.From, e.FromPort, e.To, e.ToPort))
			}
		} else {
			seen[k] = i
		}
		if len(e.Waypoints) > MaxEdgeWaypoints {
			waypointOverruns++
			if waypointOverruns <= maxReportedPerRule {
				errs = append(errs, fmt.Errorf("edge %d has %d waypoints, limit is %d",
					i, len(e.Waypoints), MaxEdgeWaypoints))
			}
		}
		waypoints += len(e.Waypoints)
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
	if dupes > maxReportedPerRule {
		errs = append(errs, fmt.Errorf("%d duplicate connections in total", dupes))
	}
	if waypointOverruns > maxReportedPerRule {
		errs = append(errs, fmt.Errorf("%d connections exceed the %d-waypoint limit in total",
			waypointOverruns, MaxEdgeWaypoints))
	}
	if waypoints > MaxGraphWaypoints {
		errs = append(errs, fmt.Errorf("graph has %d connection waypoints in total, limit is %d",
			waypoints, MaxGraphWaypoints))
	}

	// An unrecognized on_error falls through the engine's switch as "abort",
	// so a typo silently drops the failure handling the author asked for.
	// Structural (the four policies need no manifest), which is what makes it
	// enforced on every path a graph can arrive by.
	for i, e := range g.Edges {
		if !e.OnError.Valid() {
			errs = append(errs, fmt.Errorf(
				"edge %d (%s→%s): unknown on_error %q (expected one of abort, skip, retry, fallback)",
				i, e.From, e.To, string(e.OnError)))
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
	return validateManifests(g, manifests, manifestRules{
		unknownModuleIsError: true,
		requireInputs:        true,
		flagUnsafeRetry:      true,
	})
}

// ValidateRuntime is the gate the daemon applies when a graph is STORED or
// RUN, as opposed to authored. It enforces the wiring rules that describe
// the data model itself — a single-value input takes one wire, a variadic
// one respects its min/max, an edge names ports that exist and whose types
// meet — because the run path cannot honour a wiring it can't represent: a
// second wire into a single-value input silently wins over the first, and
// the author is never told.
//
// Two editor-only rules are deliberately left out. An unconnected required
// input is a work-in-progress state the editor nags about; at run time the
// step fails on its own with a better message. And a module missing from
// the catalog is tolerated rather than rejected — a tenant's runner and MCP
// drops live outside the default palette, so "unknown here" is not
// "invalid" — the port-level rules that need its port list are skipped for it.
// Fan-in is not one of those: it applies to every step, catalogued or not.
func ValidateRuntime(g Graph, manifests map[string]Manifest) error {
	if len(manifests) == 0 {
		// No catalog to validate against (a unit harness, or a resolver that
		// exposes none): degrade to the structural rules.
		return Validate(g)
	}
	return validateManifests(g, manifests, manifestRules{})
}

// manifestRules selects which manifest-level checks apply. The zero value is
// the run-path set; the editor turns the two authoring rules on.
type manifestRules struct {
	// unknownModuleIsError rejects a node whose module isn't in the catalog.
	unknownModuleIsError bool
	// requireInputs flags a required input with neither a wire nor an inline
	// param value.
	requireInputs bool
	// flagUnsafeRetry flags on_error=retry on a non-idempotent module — a
	// judgement about risk, not an invalid wiring, so the run path leaves a
	// stored flow that already made that choice alone.
	flagUnsafeRetry bool
}

// inputKey addresses one input pin: a node and one of its ports.
type inputKey struct{ node, port string }

func validateManifests(g Graph, manifests map[string]Manifest, rules manifestRules) error {
	errs := []error{}
	if err := Validate(g); err != nil {
		errs = append(errs, err)
	}

	nodeManifest := make(map[string]Manifest, len(g.Nodes))
	triggerSteps := 0
	for _, n := range g.Nodes {
		m, ok := manifests[n.Module]
		if !ok {
			if rules.unknownModuleIsError {
				errs = append(errs, fmt.Errorf("node %q references unknown module %q", n.ID, n.Module))
			}
			continue
		}
		nodeManifest[n.ID] = m
		if m.ExecutionModel == ExecutionTrigger {
			triggerSteps++
		}
	}
	// Trigger STEPS count against the same ceiling as the Triggers array —
	// see MaxGraphTriggers. Which steps are triggers is a manifest property,
	// so this rule lives here rather than in the structural Validate; every
	// path that stores or runs a graph (SaveGraph, SubmitGraphOpts) reaches it
	// through ValidateRuntime with the tenant's catalog in hand.
	if total := triggerSteps + len(g.Triggers); total > MaxGraphTriggers {
		errs = append(errs, fmt.Errorf("graph has %d triggers (%d trigger steps, %d declared), limit is %d",
			total, triggerSteps, len(g.Triggers), MaxGraphTriggers))
	}

	// A switched-off node never executes: at run time the worker records it as
	// skipped and the skip cascade prunes everything downstream. So whether it
	// is "wired up correctly" is irrelevant to whether the flow can run —
	// exclude disabled nodes (and the edges touching them, which carry no data)
	// from the required-input / port-shape / MIME checks below. This mirrors the
	// editor, which never nags about a switched-off step's config. Edges INTO a
	// live node are still counted toward its fan-in, so a live node fed only by
	// a disabled upstream isn't spuriously flagged "required input unconnected".
	disabled := make(map[string]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.Disabled {
			disabled[n.ID] = true
		}
	}

	incoming := make(map[inputKey]int)

	for i, e := range g.Edges {
		src, srcOK := nodeManifest[e.From]
		dst, dstOK := nodeManifest[e.To]
		if !srcOK || !dstOK {
			// A module this daemon has no manifest for gets no port rules (see
			// ValidateRuntime), but the wire still counts toward fan-in: that
			// rule needs no port list, and AssembleInput keeps exactly one
			// value per port whether or not a manifest was available.
			incoming[inputKey{e.To, e.ToPort}]++
			continue
		}
		// An edge touching a switched-off node carries no data, so skip the
		// port-shape and MIME checks on it — but still count the wire toward the
		// destination's fan-in so a live node fed by a disabled upstream doesn't
		// read as "unconnected".
		if disabled[e.From] || disabled[e.To] {
			incoming[inputKey{e.To, e.ToPort}]++
			continue
		}
		// A step whose ports come from its params (subgraph's
		// input_map/output_map) can't be judged against the manifest.
		if src.DynamicPorts || dst.DynamicPorts {
			incoming[inputKey{e.To, e.ToPort}]++
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
		if !rules.flagUnsafeRetry {
			break
		}
		if e.OnError != OnErrorRetry {
			continue
		}
		if disabled[e.From] || disabled[e.To] {
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
			// No manifest, so no port list to judge — but a step outside the
			// catalog is not a step outside the data model. It runs on a runner
			// or an MCP host, and AssembleInput writes one value per port there
			// too, so a second wire into the same port is silently dropped.
			// Without this, the fan-in rule the canvas enforces was skipped for
			// exactly the steps the canvas cannot see: 300 wires into one input
			// stored, ran, and delivered whichever value was walked last.
			if !disabled[n.ID] {
				errs = append(errs, unknownModuleFanIn(g, n, incoming)...)
			}
			continue
		}
		// A switched-off step never runs; don't require its inputs be wired.
		if disabled[n.ID] {
			continue
		}
		// Dynamic-port steps: the manifest's port list isn't the contract, so
		// required inputs and port shape can't be read off it. Fan-in still
		// applies — it needs no port list. A dynamic port carries one value
		// (subgraph's input_map seeds one Ref per parent port), so a second
		// wire is silently dropped at assembly just as it would be anywhere
		// else. Walk the edges rather than the incoming map to keep the
		// message order deterministic.
		if m.DynamicPorts {
			reported := map[string]bool{}
			for _, e := range g.Edges {
				if e.To != n.ID || reported[e.ToPort] {
					continue
				}
				if count := incoming[inputKey{n.ID, e.ToPort}]; count > 1 {
					reported[e.ToPort] = true
					errs = append(errs, fmt.Errorf("node %q: input %q has %d connections, but a step whose ports come from its own settings takes one value per port",
						n.ID, e.ToPort, count))
				}
			}
			continue
		}
		for _, p := range m.Inputs {
			count := incoming[inputKey{n.ID, p.Port}]
			if rules.requireInputs && p.Required && count == 0 && !hasInlineParamValue(n.Params, p.Port) {
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
				// No port in the catalog declares a Max, so without a default
				// the only ceiling on fan-in was the graph's connection cap —
				// and every wire is a value the run has to assemble and store.
				max := DefaultMaxVariadicFanIn
				if p.Max != nil {
					max = *p.Max
				}
				// A declared max is the drop's business up to the absolute
				// ceiling — see MaxVariadicFanIn. Not every manifest is ours.
				if max > MaxVariadicFanIn {
					max = MaxVariadicFanIn
				}
				if count > max {
					errs = append(errs, fmt.Errorf("node %q: variadic input %q has %d connections, max %d",
						n.ID, p.Port, count, max))
				}
			}
		}
	}

	return errors.Join(errs...)
}

// unknownModuleFanIn reports each input port of n that more than one wire
// reaches. Walks the edges (not the incoming map) so the message order is
// deterministic, and reports each port once.
func unknownModuleFanIn(g Graph, n Node, incoming map[inputKey]int) []error {
	var errs []error
	reported := map[string]bool{}
	for _, e := range g.Edges {
		if e.To != n.ID || reported[e.ToPort] {
			continue
		}
		if count := incoming[inputKey{n.ID, e.ToPort}]; count > 1 {
			reported[e.ToPort] = true
			errs = append(errs, fmt.Errorf("node %q: input %q has %d connections, but a step this instance has no description for takes one value per port",
				n.ID, e.ToPort, count))
		}
	}
	return errs
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
	issues = append(issues, ManifestLintIssues(g, manifests)...)
	issues = append(issues, structuredIntoTextWarnings(g, manifests)...)
	return append(issues, cardinalityMismatchWarnings(g, manifests)...)
}

// structuredIntoTextWarnings flags edges that feed STRUCTURED data (a list of
// rows, or a JSON object) straight into a plain-text input — the "I wired raw
// data into the email body / message and got a wall of JSON" mistake. It's a
// WARN, not an error: it's occasionally intentional, and the run won't fail —
// it'll just look ugly. The fix is to route through a formatting step
// (render_text) first.
//
// High-precision by design: it only fires when the source port is declared a
// list OR explicitly application/json, AND the destination input accepts ONLY
// text/plain. A formatting drop's rows input isn't text/plain, so it's never
// flagged. Ports with no declared MIME (wildcards) are left alone — we can't
// tell if they're structured, so we don't guess. Needs manifests; returns nil
// without them.
func structuredIntoTextWarnings(g Graph, manifests map[string]Manifest) []LintIssue {
	if len(manifests) == 0 {
		return nil
	}
	byID := make(map[string]Manifest, len(g.Nodes))
	for _, n := range g.Nodes {
		if m, ok := manifests[n.Module]; ok {
			byID[n.ID] = m
		}
	}
	var out []LintIssue
	for _, e := range g.Edges {
		src, ok1 := byID[e.From]
		dst, ok2 := byID[e.To]
		if !ok1 || !ok2 {
			continue
		}
		op, hasOut := src.Output(e.FromPort)
		ip, hasIn := dst.Input(e.ToPort)
		if !hasOut || !hasIn {
			continue
		}
		// The pass pin is an untyped sequencing wildcard — it threads whatever
		// the author routed through it, so we can't (and shouldn't) judge it as
		// "structured". Skip it, else a trigger's pass output (now declared,
		// untyped) wired into a text input would false-positive here.
		if e.FromPort == PassPort {
			continue
		}
		// A source is "structured" if it's a declared list or JSON, OR it's a
		// trigger output that isn't plain text — a webhook/form body is an
		// untyped (wildcard-MIME) object or list, the single most common thing a
		// beginner wires straight into a message/email body. Including triggers
		// here is what catches that case the typed checks miss.
		structuredSource := op.List ||
			slices.Contains(op.MIME, "application/json") ||
			(src.ExecutionModel == ExecutionTrigger && !mimeIsTextOnly(op.MIME))
		if structuredSource && mimeIsTextOnly(ip.MIME) {
			out = append(out, LintIssue{
				Code:     "structured_into_text",
				Severity: LintWarn,
				Message: fmt.Sprintf("node %q feeds raw structured data into %q's text input %q — reference a specific field (e.g. ${trigger.body.<field>} or ${upstream.%s.%s[0].<field>}) or format it with render_text; otherwise it arrives as raw JSON.",
					e.From, e.To, e.ToPort, e.From, e.FromPort),
				NodeIDs: []string{e.From, e.To},
			})
		}
	}
	return out
}

// cardinalityMismatchWarnings flags an edge from a MANY (list) output into a
// ONE (single-value) input — the "you fed a list into a one-at-a-time step"
// case. In the simplified data model the intent is "run the step once per
// item" (a For each loop); surfacing it lets the author confirm that's what
// they want (or wire a single item / a render step instead). WARN, not error:
// a one-input that's untyped (KindAny) or variadic legitimately takes the whole
// list, so those are skipped to keep the signal low-noise.
//
// NOTE: this is the detection half of the many→one model rule. Actually RUNNING
// the consumer per item automatically (implicit for-each) is a separate engine
// change; until then, the author wraps it in for_each — which this points to.
func cardinalityMismatchWarnings(g Graph, manifests map[string]Manifest) []LintIssue {
	if len(manifests) == 0 {
		return nil
	}
	byID := make(map[string]Manifest, len(g.Nodes))
	for _, n := range g.Nodes {
		if m, ok := manifests[n.Module]; ok {
			byID[n.ID] = m
		}
	}
	var out []LintIssue
	for _, e := range g.Edges {
		src, ok1 := byID[e.From]
		dst, ok2 := byID[e.To]
		if !ok1 || !ok2 {
			continue
		}
		op, hasOut := src.Output(e.FromPort)
		ip, hasIn := dst.Input(e.ToPort)
		if !hasOut || !hasIn || ip.Variadic {
			continue
		}
		if op.Cardinality() == Many && ip.Cardinality() == One && ip.Kind() != KindAny {
			out = append(out, LintIssue{
				Code:     "many_into_one",
				Severity: LintWarn,
				Message: fmt.Sprintf("node %q sends many items into %q's single-item input %q — to act on each item, wrap %q in a For each loop; otherwise feed a single item.",
					e.From, e.To, e.ToPort, e.To),
				NodeIDs: []string{e.From, e.To},
			})
		}
	}
	return out
}

// mimeIsTextOnly reports whether a port accepts text/plain and nothing else —
// a pure text sink. An empty set is a wildcard, not a text-only sink.
func mimeIsTextOnly(mimes []string) bool {
	if len(mimes) == 0 {
		return false
	}
	for _, m := range mimes {
		if m != "text/plain" {
			return false
		}
	}
	return true
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
