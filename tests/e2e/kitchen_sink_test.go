// Package e2e — exercises multiple policy features in a single graph and
// verifies they cohere. Each subtest builds an isolated harness; failures
// in one don't leak state into others.
package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	dzio "git.sr.ht/~klahr/dazyflow/drops/io"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// fullStack wires every production-relevant piece together: auth + Git
// workspace + JobStore + Engine + sandbox + quota + workers + a custom
// flaky module so we can drive retry behaviour deterministically.
type fullStack struct {
	svc          *daemon.Service
	jobs         core.JobStore
	bus          *daemon.MemoryBus
	sandbox      *daemon.FSSandbox
	quota        *daemon.FSQuota
	flakyCalls   *atomic.Int32
	failuresLeft *atomic.Int32
	principal    core.Principal
}

func newFullStack(t *testing.T, quotaBytes int64) *fullStack {
	t.Helper()
	base := t.TempDir()
	sandbox, err := daemon.NewFSSandbox(base)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	quota, err := daemon.NewFSQuota(base, map[string]int64{"acme": quotaBytes})
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	quota.SetCacheTTL(0)
	// Wire the atomic quota reserver the SAME way production does
	// (cmd/dzd/main.go: io.SetQuotaReserver). Without it, file_write falls back
	// to the per-job QuotaUsed snapshot, which two concurrent same-tenant writes
	// can both pass before either commits — the exact TOCTOU the reservation
	// closes. It's a process global, so clear it on cleanup (e2e tests run
	// serially within the package).
	dzio.SetQuotaReserver(quota.Reserve)
	t.Cleanup(func() { dzio.SetQuotaReserver(nil) })

	flakyCalls := &atomic.Int32{}
	failuresLeft := &atomic.Int32{}
	failuresLeft.Store(2) // module fails 2× then succeeds

	reg := engine.NewRegistry()
	// "flaky" — uses exponential backoff retry policy.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "flaky",
			Version:        "1.0",
			Summary:        "Test fixture flaky.",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs:        []core.Port{{Port: "out"}},
			Idempotent:     true,
			RetryPolicy:    core.RetryExponentialBackoff,
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			flakyCalls.Add(1)
			if failuresLeft.Add(-1) >= 0 {
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusError,
					Error:  &core.JobError{Code: "transient", Message: "still warming up"},
				}, nil
			}
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: "from-flaky"}},
			}, nil
		},
	})
	// "explode" — always fails; used as a fallback target's primary.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "explode",
			Version:        "1.0",
			Summary:        "Test fixture explode.",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs:        []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusError,
				Error:  &core.JobError{Code: "boom", Message: "intentional"},
			}, nil
		},
	})
	// Bring in sleep, file_read, file_write from the global registry.
	for id, m := range engine.Default.Manifests() {
		nt, _ := engine.Default.Get(id)
		nativeT := nt
		_ = reg.Register(engine.NativeDrop{
			Manifest: m,
			Execute: func(ctx context.Context, j core.Job, p chan<- core.Progress) (core.Result, error) {
				return nativeT.Execute(ctx, j, p)
			},
		})
	}

	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: reg},
		Sandbox:  sandbox,
		Quota:    quota,
	}

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "acme", Workspace: "ws1", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// 3 workers so node-level parallelism actually happens.
	for i := 0; i < 3; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID:              "w" + string('a'+rune(i)),
			PollInterval:    5 * time.Millisecond,
			LeaseDuration:   2 * time.Second,
			LeaseRenewEvery: 500 * time.Millisecond,
			MaxRetries:      4,
			RetryBackoff:    func(int) time.Duration { return 10 * time.Millisecond },
		}, jobs, eng, bus)
		go func() { _ = w.Run(wctx) }()
	}

	return &fullStack{
		svc: svc, jobs: jobs, bus: bus,
		sandbox: sandbox, quota: quota,
		flakyCalls: flakyCalls, failuresLeft: failuresLeft, principal: p,
	}
}

// waitTerminal polls the job store until id reaches a terminal status and
// returns a TerminalEvent built from the record. Store-polling avoids the
// subscribe-after-finish race a bus subscription has (see waitForFire) — a
// run that finishes before the subscribe lands would otherwise hang the wait.
func waitTerminal(t *testing.T, store core.JobStore, id string) daemon.TerminalEvent {
	t.Helper()
	var ev daemon.TerminalEvent
	waitFor(t, "run "+id+" to reach a terminal status", func() bool {
		rec, err := store.Get(context.Background(), id)
		if err != nil {
			return false
		}
		ev = daemon.TerminalEvent{JobID: id, Status: rec.Status}
		if rec.Result != nil {
			ev.Error = rec.Result.Error
		}
		return core.IsTerminalStatus(rec.Status)
	})
	return ev
}

