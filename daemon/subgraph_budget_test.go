// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import "testing"

// TestSubtreeBudget_CapsFanOut guards the subgraph fan-out fix: a single root
// run-tree may spawn at most maxSubgraphRunsPerRoot descendant runs, after
// which charge() refuses — turning an exponential N^depth blow-up into a clean
// error instead of a job-store flood.
func TestSubtreeBudget_CapsFanOut(t *testing.T) {
	b := newSubtreeBudget()
	for i := 0; i < maxSubgraphRunsPerRoot; i++ {
		if !b.charge("root-A") {
			t.Fatalf("charge #%d for root-A was refused; want allowed up to %d", i+1, maxSubgraphRunsPerRoot)
		}
	}
	if b.charge("root-A") {
		t.Fatalf("charge beyond %d for root-A was allowed; want refused", maxSubgraphRunsPerRoot)
	}
}

// TestSubtreeBudget_PerRootIsolation: distinct trigger trees get independent
// allowances (each top-level trigger is its own root).
func TestSubtreeBudget_PerRootIsolation(t *testing.T) {
	b := newSubtreeBudget()
	for i := 0; i < maxSubgraphRunsPerRoot; i++ {
		b.charge("root-A")
	}
	if !b.charge("root-B") {
		t.Fatal("root-B was refused after root-A hit its cap; budgets must be per-root")
	}
}

// TestSubtreeBudget_TopLevelNeverCharged: a top-level run (no subgraph parent,
// root == "") is never charged, so ordinary runs are unaffected.
func TestSubtreeBudget_TopLevelNeverCharged(t *testing.T) {
	b := newSubtreeBudget()
	for i := 0; i < maxSubgraphRunsPerRoot*4; i++ {
		if !b.charge("") {
			t.Fatal("empty root (top-level run) must always be allowed")
		}
	}
}

// TestSubtreeBudget_EvictionBounded: the tracking map never exceeds its cap
// even under a churn of distinct roots far larger than the bound.
func TestSubtreeBudget_EvictionBounded(t *testing.T) {
	b := newSubtreeBudget()
	for i := 0; i < maxSubtreeRootsTracked+5000; i++ {
		b.charge(string(rune(i%256)) + "-" + itoa(i))
	}
	if len(b.counts) > maxSubtreeRootsTracked {
		t.Fatalf("counts map grew to %d, want <= %d", len(b.counts), maxSubtreeRootsTracked)
	}
	if len(b.order) > maxSubtreeRootsTracked {
		t.Fatalf("order slice grew to %d, want <= %d", len(b.order), maxSubtreeRootsTracked)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
