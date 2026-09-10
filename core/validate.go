// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"fmt"
	"slices"
)

// Every problem is still reported; the report just can't grow with the graph.
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
	if total := GraphApprovalRecipients(g); total > MaxGraphApprovalRecipients {
		errs = append(errs, fmt.Errorf("graph notifies %d approvers in one run, limit is %d",
			total, MaxGraphApprovalRecipients))
	}
	if ApproxGraphBytes(g, MaxGraphBytes) > MaxGraphBytes {
		errs = append(errs, fmt.Errorf("graph carries more than %d bytes of settings, labels and notes",
			MaxGraphBytes))
	}

	// Only a non-variadic input used to catch a duplicate wire, so they accumulated
	// freely on a variadic pin. Bounded volume: a graph at the connection ceiling can
	// be entirely duplicates.
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

func ValidateWithManifests(g Graph, manifests map[string]Manifest) error {
	return validateManifests(g, manifests, manifestRules{
		unknownModuleIsError: true,
		requireInputs:        true,
		flagUnsafeRetry:      true,
	})
}

// The gate for a graph being STORED or RUN rather than authored: the run path
// cannot honour a wiring it can't represent, and a second wire into a
// single-value input silently wins over the first with nobody told.
//
// Two editor-only rules are deliberately left out. An unconnected required input
// is a work-in-progress state the step reports better itself at run time. And a
// module missing from the catalog is tolerated: a tenant runner's drops live
// outside the default palette, so "unknown here" is not "invalid". Fan-in is not
// one of those — it applies to every step, catalogued or not.
func ValidateRuntime(g Graph, manifests map[string]Manifest) error {
	if len(manifests) == 0 {
		return Validate(g)
	}
	return validateManifests(g, manifests, manifestRules{})
}

type manifestRules struct {
	unknownModuleIsError bool
	requireInputs        bool
	flagUnsafeRetry      bool
}

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
	if total := triggerSteps + len(g.Triggers); total > MaxGraphTriggers {
		errs = append(errs, fmt.Errorf("graph has %d triggers (%d trigger steps, %d declared), limit is %d",
			total, triggerSteps, len(g.Triggers), MaxGraphTriggers))
	}

	// A switched-off node never executes, so whether it is wired correctly says
	// nothing about whether the flow can run. Edges INTO a live node still count
	// toward fan-in, or a live node fed only by a disabled upstream reads as
	// unconnected.
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
			// No manifest means no port rules, but the wire still counts toward fan-in:
			// AssembleInput keeps one value per port either way.
			incoming[inputKey{e.To, e.ToPort}]++
			continue
		}
		if disabled[e.From] || disabled[e.To] {
			incoming[inputKey{e.To, e.ToPort}]++
			continue
		}
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

	// The partial side effects of a failed run may not be safe to replay.
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
			// A step outside the catalog is not outside the data model: AssembleInput writes
			// one value per port on a runner too, so without this the fan-in rule was skipped
			// for exactly the steps the canvas cannot see — 300 wires into one input ran and
			// delivered whichever value was walked last.
			if !disabled[n.ID] {
				errs = append(errs, unknownModuleFanIn(g, n, incoming)...)
			}
			continue
		}
		if disabled[n.ID] {
			continue
		}
		// The manifest's port list is not the contract here. Fan-in still applies, and
		// the edges are walked rather than the incoming map to keep message order
		// deterministic.
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
				// A wire OR an inline param of the same name satisfies a required input, which
				// is also how a for_each body node draws one from its ${item.…} param.
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
				max := DefaultMaxVariadicFanIn
				if p.Max != nil {
					max = *p.Max
				}
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

// The single authoring gate: the AI generator and POST /validate/graph both call
// it, so a graph that lints clean here is acceptable whichever orchestrator
// composed it, and the paths cannot drift. Without a catalog it degrades to the
// placeholder and security rules.
func ValidateGraphFull(g Graph, manifests map[string]Manifest) []LintIssue {
	issues := LintGraph(g)
	issues = append(issues, ManifestLintIssues(g, manifests)...)
	return append(issues, WiringWarnings(g, manifests)...)
}

// WiringWarnings is the catalog-aware half of ValidateGraphFull that judges
// EDGES rather than structure: a list wired into a single-item input, raw JSON
// wired into a text one. Every issue it returns is a warning about a connection
// the author drew, and every one names both ends.
//
// It is split out for the save path, which wants these and not the structural
// errors beside them. "Unknown module" is an error there for a reason that does
// not hold on a save: the catalog can be legitimately incomplete when an MCP
// server or an API integration is momentarily unreachable, and a red
// "references unknown module" across a flow the author has not touched would be
// wrong about their flow and right only about the network.
func WiringWarnings(g Graph, manifests map[string]Manifest) []LintIssue {
	issues := structuredIntoTextWarnings(g, manifests)
	issues = append(issues, cardinalityMismatchWarnings(g, manifests)...)
	return append(issues, executableFromTriggerWarnings(g, manifests)...)
}

// A trigger's payload is written by whoever called the endpoint. Feeding it to a
// port that RUNS its text means the caller chooses the code, which is a
// different proposition from the caller choosing a value — and it does not
// announce itself on the canvas, where both are one line between two steps.
//
// A warning rather than an error: it is a legitimate design for a trusted
// internal endpoint, and the sandbox bounds what the code can reach. But it
// should be a decision someone made, not one they drew by accident.
func executableFromTriggerWarnings(g Graph, manifests map[string]Manifest) []LintIssue {
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
		if !ok1 || !ok2 || e.FromPort == PassPort {
			continue
		}
		ip, hasIn := dst.Input(e.ToPort)
		if !hasIn || !ip.Executable {
			continue
		}
		if src.ExecutionModel != ExecutionTrigger && src.Category != "trigger" {
			continue
		}
		out = append(out, LintIssue{
			Code:     "executable_from_trigger",
			Severity: LintWarn,
			Message: fmt.Sprintf("node %q is a trigger, and %q runs what arrives on %q as code — whoever calls the trigger would choose the script. Feed it from a Text step or a file instead, or make sure only trusted callers can reach %q.",
				e.From, e.To, e.ToPort, e.From),
			NodeIDs: []string{e.From, e.To},
		})
	}
	return out
}

// A warning, not an error: occasionally intentional, and the run will not fail,
// just look ugly. High-precision by design — the source must be a declared list
// or explicit JSON and the destination accept ONLY text/plain — so a formatting
// drop's rows input is never flagged.
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
		// The pass pin threads whatever the author routed through it, so a trigger's
		// untyped pass output into a text input would false-positive.
		if e.FromPort == PassPort {
			continue
		}
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

func ManifestLintIssues(g Graph, manifests map[string]Manifest) []LintIssue {
	if len(manifests) == 0 {
		return nil
	}
	err := ValidateWithManifests(g, manifests)
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		out := make([]LintIssue, 0, len(joined.Unwrap()))
		for _, e := range joined.Unwrap() {
			out = append(out, LintIssue{Code: "invalid_structure", Severity: LintError, Message: e.Error()})
		}
		return out
	}
	return []LintIssue{{Code: "invalid_structure", Severity: LintError, Message: err.Error()}}
}

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
