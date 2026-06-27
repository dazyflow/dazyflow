// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/pollstate"
)

// TestPauseRegistry_Cov exercises the in-memory breakpoint pause registry:
// addPaused (with dedup), takePaused (returns + clears), setStepping/isStepping,
// clear, and shouldPauseAfter's three legs.
func TestPauseRegistry_Cov(t *testing.T) {
	r := &pauseRegistry{paused: map[string][]string{}, stepping: map[string]bool{}}

	r.addPaused("run", "a")
	r.addPaused("run", "a") // dedup
	r.addPaused("run", "b")
	if got := r.takePaused("run"); len(got) != 2 {
		t.Fatalf("takePaused = %v, want [a b]", got)
	}
	if got := r.takePaused("run"); len(got) != 0 {
		t.Fatalf("takePaused after take = %v, want empty", got)
	}

	r.setStepping("run", true)
	if !r.isStepping("run") {
		t.Fatal("isStepping = false after setStepping(true)")
	}
	r.setStepping("run", false)
	if r.isStepping("run") {
		t.Fatal("isStepping = true after setStepping(false)")
	}

	r.addPaused("run2", "x")
	r.setStepping("run2", true)
	r.clear("run2")
	if r.isStepping("run2") || len(r.takePaused("run2")) != 0 {
		t.Fatal("clear left state behind")
	}

	// shouldPauseAfter: step mode wins.
	breakpoints.setStepping("sp", true)
	t.Cleanup(func() { breakpoints.clear("sp") })
	g := core.Graph{Nodes: []core.Node{{ID: "n", Breakpoint: true}, {ID: "plain"}}}
	if !shouldPauseAfter(g, "sp", "plain") {
		t.Fatal("step mode should force a pause")
	}
	// No step mode: breakpoint node pauses, plain node does not, unknown node no.
	if !shouldPauseAfter(g, "other", "n") {
		t.Fatal("breakpoint node should pause")
	}
	if shouldPauseAfter(g, "other", "plain") {
		t.Fatal("plain node should not pause")
	}
	if shouldPauseAfter(g, "other", "ghost") {
		t.Fatal("unknown node should not pause")
	}
}

// TestAutoFSWorkspaces_MemoryEnumeration covers AutoFSWorkspaces.List and All
// in memory mode (empty base): stores opened this process are enumerated.
func TestAutoFSWorkspaces_MemoryEnumeration(t *testing.T) {
	a := NewAutoFSWorkspaces("") // memory mode

	// Open two workspaces for one tenant and one for another.
	if _, err := a.Open("acme", "main"); err != nil {
		t.Fatalf("Open acme/main: %v", err)
	}
	if _, err := a.Open("acme", "staging"); err != nil {
		t.Fatalf("Open acme/staging: %v", err)
	}
	if _, err := a.Open("other", "main"); err != nil {
		t.Fatalf("Open other/main: %v", err)
	}

	list, err := a.List("acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0] != "main" || list[1] != "staging" {
		t.Fatalf("List(acme) = %v, want [main staging]", list)
	}

	all := a.All()
	if _, ok := all["acme/main"]; !ok {
		t.Fatalf("All missing acme/main: %v", keys(all))
	}
	if len(all) != 3 {
		t.Fatalf("All = %d entries, want 3", len(all))
	}

	// RemoveTenant evicts a tenant's cached stores (memory mode is a no-op
	// on disk but still clears the cache).
	if err := a.RemoveTenant("acme"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}
	if l, _ := a.List("acme"); len(l) != 0 {
		t.Fatalf("List(acme) after remove = %v, want empty", l)
	}
}

