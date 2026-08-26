// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "reflect"

// BehaviorEqual reports whether two revisions of a flow would behave the
// same — i.e. whether promoting one over the other would change anything a
// run, a schedule, or an inbound trigger does.
//
// It exists for the editor's "your draft has changes that aren't live yet"
// prompt. That prompt used to be a plain DeepEqual of the two graphs, which
// made the canvas nag about work nobody can publish anything meaningful
// about: nudge a step, drop a note on the canvas, bend a wire, and the
// editor announced unpublished changes while the diff view — which has
// always ignored cosmetics — reported the draft as identical to the live
// version. Two features, two answers, both looking at the same pair of
// revisions.
//
// EDITOR-ONLY, ignored here (mirror of the cosmetic set in
// web/src/lib/diffGraphs.ts — keep the two in lockstep):
//
//	Node.Position    where a step sits on the canvas
//	Node.Label       what a step is called on the canvas. Editor presentation,
//	                 like its position: the engine never reads it, and nothing
//	                 outside the editor shows it — so promoting it changes
//	                 nothing about the live flow. (Graph.Name is NOT in this
//	                 list, and the difference is real: a flow's name reaches
//	                 people through the flow list and failure mail.)
//	Edge.Waypoints   hand-tuned wire routing
//	Graph.Frames     comment boxes grouping steps
//
// APPLIED FROM HEAD, so publishing is not what makes it take effect:
//
//	Graph.Disabled   the pause switch. The scheduler, webhook and form
//	                 endpoints all read it off HEAD (see scheduler.go,
//	                 webhook.go, form.go), so pausing is live the moment
//	                 it is saved. Counting it as publishable drift made
//	                 pausing a flow raise a "publish your changes" prompt
//	                 for a change that was already in force.
//
// Everything else counts, deliberately: the comparison is a DeepEqual over
// the graph with the fields above cleared, not an allowlist of the fields
// we thought to check. A field added to Graph or Node therefore defaults to
// "publishing it matters", which is the safe direction — the editor may ask
// to publish something harmless, but it will never stay quiet about a real
// behaviour change it didn't know to look at.
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