// TestKitchenSink_AllPoliciesTogether builds a single graph that uses:
//
//   - retry on a flaky node (fails twice → succeeds)
//   - fallback handler for an always-failing node ("explode")
//   - sandbox+quota for file_write nodes
//   - skip edge so a downstream node runs even if a sibling failed
//   - per-node parallel execution across 3 workers
//
// Topology:
//
//	flaky ────────────────┐
//	                       ├─→ merger ─→ file_write (output)
//	explode ──fallback──→ handler ──┘
//	explode ──skip──────→ ignored-leaf
//	(seed file pre-staged in sandbox; file_write reads it via Ref input)
func TestKitchenSink_AllPoliciesTogether(t *testing.T) {
	h := newFullStack(t, 10_000) // 10 KB tenant quota, plenty

	// Seed a source file inside the sandbox so file_write has something
	// to copy under quota's eye.
	root, _ := h.sandbox.Root("acme", "ws1")
	seed := []byte("kitchen-sink data")
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), seed, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	g := core.Graph{
		ID: "kitchen", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "flaky", Module: "flaky"},
			{ID: "explode", Module: "explode"},
			{ID: "handler", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "ignored", Module: "delay", Params: map[string]any{"ms": 5}},
			{ID: "merger", Module: "merge"},
			{ID: "reader", Module: "file_read", Params: map[string]any{"path": "seed.txt"}},
			{ID: "writer", Module: "file_write", Params: map[string]any{"path": "out.txt"}},
		},
		Edges: []core.Edge{
			// flaky → merger; on_error=retry triggers the retry policy
			{From: "flaky", FromPort: "out", To: "merger", ToPort: "items", OnError: core.OnErrorRetry},
			// explode → handler (fallback rescues)
			{From: "explode", FromPort: "out", To: "handler", ToPort: "in", OnError: core.OnErrorFallback},
			// explode → ignored (skip — runs anyway despite failure)
			{From: "explode", FromPort: "out", To: "ignored", ToPort: "in", OnError: core.OnErrorSkip},
			// handler → merger
			{From: "handler", FromPort: "out", To: "merger", ToPort: "items"},
			// reader → writer (sandbox+quota active here)
			{From: "reader", FromPort: "out", To: "writer", ToPort: "in"},
		},
	}

	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitTerminal(t, h.jobs, graphRunID)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q (err=%+v)", terminal.Status, terminal.Error)
	}

	// === Per-node assertions ===

	flaky, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "flaky"))
	if flaky.Status != core.JobStatusSucceeded {
		t.Errorf("flaky.Status = %q, want succeeded", flaky.Status)
	}
	if flaky.Attempt != 3 {
		t.Errorf("flaky.Attempt = %d, want 3 (2 retries + 1 success)", flaky.Attempt)
	}
	if h.flakyCalls.Load() != 3 {
		t.Errorf("flaky module called %d times, want 3", h.flakyCalls.Load())
	}

	explode, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "explode"))
	if explode.Status != core.JobStatusFailed {
		t.Errorf("explode.Status = %q, want failed", explode.Status)
	}

	handler, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "handler"))
	if handler.Status != core.JobStatusSucceeded {
		t.Errorf("handler.Status = %q, want succeeded (fallback activated)", handler.Status)
	}

	ignored, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "ignored"))
	if ignored.Status != core.JobStatusSucceeded {
		t.Errorf("ignored.Status = %q, want succeeded (skip-edge keeps it alive)", ignored.Status)
	}

	merger, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "merger"))
	if merger.Status != core.JobStatusSucceeded {
		t.Errorf("merger.Status = %q, want succeeded", merger.Status)
	}

	writer, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "writer"))
	if writer.Status != core.JobStatusSucceeded {
		t.Errorf("writer.Status = %q, want succeeded", writer.Status)
	}

	// === Filesystem effect ===
	got, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatalf("out.txt missing: %v", err)
	}
	if string(got) != string(seed) {
		t.Errorf("out.txt = %q, want %q", got, seed)
	}

	// === Worker distribution ===
	workers := map[string]struct{}{}
	for _, id := range []string{"flaky", "handler", "ignored", "merger", "reader", "writer"} {
		rec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, id))
		if rec.WorkerID != "" {
			workers[rec.WorkerID] = struct{}{}
		}
	}
	if len(workers) < 2 {
		t.Logf("only %d worker(s) saw nodes; expected ≥2 across %d workers", len(workers), 3)
	}
}