// TestAutoFSWorkspaces_DiskEnumeration covers List/All/RemoveTenant in
// filesystem mode (non-empty base): the on-disk ReadDir branches.
func TestAutoFSWorkspaces_DiskEnumeration(t *testing.T) {
	dir := t.TempDir()
	a := NewAutoFSWorkspaces(dir)

	// Open provisions the on-disk tenant/workspace directories.
	if _, err := a.Open("acme", "main"); err != nil {
		t.Fatalf("Open acme/main: %v", err)
	}
	if _, err := a.Open("acme", "staging"); err != nil {
		t.Fatalf("Open acme/staging: %v", err)
	}
	if _, err := a.Open("other", "main"); err != nil {
		t.Fatalf("Open other/main: %v", err)
	}

	// List reads the tenant's workspace directories from disk.
	list, err := a.List("acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 || list[0] != "main" || list[1] != "staging" {
		t.Fatalf("List(acme) = %v, want [main staging]", list)
	}
	// A tenant with no directory lists empty (not an error).
	if l, err := a.List("nobody"); err != nil || len(l) != 0 {
		t.Fatalf("List(nobody) = %v / %v, want empty", l, err)
	}

	// All walks every tenant/workspace dir on disk.
	all := a.All()
	if len(all) != 3 {
		t.Fatalf("All = %d, want 3 (%v)", len(all), keys(all))
	}

	// RemoveTenant deletes the on-disk subtree.
	if err := a.RemoveTenant("acme"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}
	if l, _ := a.List("acme"); len(l) != 0 {
		t.Fatalf("List(acme) after remove = %v, want empty", l)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBufferedUsage_Cov covers BufferedUsage's passthrough AddSkippedRun, the
// flush-on-shutdown Run loop, and Flush re-queue-on-success.
func TestBufferedUsage_Cov(t *testing.T) {
	inner := NewMemUsageStore()
	b := NewBufferedUsage(inner)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// AddSkippedRun passes straight through.
	if err := b.AddSkippedRun(context.Background(), "acme", now); err != nil {
		t.Fatalf("AddSkippedRun: %v", err)
	}
	// AddRun passes through too.
	_ = b.AddRun(context.Background(), "acme", now)
	// Buffered node executions flush on read.
	_ = b.AddNodeExecutions(context.Background(), "acme", 7, now)

	buckets, _ := b.Usage(context.Background(), "acme", 1)
	if len(buckets) == 0 || buckets[0].NodeExecutions != 7 {
		t.Fatalf("node executions = %+v, want 7", buckets)
	}

	// Run flushes on ctx cancel.
	ctx, cancel := context.WithCancel(context.Background())
	_ = b.AddNodeExecutions(ctx, "acme", 3, now)
	done := make(chan struct{})
	go func() { b.Run(ctx, time.Millisecond); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	buckets, _ = inner.Usage(context.Background(), "acme", 1)
	if len(buckets) == 0 || buckets[0].NodeExecutions != 10 {
		t.Fatalf("after shutdown flush = %+v, want 10", buckets)
	}
}

// TestSchedulerSetters_Cov covers SetLeader and SetPollStateReader (nil-guarded
// installs).
func TestSchedulerSetters_Cov(t *testing.T) {
	s := NewScheduler(&Service{})

	// Nil leader is ignored (default stays).
	s.SetLeader(nil)
	// A real predicate installs.
	called := false
	s.SetLeader(func() bool { called = true; return false })
	if s.leader == nil || s.leader() || !called {
		t.Fatal("SetLeader did not install the predicate")
	}

	s.SetPollStateReader(nil)
	if s.pollState != nil {
		t.Fatal("SetPollStateReader(nil) should leave reader unset")
	}
	s.SetPollStateReader(func(context.Context, string, string) *pollstate.Marker {
		return &pollstate.Marker{Empty: true}
	})
	if s.pollState == nil {
		t.Fatal("SetPollStateReader did not install the reader")
	}
	if m := s.pollState(context.Background(), "t", "g"); m == nil || !m.Empty {
		t.Fatalf("pollState reader = %+v", m)
	}
}

// TestProviderSchemes_Cov covers the trivial Scheme() identity methods on the
// resource + builtin secret providers.
func TestProviderSchemes_Cov(t *testing.T) {
	if (&ResourceProvider{}).Scheme() != "resource" {
		t.Fatal("ResourceProvider.Scheme")
	}
	if NewBuiltinProvider().Scheme() != "builtin" {
		t.Fatal("BuiltinProvider.Scheme")
	}
}
