// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

// triggerSubstituter resolves ${trigger.port.path…} against the trigger node
// the run actually started from: the same data ${upstream.<id>.port.path}
// reaches, named the way a flow author thinks about it. WHICH node it means is
// decided by the run, not the graph: a graph may carry several trigger nodes,
// and the one that fired is the one holding a result in prior.
//
// An unresolvable ${trigger.…} is an error, not a shrug. SubstituteString
// leaves an UNKNOWN scheme alone so arbitrary ${…} text in JSON and shell
// survives; `trigger` is a known scheme with an owner, so failing to resolve it
// is a broken reference, exactly as for `upstream`. The alternative once mailed
// a customer a literal "${trigger.body.version}" with nothing failing or
// logging. A manual Run of a webhook flow therefore fails here; "Send test
// event" seeds the trigger and is the way to exercise such a flow by hand.
func triggerSubstituter(graph core.Graph, prior map[string]core.Result) Substituter {
	return func(_ context.Context, scheme, path string) (string, bool, error) {
		if scheme != "trigger" {
			return "", false, nil
		}
		if prior == nil {
			return "", true, fmt.Errorf(
				"trigger: ${trigger.%s} cannot be resolved here — this code path has no run results", path)
		}
		id := firedTriggerNode(graph, prior)
		if id == "" {
			return "", true, fmt.Errorf(
				"trigger: no trigger fired in this run, so ${trigger.%s} has nothing to read "+
					"(a manual Run has no trigger data — use Send test event)", path)
		}
		// Delegate to the upstream resolver so the two schemes can never
		// disagree about path syntax: dots, [0] indexing and the stringify
		// rules are defined once.
		v, err := resolveUpstreamPath(prior, id+"."+path)
		if err != nil {
			return "", true, err
		}
		return stringifyForTemplate(v), true, nil
	}
}

// firedTriggerNode returns the id of the trigger node this run started from,
// or "" when none did.
//
// Graph order, not map order, so a graph with two fired triggers (which the
// runtime does not produce, but a seeded test can) resolves the same way every
// time rather than picking whichever the map iterated first.
func firedTriggerNode(graph core.Graph, prior map[string]core.Result) string {
	for _, n := range graph.Nodes {
		if !core.IsTriggerModule(n.Module) {
			continue
		}
		if _, ran := prior[n.ID]; ran {
			return n.ID
		}
	}
	return ""
}