// TestKitchenSink_QuotaCutsOffMidGraph composes a graph where the first
// file_write barely fits but a second one exceeds the tenant quota. The
// downstream of the quota-blocked write should propagate the failure.
func TestKitchenSink_QuotaCutsOffMidGraph(t *testing.T) {
	h := newFullStack(t, 50) // 50 byte budget

	root, _ := h.sandbox.Root("acme", "ws1")
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), make([]byte, 20), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 20 (seed) + 20 (a.txt) = 40; second copy would push to 60 > 50.

	g := core.Graph{
		ID: "quota-cut", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "rd", Module: "file_read", Params: map[string]any{"path": "seed.txt"}},
			{ID: "wr1", Module: "file_write", Params: map[string]any{"path": "a.txt"}},
			{ID: "wr2", Module: "file_write", Params: map[string]any{"path": "b.txt"}},
		},
		Edges: []core.Edge{
			{From: "rd", FromPort: "out", To: "wr1", ToPort: "in"},
			{From: "rd", FromPort: "out", To: "wr2", ToPort: "in"},
		},
	}
	graphRunID, _ := h.svc.SubmitGraph(t.Context(), h.principal, g)
	terminal := waitTerminal(t, h.jobs, graphRunID)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed (second write should hit quota)", terminal.Status)
	}

	// Exactly one of the writes should have succeeded; the other should
	// be quota_exceeded. Which one depends on worker scheduling.
	wr1, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "wr1"))
	wr2, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "wr2"))
	statuses := []string{string(wr1.Status), string(wr2.Status)}
	good := 0
	bad := 0
	for _, s := range statuses {
		switch core.JobStatus(s) {
		case core.JobStatusSucceeded:
			good++
		case core.JobStatusFailed:
			bad++
		}
	}
	if good != 1 || bad != 1 {
		t.Errorf("expected 1 success + 1 fail, got %+v", statuses)
	}
}

// TestKitchenSink_ConcurrentGraphsIsolated submits 5 different graphs in
// parallel and verifies they all complete without cross-contamination of
// job IDs, bus subscriptions, or sandbox state.
func TestKitchenSink_ConcurrentGraphsIsolated(t *testing.T) {
	h := newFullStack(t, 1_000_000)

	const N = 5
	var wg sync.WaitGroup
	results := make([]string, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			g := core.Graph{
				ID:        "concurrent-" + string('a'+rune(i)),
				Tenant:    "acme",
				Workspace: "ws1",
				Nodes: []core.Node{
					{ID: "a", Module: "delay", Params: map[string]any{"ms": 10}},
					{ID: "b", Module: "delay", Params: map[string]any{"ms": 10}},
				},
				Edges: []core.Edge{
					{From: "a", FromPort: "out", To: "b", ToPort: "in"},
				},
			}
			runID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
			if err != nil {
				t.Errorf("Submit %d: %v", i, err)
				return
			}
			terminal := waitTerminal(t, h.jobs, runID)
			results[i] = string(terminal.Status)
		}(i)
	}
	wg.Wait()

	for i, s := range results {
		if s != string(core.JobStatusSucceeded) {
			t.Errorf("graph %d status = %q, want succeeded", i, s)
		}
	}
}

// TestKitchenSink_GraphRecordReflectsOutcome checks that the graph-level
// JobRecord always ends in a sensible terminal state, regardless of how
// the run finished.
func TestKitchenSink_GraphRecordReflectsOutcome(t *testing.T) {
	cases := []struct {
		name      string
		buildFunc func(*fullStack) core.Graph
		want      core.JobStatus
	}{
		{
			name: "succeeds cleanly",
			buildFunc: func(h *fullStack) core.Graph {
				return core.Graph{
					ID: "happy", Tenant: "acme", Workspace: "ws1",
					Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
				}
			},
			want: core.JobStatusSucceeded,
		},
		{
			name: "node fails terminally",
			buildFunc: func(h *fullStack) core.Graph {
				return core.Graph{
					ID: "bad", Tenant: "acme", Workspace: "ws1",
					Nodes: []core.Node{{ID: "a", Module: "explode"}},
				}
			},
			want: core.JobStatusFailed,
		},
		{
			name: "fallback rescues",
			buildFunc: func(h *fullStack) core.Graph {
				return core.Graph{
					ID: "rescued", Tenant: "acme", Workspace: "ws1",
					Nodes: []core.Node{
						{ID: "x", Module: "explode"},
						{ID: "fb", Module: "delay", Params: map[string]any{"ms": 1}},
					},
					Edges: []core.Edge{
						{From: "x", FromPort: "out", To: "fb", ToPort: "in", OnError: core.OnErrorFallback},
					},
				}
			},
			want: core.JobStatusSucceeded,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newFullStack(t, 1_000_000)
			g := c.buildFunc(h)
			runID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
			if err != nil {
				if !strings.Contains(err.Error(), "invalid") {
					t.Fatalf("Submit: %v", err)
				}
				return
			}
			_ = waitTerminal(t, h.jobs, runID)
			rec, _ := h.jobs.Get(t.Context(), runID)
			if rec.Status != c.want {
				t.Errorf("graph status = %q, want %q", rec.Status, c.want)
			}
		})
	}
}
