// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import "sync"

const (
	// maxSubgraphRunsPerRoot caps the TOTAL number of descendant runs a
	// single top-level run may spawn through subgraph nodes. maxSubgraphDepth
	// bounds the DEPTH of nesting but not the BREADTH: a graph with N subgraph
	// nodes whose children each also have N subgraph nodes fans out to
	// ~N^depth runs (N up to MaxGraphNodes). Depth 8 with N=10 is 10^7 runs
	// from one trigger — a job-store/DB flood. This total cap turns a
	// recursive/exponential blow-up into a clean per-node error while staying
	// generous for legitimate fan-out (calling many subflows from one run).
	maxSubgraphRunsPerRoot = 1024

	// maxSubtreeRootsTracked bounds the in-memory counter map — one entry per
	// active root run-tree. Mirrors the rate limiter's bounded-map approach so
	// a churn of distinct roots can't grow it without bound. When full, the
	// oldest-tracked root is evicted (it then simply starts counting afresh,
	// acceptable for a safety backstop under extreme churn).
	maxSubtreeRootsTracked = 50_000
)

// subtreeBudget tracks how many descendant runs each root run-tree has spawned
// through subgraph nodes, so the engine can refuse an exponential fan-out.
// Counts are never decremented: the budget is per-trigger (each new top-level
// run is a fresh root with a fresh allowance), and entries age out via the
// bounded-map eviction below.
type subtreeBudget struct {
	mu     sync.Mutex
	counts map[string]int // rootRunID -> descendant runs charged so far
	order  []string       // insertion order, for cheap oldest-eviction
}

func newSubtreeBudget() *subtreeBudget {
	return &subtreeBudget{counts: make(map[string]int)}
}

// charge records one more descendant run under rootRunID and reports whether it
// stays within maxSubgraphRunsPerRoot. A top-level run with no subgraph parent
// (rootRunID == "") is never charged.
func (b *subtreeBudget) charge(rootRunID string) bool {
	if rootRunID == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.counts == nil {
		b.counts = make(map[string]int)
	}
	n, seen := b.counts[rootRunID]
	if n >= maxSubgraphRunsPerRoot {
		return false
	}
	if !seen {
		if len(b.counts) >= maxSubtreeRootsTracked && len(b.order) > 0 {
			oldest := b.order[0]
			b.order = b.order[1:]
			delete(b.counts, oldest)
		}
		b.order = append(b.order, rootRunID)
	}
	b.counts[rootRunID] = n + 1
	return true
}

// subtreeBudgetInst lazily initializes the per-Service budget so a zero-value
// Service{} (built directly as a struct literal in production and tests) works
// without an explicit constructor.
func (s *Service) subtreeBudgetInst() *subtreeBudget {
	s.subtreeOnce.Do(func() { s.subtreeBudgetV = newSubtreeBudget() })
	return s.subtreeBudgetV
}
