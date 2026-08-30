// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"

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
// Unresolvable cases return ok=false rather than an error, matching every other
// substituter: the placeholder stays as written. That is the same silence this
// function exists to end, so it is deliberately narrow — no trigger node in the
// graph at all, or a manual run where none fired.
func triggerSubstituter(graph core.Graph, prior map[string]core.Result) Substituter {
	return func(_ context.Context, scheme, path string) (string, bool, error) {
		if scheme != "trigger" || prior == nil {
			return "", false, nil
		}
		id := firedTriggerNode(graph, prior)
		if id == "" {
			return "", false, nil
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
