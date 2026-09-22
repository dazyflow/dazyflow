// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"container/heap"
	"errors"
	"sort"
)

var ErrCycle = errors.New("graph contains a cycle")

func TopologicalOrder(g Graph) ([]string, error) {
	indeg, succ := buildAdjacency(g)
	order := make([]string, 0, len(g.Nodes))

	// A min-heap keeps the lexicographic tie-break without re-sorting the ready
	// set every step.
	ready := stringHeap(readyNodes(indeg))
	heap.Init(&ready)
	for ready.Len() > 0 {
		next := heap.Pop(&ready).(string)
		order = append(order, next)
		for _, s := range succ[next] {
			indeg[s]--
			if indeg[s] == 0 {
				heap.Push(&ready, s)
			}
		}
	}

	// Against the UNIQUE ids: a duplicate node ID is its own validation error and
	// must not also surface as a phantom cycle.
	if len(order) != len(indeg) {
		return nil, ErrCycle
	}
	return order, nil
}

type stringHeap []string

func (h stringHeap) Len() int           { return len(h) }
func (h stringHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h stringHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *stringHeap) Push(x any)        { *h = append(*h, x.(string)) }
func (h *stringHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

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

	if processed != len(indeg) {
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
