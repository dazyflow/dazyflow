// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
)

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
