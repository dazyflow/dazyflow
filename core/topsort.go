// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"sort"
)

var ErrCycle = errors.New("graph contains a cycle")

// TopologicalOrder returns a linear node ordering such that for every edge
// from A → B, A appears before B. Ties are broken by node ID for determinism.
// Returns ErrCycle if the graph is cyclic. Edges to or from unknown nodes
// are ignored — call Validate first to surface those.
func TopologicalOrder(g Graph) ([]string, error) {
	indeg, succ := buildAdjacency(g)
	order := make([]string, 0, len(g.Nodes))

	ready := readyNodes(indeg)
	for len(ready) > 0 {
		sort.Strings(ready)
		next := ready[0]
		ready = ready[1:]
		order = append(order, next)
		for _, s := range succ[next] {
			indeg[s]--
			if indeg[s] == 0 {
				ready = append(ready, s)
			}
		}
	}

	if len(order) != len(g.Nodes) {
		return nil, ErrCycle
	}
	return order, nil
}

// ExecutionLayers groups nodes into layers where every node in layer N has
// all its predecessors in layers 0..N-1. Nodes within a layer can execute
// in parallel. Within a layer, IDs are sorted for determinism.
func ExecutionLayers(g Graph) ([][]string, error) {
	indeg, succ := buildAdjacency(g)
	var layers [][]string
	processed := 0

	current := readyNodes(indeg)
	for len(current) > 0 {
		sort.Strings(current)
		layers = append(layers, current)
		processed += len(current)

		var next []string
		for _, n := range current {
			for _, s := range succ[n] {
				indeg[s]--
				if indeg[s] == 0 {
					next = append(next, s)
				}
			}
		}
		current = next
	}

	if processed != len(g.Nodes) {
		return nil, ErrCycle
	}
	return layers, nil
}

func buildAdjacency(g Graph) (indeg map[string]int, succ map[string][]string) {
	indeg = make(map[string]int, len(g.Nodes))
	succ = make(map[string][]string, len(g.Nodes))
	for _, n := range g.Nodes {
		indeg[n.ID] = 0
	}
	for _, e := range g.Edges {
		if _, ok := indeg[e.From]; !ok {
			continue
		}
		if _, ok := indeg[e.To]; !ok {
			continue
		}
		succ[e.From] = append(succ[e.From], e.To)
		indeg[e.To]++
	}
	return indeg, succ
}

func readyNodes(indeg map[string]int) []string {
	var ready []string
	for id, d := range indeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	return ready
}
