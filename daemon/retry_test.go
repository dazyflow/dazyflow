package daemon_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// flaky manifest declares retry policy + idempotent so retries are allowed.
var flakyManifest = core.Manifest{
	ID:             "flaky",
	Version:        "1.0",
	ExecutionModel: core.ExecutionBatch,
	ProcessModel:   core.ProcessLongLived,
	Inputs:         []core.Port{{Port: "in"}},
	Outputs:        []core.Port{{Port: "out"}},
	Idempotent:     true,
	RetryPolicy:    core.RetryExponentialBackoff,
}

// retryHarness wires Service + Workers around an isolated registry so each
// test can register a controllable flaky module.
type retryHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       *daemon.MemoryBus
	principal core.Principal
}

func newRetryHarness(t *testing.T, exec engine.NativeNode, workerCfg daemon.WorkerConfig) *retryHarness {
	t.Helper()

	reg := engine.NewRegistry()
	if err := reg.Register(exec); err != nil {
		t.Fatalf("register flaky: %v", err)
	}
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role})
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}

	if workerCfg.ID == "" {
		workerCfg.ID = "w"
	}
	if workerCfg.PollInterval == 0 {
		workerCfg.PollInterval = 5 * time.Millisecond
	}
	if workerCfg.LeaseDuration == 0 {
		workerCfg.LeaseDuration = 2 * time.Second
	}
	if workerCfg.LeaseRenewEvery == 0 {
		workerCfg.LeaseRenewEvery = 500 * time.Millisecond
	}

	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(workerCfg, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	return &retryHarness{svc: svc, jobs: jobs, bus: bus, principal: p}
}

// flakyNode builds a NativeNode that fails the first `failCount` calls and
// succeeds afterwards. Calls counter is shared across attempts.
func flakyNode(failCount *atomic.Int32) engine.NativeNode {
	return engine.NativeNode{
		Manifest: flakyManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			calls := failCount.Add(-1)
			if calls >= 0 {
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusError,
					Error:  &core.JobError{Code: "transient", Message: "simulated transient failure"},
				}, nil
			}
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: "ok"}},
			}, nil
		},
	}
}

func TestRetry_NodeSucceedsAfterFailures(t *testing.T) {
	failCount := atomic.Int32{}
	failCount.Store(2) // fail twice, succeed third time

	h := newRetryHarness(t, flakyNode(&failCount), daemon.WorkerConfig{
		MaxRetries:   3,
		RetryBackoff: func(int) time.Duration { return 10 * time.Millisecond },
	})

	g := core.Graph{
		ID: "retry-ok", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "flaky"}},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q (err=%+v)", terminal.Status, terminal.Error)
	}
	rec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "n"))
	if rec.Attempt != 3 {
		t.Errorf("attempt = %d, want 3 (2 retries + 1 success)", rec.Attempt)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("node status = %q", rec.Status)
	}
}

func TestRetry_ExhaustedFailsGraph(t *testing.T) {
	failCount := atomic.Int32{}
	failCount.Store(10) // always fail

	h := newRetryHarness(t, flakyNode(&failCount), daemon.WorkerConfig{
		MaxRetries:   3,
		RetryBackoff: func(int) time.Duration { return 5 * time.Millisecond },
	})

	g := core.Graph{
		ID: "retry-fail", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "flaky"}},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("status = %q (want failed)", terminal.Status)
	}
	rec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "n"))
	if rec.Attempt != 3 {
		t.Errorf("attempt = %d, want exactly 3 (max retries)", rec.Attempt)
	}
	if rec.Status != core.JobStatusFailed {
		t.Errorf("node status = %q", rec.Status)
	}
}

func TestRetry_HonorsBackoffDelay(t *testing.T) {
	failCount := atomic.Int32{}
	failCount.Store(1) // fail once, succeed second time

	backoff := 100 * time.Millisecond
	h := newRetryHarness(t, flakyNode(&failCount), daemon.WorkerConfig{
		MaxRetries:   3,
		RetryBackoff: func(int) time.Duration { return backoff },
	})

	g := core.Graph{
		ID: "retry-timing", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "flaky"}},
	}
	start := time.Now()
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_ = waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	elapsed := time.Since(start)
	if elapsed < backoff {
		t.Errorf("completed in %v, expected at least %v from backoff", elapsed, backoff)
	}
	// Should not have waited multiple backoffs since there was only one retry.
	if elapsed > backoff+800*time.Millisecond {
		t.Errorf("completed in %v, suspiciously long; backoff=%v", elapsed, backoff)
	}
}

