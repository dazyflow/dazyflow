package daemon

import (
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
)

// loopBodyOwners maps each loop-body node ID → the for_each node that owns it.
// A node is a loop body when it's reachable from a for_each's "body" output
// pin (an edge with From = the for_each node and FromPort = "body");
// ownership extends transitively along edges leaving the entry node. Loop-body
// nodes are excluded from the normal dispatcher — they run only via their
// for_each, once per item — so this is the single source of truth both the
// seed path and the live dispatcher consult.
//
// v1 limitation: the body is "the entry node and everything downstream of it",
// stopping only at the loop node itself. Don't wire a loop body back into the
// main flow; a node reachable from both the body and the main flow is treated
// as loop-owned (and thus excluded from normal dispatch).
func loopBodyOwners(graph core.Graph) map[string]string {
	module := make(map[string]string, len(graph.Nodes))
	for _, n := range graph.Nodes {
		module[n.ID] = n.Module
	}
	outEdges := make(map[string][]string)
	for _, e := range graph.Edges {
		outEdges[e.From] = append(outEdges[e.From], e.To)
	}
	owners := map[string]string{}
	for _, e := range graph.Edges {
		if e.FromPort != "body" || module[e.From] != "for_each" {
			continue
		}
		forEach := e.From
		stack := []string{e.To}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			// Never fold the loop node itself into its body (a body chain that
			// loops back), and don't revisit an already-owned node.
			if n == forEach {
				continue
			}
			if _, seen := owners[n]; seen {
				continue
			}
			owners[n] = forEach
			stack = append(stack, outEdges[n]...)
		}
	}
	return owners
}

// isLoopBody reports whether nodeID is owned by some for_each loop body in
// the graph. Thin wrapper for dispatch sites that only need a yes/no.
func isLoopBody(graph core.Graph, nodeID string) bool {
	_, ok := loopBodyOwners(graph)[nodeID]
	return ok
}

// validateLoopBodies rejects loop-body configurations the engine can't run
// correctly yet, so a bad graph fails clearly at submit time instead of
// producing silently-wrong output. The single v1 rule: a for_each may not
// sit inside another for_each's body — the body subgraph runs in-process via
// Engine.Run, which has no per-item fan-out, so a nested loop would run once
// rather than once per inner item. The frontend flags the same case
// (nodeCard.loopNested); this is the server-side safety net for API callers.
func validateLoopBodies(g core.Graph) error {
	owners := loopBodyOwners(g)
	for _, n := range g.Nodes {
		if n.Module != "for_each" {
			continue
		}
		if outer, nested := owners[n.ID]; nested {
			return fmt.Errorf("for_each %q runs inside the body of for_each %q: nested loops aren't supported yet", n.ID, outer)
		}
	}
	return nil
}

// extractLoopBody builds the standalone subgraph that a for_each runs once
// per item: every node owned by forEachID, plus the edges that run *between*
// those body nodes. The for_each's own "body" pin edge is dropped — in the
// extracted subgraph the body entry node has no incoming edge, so it's a
// root and runs with empty input (its params reference ${item.…} for the
// row). Edges crossing the body boundary (into or out of the body from the
// main flow) are dropped too; v1 body nodes draw their per-row data from
// ${item.…} and from other body nodes only.
//
// The returned graph inherits the parent's ID/Tenant/Workspace so secrets,
// connection defaults, and resource resolution scope identically to the
// main run. ok=false means this node owns no body (legacy/unwired for_each).
func extractLoopBody(graph core.Graph, forEachID string) (core.Graph, bool) {
	owners := loopBodyOwners(graph)
	bodyIDs := map[string]struct{}{}
	for nodeID, owner := range owners {
		if owner == forEachID {
			bodyIDs[nodeID] = struct{}{}
		}
	}
	if len(bodyIDs) == 0 {
		return core.Graph{}, false
	}

	// Disabled switch inside the body: a disabled node is pruned, and so is
	// anything reachable ONLY through it — mirroring the main flow's skip
	// cascade ("off prunes the branch"). Reachability is recomputed over the
	// body with disabled nodes removed: keep the entry nodes (targets of the
	// for_each's `body` pin) that aren't disabled, plus everything they
	// still reach. A body whose every node is pruned stays ok=true with
	// zero nodes — the for_each then runs an empty pass per item rather
	// than falling back to legacy mode.
	disabled := map[string]bool{}
	for _, n := range graph.Nodes {
		disabled[n.ID] = n.Disabled
	}
	outEdges := map[string][]string{}
	for _, e := range graph.Edges {
		_, fromBody := bodyIDs[e.From]
		_, toBody := bodyIDs[e.To]
		if fromBody && toBody {
			outEdges[e.From] = append(outEdges[e.From], e.To)
		}
	}
	kept := map[string]struct{}{}
	var stack []string
	for _, e := range graph.Edges {
		if e.FromPort == "body" && e.From == forEachID && !disabled[e.To] {
			stack = append(stack, e.To)
		}
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, seen := kept[n]; seen || disabled[n] {
			continue
		}
		if _, inBody := bodyIDs[n]; !inBody {
			continue
		}
		kept[n] = struct{}{}
		stack = append(stack, outEdges[n]...)
	}

	sub := core.Graph{
		ID:        graph.ID,
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
	}
	for _, n := range graph.Nodes {
		if _, ok := kept[n.ID]; ok {
			sub.Nodes = append(sub.Nodes, n)
		}
	}
	for _, e := range graph.Edges {
		_, fromKept := kept[e.From]
		_, toKept := kept[e.To]
		if fromKept && toKept {
			sub.Edges = append(sub.Edges, e)
		}
	}
	return sub, true
}
