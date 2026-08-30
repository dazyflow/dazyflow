// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"regexp"

	"git.sr.ht/~klahr/dazyflow/core"
)

// template_refs.go makes ${upstream.…} and ${trigger.…} mean what they say:
// "that node in this run", rather than "that node, but only if you wired it
// straight into this one".
//
// fetchPredecessors collects the DIRECT predecessors of a node, which is
// exactly right for assembling its inputs — an input port is fed by an edge or
// it is fed by nothing. Template resolution then reused that same map, and
// inherited a restriction that makes no sense for it. In webhook → if → email,
// the email step's map holds only the if, so:
//
//	${upstream.webhook_input_1.body}  failed the node outright
//	${trigger.body.version}           was left in the text and MAILED OUT
//
// The second is the worse of the two: the reference menu offers that token, a
// lint message suggests it, and the result was a literal "${trigger.body.
// version}" in a customer's inbox with nothing anywhere reporting a problem.
//
// The fix is to look the referenced nodes up in the run. They are there: every
// node's result is a completed job record at NodeJobID(runID, nodeID), and the
// seeded trigger is written as one too. Only what a node's params actually
// mention is fetched, so the common case — no cross-references at all — costs
// nothing, and a flow cannot turn into a full scan of its own run by growing.
//
// The results are added to the SAME map the inputs came from, which is safe
// because every reader of it is a keyed lookup: AssembleInput walks edges and
// looks up prior[edge.From], so an entry for a node with no edge into this one
// is never consulted. (me_routes.go's run inspector already relies on this,
// passing a map of every record in the run to the same function.)

// upstreamRefPattern matches ${upstream.<nodeID>.<port>…}. The node ID stops at
// the dot that begins the port, which the substituter requires — a reference
// with no port is an error there, so there is nothing to prefetch for it.
var upstreamRefPattern = regexp.MustCompile(`\$\{upstream\.([^.}\s]+)\.`)

// triggerRefPattern matches ${trigger.…} in any of its forms.
var triggerRefPattern = regexp.MustCompile(`\$\{trigger[.}]`)

// templateRefs reports which other nodes this node's params name.
//
// Reads the params off the GRAPH rather than the job record: the graph holds
// the template as written, which is the only place the reference is still
// visible. By the time a job's params are resolved the answer is gone.
func templateRefs(graph core.Graph, nodeID string) (nodes []string, wantsTrigger bool) {
	var params map[string]any
	for _, n := range graph.Nodes {
		if n.ID == nodeID {
			params = n.Params
			break
		}
	}
	if len(params) == 0 {
		return nil, false
	}
	// Marshalling is how every string in a params tree gets looked at without
	// a bespoke walker for maps, slices and the arbitrary JSON a drop may
	// carry. A params value that cannot be marshalled has no templates in it
	// either, so failing here means "nothing to prefetch", not an error.
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	for _, m := range upstreamRefPattern.FindAllSubmatch(raw, -1) {
		id := string(m[1])
		if id != "" && !seen[id] {
			seen[id] = true
			nodes = append(nodes, id)
		}
	}
	return nodes, triggerRefPattern.Match(raw)
}

// addTemplateResults fills in the results of nodes this node's params reference
// but no edge connects, so ${upstream.…} and ${trigger.…} can resolve against
// them. Mutates prior in place; entries already there (the real predecessors)
// are never overwritten.
//
// Best-effort by design: a reference to a node that has not run, or has no
// record, is left absent so the substituter reports it. Silently resolving to
// nothing is the failure this whole file exists to end.
func (w *Worker) addTemplateResults(ctx context.Context, graph core.Graph, rec core.JobRecord, prior map[string]core.Result) {
	refs, wantsTrigger := templateRefs(graph, rec.NodeID)
	if wantsTrigger {
		// Which trigger FIRED is decided by which one has a result, and that
		// is a question only the run can answer — a graph may carry both a
		// webhook and a schedule. Offer every trigger node; the substituter
		// picks the one that ran.
		for _, n := range graph.Nodes {
			if core.IsTriggerModule(n.Module) {
				refs = append(refs, n.ID)
			}
		}
	}
	for _, id := range refs {
		if id == rec.NodeID {
			continue // a node cannot reference its own not-yet-existing result
		}
		if _, have := prior[id]; have {
			continue
		}
		predRec, err := w.store.Get(ctx, NodeJobID(rec.GraphRunID, id))
		if err != nil || predRec.Result == nil {
			continue
		}
		if predRec.Status != core.JobStatusSucceeded && predRec.Status != core.JobStatusAwaiting {
			continue
		}
		prior[id] = *predRec.Result
	}
}