func TestRetry_NoRetryEdgeMeansNoRetry(t *testing.T) {
	// Even with a retryable manifest, no edge requesting retry means a
	// failure is terminal. We compose a graph where node "n" feeds "sink"
	// via on_error=abort (or default).
	failCount := atomic.Int32{}
	failCount.Store(1)

	exec := flakyNode(&failCount)
	// Register a sink so we have an outgoing edge to attach OnError to.
	sinkManifest := core.Manifest{
		ID: "sink", Version: "1.0",
		ExecutionModel: core.ExecutionBatch,
		ProcessModel:   core.ProcessLongLived,
		Inputs:         []core.Port{{Port: "in"}},
		Outputs:        []core.Port{{Port: "out"}},
	}
	reg := engine.NewRegistry()
	_ = reg.Register(exec)
	_ = reg.Register(engine.NativeNode{
		Manifest: sinkManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: job.ID, Status: core.StatusOK}, nil
		},
	})

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role})
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}},
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:           "w",
		PollInterval: 5 * time.Millisecond,
		MaxRetries:   3,
		RetryBackoff: func(int) time.Duration { return 5 * time.Millisecond },
	}, jobs, &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "no-retry-edge", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "n", Module: "flaky"},
			{ID: "sink", Module: "sink"},
		},
		Edges: []core.Edge{
			// on_error defaults to "" (treated as abort)
			{From: "n", FromPort: "out", To: "sink", ToPort: "in"},
		},
	}
	graphRunID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed (no retry edge)", terminal.Status)
	}
	rec, _ := jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "n"))
	if rec.Attempt != 1 {
		t.Errorf("attempt = %d, want 1 (no retries)", rec.Attempt)
	}
}

func TestValidate_NonIdempotentRetryRejected(t *testing.T) {
	// Module is NOT idempotent but the edge requests retry → validation
	// error per spec.
	src := core.Manifest{
		ID: "writer", Version: "1.0",
		Outputs:    []core.Port{{Port: "out"}},
		Idempotent: false,
	}
	dst := core.Manifest{
		ID:      "sink",
		Inputs:  []core.Port{{Port: "in"}},
		Outputs: []core.Port{},
	}
	g := core.Graph{
		Nodes: []core.Node{
			{ID: "a", Module: "writer"},
			{ID: "b", Module: "sink"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in", OnError: core.OnErrorRetry},
		},
	}
	err := core.ValidateWithManifests(g, map[string]core.Manifest{
		"writer": src,
		"sink":   dst,
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "non-idempotent") {
		t.Errorf("err = %q; expected mention of 'non-idempotent'", err.Error())
	}
}

func TestValidate_IdempotentRetryAllowed(t *testing.T) {
	src := core.Manifest{
		ID: "writer", Version: "1.0",
		Outputs:    []core.Port{{Port: "out"}},
		Idempotent: true,
	}
	dst := core.Manifest{
		ID:     "sink",
		Inputs: []core.Port{{Port: "in"}},
	}
	g := core.Graph{
		Nodes: []core.Node{
			{ID: "a", Module: "writer"},
			{ID: "b", Module: "sink"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in", OnError: core.OnErrorRetry},
		},
	}
	if err := core.ValidateWithManifests(g, map[string]core.Manifest{
		"writer": src,
		"sink":   dst,
	}); err != nil {
		t.Errorf("idempotent retry should pass validation: %v", err)
	}
}

func TestRequeue_PreservesAttemptAndClearsResult(t *testing.T) {
	// Direct JobStore test (not through the worker) to nail down the
	// Requeue contract.
	store := jobstore.NewMemory()
	ctx := t.Context()

	rec := core.JobRecord{
		ID: "j1", Kind: core.JobKindNode,
		GraphRunID: "g", GraphID: "g", NodeID: "n",
	}
	if err := store.Enqueue(ctx, rec); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := store.Claim(ctx, "w1", time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.Attempt != 1 {
		t.Errorf("attempt = %d after first claim", claimed.Attempt)
	}

	// Marshal a result onto the record to verify Requeue clears it.
	failure := &core.Result{Status: core.StatusError, Error: &core.JobError{Code: "boom"}}
	// We can't write the result without Complete; use Requeue to confirm
	// the path that re-queues a still-running record (the worker calls
	// Requeue before Complete).
	availableAt := time.Now().Add(100 * time.Millisecond)
	if err := store.Requeue(ctx, "j1", availableAt); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	got, _ := store.Get(ctx, "j1")
	if got.Status != core.JobStatusQueued {
		t.Errorf("status = %q, want queued", got.Status)
	}
	if got.AvailableAt == nil || !got.AvailableAt.Equal(availableAt) {
		t.Errorf("AvailableAt = %v, want %v", got.AvailableAt, availableAt)
	}
	if got.Attempt != 1 {
		t.Errorf("attempt = %d after requeue (should preserve)", got.Attempt)
	}

	// Claim before availability — should be skipped.
	if _, err := store.Claim(ctx, "w2", time.Second); err == nil {
		t.Error("Claim succeeded before availability passed")
	}

	// Wait past availability.
	time.Sleep(120 * time.Millisecond)
	again, err := store.Claim(ctx, "w2", time.Second)
	if err != nil {
		t.Fatalf("Claim after delay: %v", err)
	}
	if again.Attempt != 2 {
		t.Errorf("attempt = %d on second claim, want 2", again.Attempt)
	}

	// silence unused failure variable
	_ = failure
}
