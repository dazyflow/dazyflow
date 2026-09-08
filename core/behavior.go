// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "reflect"

// Whether promoting one revision over the other changes anything a run, a
// schedule or an inbound trigger does.
//
// Ignored as editor-only, mirroring web/src/lib/diffGraphs.ts — keep the two in
// lockstep: Node.Position, Label, Collapsed, Locked, Edge.Waypoints, Graph.Frames.
// Graph.Name is NOT cosmetic — it reaches people through failure mail.
// Graph.Disabled is ignored because the endpoints read it off HEAD, so pausing is
// live the moment it is saved and is not publishable drift.
//
// A DeepEqual with those cleared rather than an allowlist, so a new field defaults
// to "publishing it matters".
func BehaviorEqual(a, b Graph) bool {
	return reflect.DeepEqual(stripCosmetic(a), stripCosmetic(b))
}

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
			n.Collapsed = false
			n.Locked = false
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
