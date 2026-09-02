// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "reflect"

// BehaviorEqual reports whether two revisions of a flow would behave the
// same: whether promoting one over the other changes anything a run, a
// schedule, or an inbound trigger does. It drives the editor's "your draft has
// changes that aren't live yet" prompt, which must not nag about cosmetics the
// diff view already ignores.
//
// Ignored as editor-only (mirror of the cosmetic set in
// web/src/lib/diffGraphs.ts; keep the two in lockstep):
//
//	Node.Position, Node.Label, Edge.Waypoints, Graph.Frames
//
// Graph.Name is NOT cosmetic: it reaches people through the flow list and
// failure mail. Graph.Disabled is ignored for a different reason: the
// scheduler, webhook and form endpoints read it off HEAD, so pausing is live
// the moment it is saved and is not publishable drift.
//
// Everything else counts. The comparison is a DeepEqual with the fields above
// cleared, not an allowlist, so a new field defaults to "publishing it
// matters", which is the safe direction.
func BehaviorEqual(a, b Graph) bool {
	return reflect.DeepEqual(stripCosmetic(a), stripCosmetic(b))
}

// stripCosmetic returns g with the editor-only and applied-from-HEAD fields
// zeroed. It copies the node and edge slices rather than editing them in
// place — the caller's graph is usually one it just loaded and still uses.
//
// Empty slices normalize to nil so a graph saved with `"nodes": []` compares
// equal to one that omits the key; DeepEqual otherwise calls those different.
func stripCosmetic(g Graph) Graph {
	out := g
	out.Frames = nil
	out.Disabled = false

	if len(g.Nodes) == 0 {
		out.Nodes = nil
	} else {
		out.Nodes = make([]Node, len(g.Nodes))
		for i, n := range g.Nodes {
			n.Position = nil
			n.Label = ""
			out.Nodes[i] = n
		}
	}

	if len(g.Edges) == 0 {
		out.Edges = nil
	} else {
		out.Edges = make([]Edge, len(g.Edges))
		for i, e := range g.Edges {
			e.Waypoints = nil
			out.Edges[i] = e
		}
	}
	return out
}
