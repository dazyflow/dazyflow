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
			if p.Required && count == 0 {
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
