// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
)

// triggerSubstituter resolves ${trigger.port.path…} against the trigger node
// the run actually started from — the same data ${upstream.<id>.port.path}
// reaches, named the way a flow author thinks about it.
//
// The scheme was offered everywhere and implemented nowhere. The {} reference
// menu lists ${trigger.body.<field>} (daemon/httpreferences.go), a lint message
// suggests it (core/validate.go), the canvas renders it as a chip reading
// "Form → email", and two comments in the webhook and form handlers state that
// it works. Nothing resolved it, and SubstituteString leaves an unknown scheme
// ALONE by design — so JSON and shell snippets survive — which meant a flow
// following the menu's own suggestion mailed out a literal "${trigger.body.
// version}" instead of a version. Nothing errored; the tests covered the menu
// offering the token and the seed's shape, and nothing covered the round trip.
//
// WHICH node it means is decided by the run, not by the graph: the trigger that
// FIRED is the one holding a result in prior. A graph may carry several trigger
// nodes (a webhook and a schedule for the same flow is an ordinary shape) and
// only one of them started any given run, so there is no ambiguity to resolve
// with a rule and none to reject at lint time.
//
// An unresolvable ${trigger.…} is an ERROR, not a shrug.
//
// This originally returned ok=false and left the placeholder as written, on the
// grounds that every other substituter does. That reasoning was wrong, and the
// difference is what "unknown" means. SubstituteString leaves an UNKNOWN SCHEME
// alone so that arbitrary ${…} text in JSON and shell survives resolution —
// nobody claimed it. `trigger` is a known scheme with an owner, so failing to
// resolve it is a broken reference, exactly as it is for `upstream`, which has
// always errored.
//
// The cost of the shrug was paid in the worst possible place: a flow with the
// step one hop further down the chain mailed a customer a literal
// "${trigger.body.version}", and nothing failed, logged, or warned. A run that
// stops with "no trigger fired in this run" is strictly better than one that
// quietly sends the template to a person.
//
// A manual Run of a webhook flow now fails here rather than sending nonsense.
// That is the intended reading: the flow asks for data this run does not have.
// "Send test event" seeds the trigger and is the way to exercise such a flow by
// hand.
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
