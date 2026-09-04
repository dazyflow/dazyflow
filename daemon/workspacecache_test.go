// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func (a *AutoFSWorkspaces) openCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.open)
}

func (a *AutoFSWorkspaces) isOpen(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.open[key]
	return ok
}

func TestAutoFSWorkspaces_EvictsBeyondCap(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces(t.TempDir())
	a.SetMaxOpen(3)

	for i := range 10 {
		if _, err := a.Open(fmt.Sprintf("t%02d", i), "main"); err != nil {
			t.Fatalf("open t%02d: %v", i, err)
		}
	}
	if got := a.openCount(); got != 3 {
		t.Fatalf("open set = %d, want 3", got)
	}
	// The three most recent survive; the rest were evicted.
	for _, k := range []string{"t07/main", "t08/main", "t09/main"} {
		if !a.isOpen(k) {
			t.Errorf("%s should still be open", k)
		}
	}
	if a.isOpen("t00/main") {
		t.Error("t00/main should have been evicted")
	}
}

func TestAutoFSWorkspaces_ReuseKeepsEntryHot(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces(t.TempDir())
	a.SetMaxOpen(3)

	for i := range 3 {
		if _, err := a.Open(fmt.Sprintf("t%d", i), "main"); err != nil {
			t.Fatal(err)
		}
	}
	// Touch the oldest so it is no longer the eviction candidate.
	if _, err := a.Open("t0", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open("t3", "main"); err != nil {
		t.Fatal(err)
	}
	if !a.isOpen("t0/main") {
		t.Error("recently used t0/main was evicted")
	}
	if a.isOpen("t1/main") {
		t.Error("t1/main was the least-recently-used and should have gone")
	}
}

// Evicting an in-memory store would discard the only copy of that tenant's
// graphs, so memory mode must never evict however the cap is set.
func TestAutoFSWorkspaces_MemoryModeNeverEvicts(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces("")
	a.SetMaxOpen(2)

	for i := range 8 {
		st, err := a.Open(fmt.Sprintf("t%d", i), "main")
		if err != nil {
			t.Fatal(err)
		}
		g := core.Graph{ID: "f", Tenant: fmt.Sprintf("t%d", i), Workspace: "main",
			Nodes: []core.Node{{ID: "a", Module: "noop"}}}
		if _, err := st.Save(g, "u"); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	if got := a.openCount(); got != 8 {
		t.Fatalf("memory mode open set = %d, want all 8 retained", got)
	}
	// And every tenant's graph is still readable.
	for i := range 8 {
		st, err := a.Open(fmt.Sprintf("t%d", i), "main")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Load("f"); err != nil {
			t.Fatalf("t%d lost its graph: %v", i, err)
		}
	}
}

// A sweep is the case the cap exists for: it must not leave one store resident
// per tenant, and it must still visit every one.
func TestAutoFSWorkspaces_AllSweepStaysBounded(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces(t.TempDir())
	a.SetMaxOpen(4)
	for i := range 20 {
		if _, err := a.Open(fmt.Sprintf("t%02d", i), "main"); err != nil {
			t.Fatal(err)
		}
	}

	seen := 0
	for range a.All() {
		seen++
		if got := a.openCount(); got > 4 {
			t.Fatalf("open set grew to %d during the sweep, want <= 4", got)
		}
	}
	if seen != 20 {
		t.Fatalf("sweep visited %d workspaces, want 20", seen)
	}
	if got := a.openCount(); got != 4 {
		t.Fatalf("after sweep = %d open, want 4", got)
	}
}

// Eviction lets two Store values exist for one directory. They must serialize
// against each other, or concurrent worktree writes corrupt .git/index.
func TestAutoFSWorkspaces_EvictedAndReopenedStoresShareTheirLock(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces(t.TempDir())
	a.SetMaxOpen(1)

	first, err := a.Open("acme", "main")
	if err != nil {
		t.Fatal(err)
	}
	// Push it out, then take a second store for the same directory.
	if _, err := a.Open("other", "main"); err != nil {
		t.Fatal(err)
	}
	if a.isOpen("acme/main") {
		t.Fatal("acme/main should have been evicted")
	}
	second, err := a.Open("acme", "main")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected a distinct Store after eviction")
	}

	// Hammer both at once. Under -race this fails loudly if they don't share
	// a lock; without it, a corrupted index surfaces as a save error.
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			g := core.Graph{ID: fmt.Sprintf("a%d", i), Tenant: "acme", Workspace: "main",
				Nodes: []core.Node{{ID: "n", Module: "noop"}}}
			if _, err := first.Save(g, "u"); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			g := core.Graph{ID: fmt.Sprintf("b%d", i), Tenant: "acme", Workspace: "main",
				Nodes: []core.Node{{ID: "n", Module: "noop"}}}
			if _, err := second.Save(g, "u"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save across two stores for one directory: %v", err)
	}

	// Every graph both stores wrote is readable through either.
	ids, err := second.ListGraphs()
	if err != nil {
		t.Fatalf("ListGraphs: %v", err)
	}
	if len(ids) != 40 {
		t.Fatalf("ListGraphs = %d graphs, want 40", len(ids))
	}
}

// All is a range-over-func now, so a caller that breaks out mid-sweep stops the
// iterator rather than running it to completion — and leaves it usable.
func TestAutoFSWorkspaces_AllStopsOnBreak(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces(t.TempDir())
	for i := range 10 {
		if _, err := a.Open(fmt.Sprintf("t%02d", i), "main"); err != nil {
			t.Fatal(err)
		}
	}

	seen := 0
	for key := range a.All() {
		seen++
		if key == "" {
			t.Fatal("empty key yielded")
		}
		if seen == 3 {
			break
		}
	}
	if seen != 3 {
		t.Fatalf("break stopped after %d yields, want 3", seen)
	}

	// A second, complete pass still sees everything.
	total := 0
	for range a.All() {
		total++
	}
	if total != 10 {
		t.Fatalf("second sweep saw %d, want 10", total)
	}
}

// Memory mode takes a different branch through All (a snapshot of the open
// set), so it needs the same guarantee.
func TestAutoFSWorkspaces_AllStopsOnBreakInMemoryMode(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces("")
	for i := range 6 {
		if _, err := a.Open(fmt.Sprintf("t%d", i), "main"); err != nil {
			t.Fatal(err)
		}
	}
	seen := 0
	for range a.All() {
		seen++
		if seen == 2 {
			break
		}
	}
	if seen != 2 {
		t.Fatalf("break stopped after %d yields, want 2", seen)
	}
	total := 0
	for range a.All() {
		total++
	}
	if total != 6 {
		t.Fatalf("second sweep saw %d, want 6", total)
	}
}

// The snapshot in memory mode exists so a consumer can Open during iteration
// without deadlocking on the registry lock.
func TestAutoFSWorkspaces_AllToleratesOpenDuringIteration(t *testing.T) {
	t.Parallel()
	a := NewAutoFSWorkspaces("")
	for i := range 3 {
		if _, err := a.Open(fmt.Sprintf("t%d", i), "main"); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range a.All() {
			if _, err := a.Open("during", "main"); err != nil {
				t.Errorf("Open during iteration: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Open during All iteration deadlocked")
	}
}
