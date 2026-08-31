// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/pollstate"
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

func TestSecretProviderSchemesAndConstructors(t *testing.T) {
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	if es.Scheme() != "secret" {
		t.Errorf("encrypted scheme = %q", es.Scheme())
	}

	aws := NewAwsSecretsProviderForStore(es, 5*time.Second)
	if aws.Scheme() != "aws" {
		t.Errorf("aws scheme = %q", aws.Scheme())
	}
	gcp := NewGcpSecretsProviderForStore(es, 5*time.Second)
	if gcp.Scheme() != "gcp" {
		t.Errorf("gcp scheme = %q", gcp.Scheme())
	}
	vault := NewVaultProviderForStore(es, 5*time.Second)
	if vault.Scheme() != "vault" {
		t.Errorf("vault scheme = %q", vault.Scheme())
	}
}

func TestTruncateForError(t *testing.T) {
	short := []byte("hello")
	if got := truncateForError(short); got != "hello" {
		t.Errorf("short = %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	if got := truncateForError(long); len(got) != 200 {
		t.Errorf("long truncated to %d bytes, want 200", len(got))
	}
}

func TestRunViewHelpers(t *testing.T) {
	if durationMS(nil, nil) != 0 {
		t.Error("durationMS(nil,nil) != 0")
	}
	start := time.Unix(0, 0)
	end := start.Add(1500 * time.Millisecond)
	if got := durationMS(&start, &end); got != 1500 {
		t.Errorf("durationMS = %d, want 1500", got)
	}

	if resultError(nil) != nil {
		t.Error("resultError(nil) != nil")
	}
	je := &core.JobError{Code: "boom", Message: "x"}
	if resultError(&core.Result{Error: je}) != je {
		t.Error("resultError did not return the embedded error")
	}

	// newSSETerminalView maps the terminal event fields.
	v := newSSETerminalView(&TerminalEvent{JobID: "r1", Status: core.JobStatusFailed, Error: je})
	if v.RunID != "r1" || v.Status != core.JobStatusFailed || v.Error != je {
		t.Errorf("sse view = %+v", v)
	}

	// newRunView falls back to EnqueuedAt for duration when StartedAt is nil.
	enq := time.Unix(100, 0)
	fin := enq.Add(2 * time.Second)
	rv := newRunView(core.JobRecord{
		ID: "r2", Tenant: "t", Workspace: "ws", GraphID: "g",
		Status: core.JobStatusSucceeded, EnqueuedAt: enq, FinishedAt: &fin,
	})
	if rv.FlowID != "t/ws/g" || rv.DurationMS != 2000 {
		t.Errorf("run view = %+v", rv)
	}
}
