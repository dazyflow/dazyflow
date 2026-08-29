// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"sort"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// The passthrough pin is added to every drop that doesn't opt out
// (core.WithPassthrough), and on a ROUTER that is a correctness bug rather
// than a cosmetic one. `pass` is re-emitted whenever the node SUCCEEDS,
// regardless of which port the payload actually took — so a node wired to a
// router's pass pin fires on every branch at once, punching a hole straight
// through the routing the author drew. Manifest.NoPassthrough says so:
//
//	pure routers (Branch): pass is emitted on every success regardless of
//	which port the payload took, so a node wired to it fires on BOTH
//	branches — punching a hole straight through the routing.
//
// Nothing enforced that. NoPassthrough is a hand-set bool, a new routing drop
// that forgot it would ship with that hole, and every existing test would pass
// — the flow just quietly runs both branches.
//
// A complete guard is not available: "these two output ports are mutually
// exclusive" is not derivable from a manifest, and split_rows proves the
// naive reading wrong (matched/unmatched look like a router's ports, but it
// emits BOTH halves on every run, so the pin is right there). So this settles
// for two checks that between them cover the realistic mistakes:
//
//  1. the drops that opt out today still do — the regression case, someone
//     refactoring a manifest and dropping the flag;
//  2. nothing NEW appears with exclusive-looking output ports and no flag —
//     the new-router case, which is the one that would actually ship broken.

// routersMustOptOut is every drop that routes a payload down exactly one of
// several ports, or emits a bare verdict rather than a payload. Removing the
// flag from any of these reintroduces the hole, so they are pinned by name.
var routersMustOptOut = []string{
	// Routers: the payload leaves by one port, never both.
	"branch", "if", "contains", "switch",
	// Approval routes to approved/rejected once a human answers.
	"await_approval",
	// Predicates: a 1/0 verdict, not a payload to thread onward. The pin
	// would also steal an operand slot on the compact operator chip.
	"compare", "and", "or", "not",
	"eq", "neq", "gt", "gte", "lt", "lte",
}

// exclusiveish are output port names that usually mean "the payload went
// exactly one way". Used only as a tripwire for NEW drops.
var exclusiveish = map[string]bool{
	"then": true, "else": true, "yes": true, "no": true,
	"matched": true, "unmatched": true, "match": true, "nomatch": true,
	"found": true, "missing": true, "hit": true, "miss": true,
	"ok": true, "err": true, "success": true, "failure": true,
	"true": true, "false": true, "valid": true, "invalid": true,
	"default": true, "otherwise": true,
}

// splitsRatherThanRoutes are drops whose exclusive-LOOKING ports are not
// exclusive at all: every one of them is emitted on every run, so there is no
// routing for a pass pin to bypass. Add here only after checking the executor
// actually writes every such port unconditionally.
var splitsRatherThanRoutes = map[string]string{
	"split_rows": "forks a row stream and emits BOTH halves every run — " +
		"matched and unmatched are two outputs, not two branches",
}

func TestRoutersOptOutOfPassthrough(t *testing.T) {
	byID := map[string]core.Manifest{}
	for _, d := range allDrops(t) {
		byID[d.id] = d.manifest
	}

	// 1. The pinned set still opts out.
	for _, id := range routersMustOptOut {
		m, ok := byID[id]
		if !ok {
			// Renamed or removed: decide deliberately rather than letting the
			// guard quietly stop covering it.
			t.Errorf("%s is in routersMustOptOut but is not registered — "+
				"if it was renamed or retired, update the list", id)
			continue
		}
		if !m.NoPassthrough {
			t.Errorf("%s routes or predicates but no longer sets NoPassthrough: "+
				"the pass pin is re-emitted on every success regardless of which "+
				"port the payload took, so anything wired to it fires on every "+
				"branch at once", id)
		}
	}

	// 2. Nothing new looks like a router while keeping the pin.
	var suspects []string
	for _, d := range allDrops(t) {
		if d.manifest.NoPassthrough || splitsRatherThanRoutes[d.id] != "" {
			continue
		}
		if d.manifest.ExecutionModel == core.ExecutionTrigger || d.manifest.Category == "trigger" {
			continue
		}
		var hits []string
		for _, p := range d.manifest.Outputs {
			if exclusiveish[strings.ToLower(p.Port)] {
				hits = append(hits, p.Port)
			}
		}
		if len(hits) >= 2 {
			sort.Strings(hits)
			suspects = append(suspects, d.id+" ("+strings.Join(hits, ", ")+")")
		}
	}
	sort.Strings(suspects)
	for _, s := range suspects {
		t.Errorf("%s has exclusive-looking output ports but keeps the pass pin. "+
			"If the payload leaves by ONE of them, set NoPassthrough — otherwise a "+
			"node wired to pass fires on every branch. If instead every port is "+
			"emitted on every run, add it to splitsRatherThanRoutes with the reason.", s)
	}
}
