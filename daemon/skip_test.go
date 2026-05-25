package daemon_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/integrations" // register sleep/merge
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// alwaysFailManifest is non-idempotent + no retry so a failure is terminal
// for that node — useful for isolating skip behaviour from retry behaviour.
var alwaysFailManifest = core.Manifest{
	ID:             "boom",
	Version:        "1.0",
	ExecutionModel: core.ExecutionBatch,
	ProcessModel:   core.ProcessLongLived,
	Inputs:         []core.Port{{Port: "in"}},
	Outputs:        []core.Port{{Port: "out"}},
}

// skipHarness adds the bomb module alongside the built-in registry so
// tests can compose graphs of "boom" + sleep + merge.
type skipHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       *daemon.MemoryBus
	principal core.Principal
	executed  *atomic.Int32 // counts boom invocations for assertions
}

func newSkipHarness(t *testing.T) *skipHarness {
	t.Helper()

	executed := &atomic.Int32{}
	reg := engine.NewRegistry()
	// Boom — always errors.
	_ = reg.Register(engine.NativeDrop{
		Manifest: alwaysFailManifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			executed.Add(1)
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusError,
				Error:  &core.JobError{Code: "boom", Message: "always fails"},
			}, nil
		},
	})
	// Source — always emits a constant ref on its "out" port. Needed
	// because sleep without an input produces no output, which would
	// confuse merge-style downstream tests.
	_ = reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "source",
			Version:        "1.0",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs:        []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{
				JobID:  job.ID,
				Status: core.StatusOK,
				Output: map[string]core.Ref{"out": {Ref: "constant-" + job.NodeID}},
			}, nil
		},
	})
	// Bring in sleep + merge from the global default registry.
	for id, mf := range engine.Default.Manifests() {
		mf := mf
		nativeT, _ := engine.Default.Get(id)
		nt := nativeT
		_ = reg.Register(engine.NativeDrop{
			Manifest: mf,
			Execute: func(ctx context.Context, j core.Job, p chan<- core.Progress) (core.Result, error) {
				return nt.Execute(ctx, j, p)
			},
		})
	}

	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}

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
		Engine:     eng,
		Bus:        bus,
	}

	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:           "w",
		PollInterval: 5 * time.Millisecond,
		MaxRetries:   1, // disable retry so we test skip in isolation
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	return &skipHarness{svc: svc, jobs: jobs, bus: bus, principal: p, executed: executed}
}

func TestSkip_FailureDoesNotPropagateThroughSkipEdge(t *testing.T) {
	h := newSkipHarness(t)

	// boom (fails) →[skip]→ sleep (no other deps, runs with empty input)
	g := core.Graph{
		ID: "skip-single", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "boom"},
			{ID: "dst", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "dst", ToPort: "in", OnError: core.OnErrorSkip},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q, want succeeded (err=%+v)", terminal.Status, terminal.Error)
	}

	srcRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "src"))
	if srcRec.Status != core.JobStatusFailed {
		t.Errorf("src status = %q, want failed", srcRec.Status)
	}
	dstRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "dst"))
	if dstRec.Status != core.JobStatusSucceeded {
		t.Errorf("dst status = %q, want succeeded (should have run despite src failure)", dstRec.Status)
	}
}

func TestSkip_AbortEdgeStillPropagatesEvenWithSkipSibling(t *testing.T) {
	h := newSkipHarness(t)

	// boom (fails) has TWO outgoing edges:
	//   - skip-edge to "skipped" sleep
	//   - default abort-edge to "aborted" sleep
	// Failure should propagate (abort wins) and graph fails. Neither
	// downstream node should run.
	g := core.Graph{
		ID: "skip-mixed", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "src", Module: "boom"},
			{ID: "skipped", Module: "sleep", Params: map[string]any{"ms": 5}},
			{ID: "aborted", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "src", FromPort: "out", To: "skipped", ToPort: "in", OnError: core.OnErrorSkip},
			{From: "src", FromPort: "out", To: "aborted", ToPort: "in", OnError: core.OnErrorAbort},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("graph status = %q, want failed (abort sibling wins)", terminal.Status)
	}
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "skipped")); err == nil {
		t.Error("'skipped' should not have been enqueued — abort sibling killed the graph")
	}
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "aborted")); err == nil {
		t.Error("'aborted' should not have been enqueued")
	}
}

func TestSkip_LeafFailureStillPropagates(t *testing.T) {
	h := newSkipHarness(t)

	// boom is a leaf — no outgoing edges. Default behaviour for leaves is
	// abort, so the graph fails.
	g := core.Graph{
		ID: "skip-leaf", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "lonely", Module: "boom"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Errorf("leaf-only failed graph status = %q, want failed", terminal.Status)
	}
}

func TestSkip_SurvivingPredecessorReachesNode(t *testing.T) {
	h := newSkipHarness(t)

	// Two predecessors of merge:
	//   - boom failing via skip-edge
	//   - sleep succeeding via default edge
	// merge should run with only sleep's output. Graph succeeds.
	g := core.Graph{
		ID: "skip-multi", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "fail", Module: "boom"},
			{ID: "ok", Module: "source"},
			{ID: "join", Module: "merge"},
		},
		Edges: []core.Edge{
			{From: "fail", FromPort: "out", To: "join", ToPort: "items", OnError: core.OnErrorSkip},
			{From: "ok", FromPort: "out", To: "join", ToPort: "items"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("graph status = %q (err=%+v)", terminal.Status, terminal.Error)
	}

	join, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "join"))
	if join.Status != core.JobStatusSucceeded {
		t.Fatalf("join status = %q", join.Status)
	}
	// merge wrote a list of refs into its "out" port. With one survivor
	// it should be a single-element list.
	if join.Result == nil || join.Result.Output["out"].Inline == nil {
		t.Fatal("merge output missing inline list")
	}
	refs, ok := join.Result.Output["out"].Inline.([]core.Ref)
	if !ok {
		t.Fatalf("merge inline is %T, want []core.Ref", join.Result.Output["out"].Inline)
	}
	if len(refs) != 1 {
		t.Errorf("merge collected %d refs, want 1 (failed pred should not contribute)", len(refs))
	}
}

func TestSkip_ChainOfSkips_AllRun(t *testing.T) {
	h := newSkipHarness(t)

	// boom(skip)→sleep1(skip)→sleep2
	// Both downstream sleeps should run; graph succeeds.
	g := core.Graph{
		ID: "skip-chain", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "boom", Module: "boom"},
			{ID: "s1", Module: "sleep", Params: map[string]any{"ms": 5}},
			{ID: "s2", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "boom", FromPort: "out", To: "s1", ToPort: "in", OnError: core.OnErrorSkip},
			{From: "s1", FromPort: "out", To: "s2", ToPort: "in"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q", terminal.Status)
	}
	for _, id := range []string{"s1", "s2"} {
		rec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, id))
		if rec.Status != core.JobStatusSucceeded {
			t.Errorf("%s status = %q", id, rec.Status)
		}
	}
}
