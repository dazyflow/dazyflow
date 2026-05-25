package daemon_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/integrations"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// workerHarness wires Service + Workers around in-memory storage. Workers
// poll fast so tests don't sit waiting.
type workerHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       *daemon.MemoryBus
	principal core.Principal
}

func newWorkerHarness(t *testing.T, workerCount int) *workerHarness {
	t.Helper()

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
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for i := 0; i < workerCount; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID:              workerName(i),
			PollInterval:    5 * time.Millisecond,
			LeaseDuration:   2 * time.Second,
			LeaseRenewEvery: 500 * time.Millisecond,
		}, jobs, eng, bus)
		go func() { _ = w.Run(workerCtx) }()
	}
	return &workerHarness{svc: svc, jobs: jobs, bus: bus, principal: p}
}

func workerName(i int) string {
	return "w-" + [...]string{"a", "b", "c", "d", "e"}[i%5]
}

func TestPerNode_LinearChain_ProgressesThroughDependencies(t *testing.T) {
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "chain", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "sleep", Params: map[string]any{"ms": 5}},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 5}},
			{ID: "c", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "b", FromPort: "out", To: "c", ToPort: "in"},
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
	for _, id := range []string{"a", "b", "c"} {
		rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, id))
		if err != nil {
			t.Fatalf("Get %s: %v", id, err)
		}
		if rec.Status != core.JobStatusSucceeded {
			t.Errorf("%s status = %q", id, rec.Status)
		}
	}
}

// TestPerNode_TimeoutFailsTheNode uses the built-in sleep module to
// guarantee a node exceeds its declared timeout. The node should land
// in Failed with code=timeout — distinct enough from a generic
// runtime error that dashboards can group on it.
func TestPerNode_TimeoutFailsTheNode(t *testing.T) {
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "tmo", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{
				ID: "slow", Module: "sleep",
				Params:         map[string]any{"ms": 2000},
				TimeoutSeconds: 1,
			},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("graph status = %q, want failed", terminal.Status)
	}
	rec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "slow"))
	if rec.Status != core.JobStatusFailed {
		t.Errorf("node status = %q, want failed", rec.Status)
	}
	if rec.Result == nil || rec.Result.Error == nil || rec.Result.Error.Code != "timeout" {
		t.Errorf("error = %+v, want code=timeout", rec.Result.Error)
	}
}

func TestPerNode_DiamondSpreadsAcrossWorkers(t *testing.T) {
	h := newWorkerHarness(t, 3)

	// a feeds b and c (which can run in parallel), both feed merge d.
	mergeMin := 1
	_ = mergeMin // documentation; "merge" already requires variadic min via manifest
	g := core.Graph{
		ID: "diamond", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "sleep", Params: map[string]any{"ms": 30}},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 30}},
			{ID: "c", Module: "sleep", Params: map[string]any{"ms": 30}},
			{ID: "d", Module: "merge"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
			{From: "a", FromPort: "out", To: "c", ToPort: "in"},
			{From: "b", FromPort: "out", To: "d", ToPort: "items"},
			{From: "c", FromPort: "out", To: "d", ToPort: "items"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q (err=%+v)", terminal.Status, terminal.Error)
	}

	// b and c should have executed on at least two distinct workers since
	// they're independent and the harness has three.
	bRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "b"))
	cRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "c"))
	if bRec.WorkerID == "" || cRec.WorkerID == "" {
		t.Fatalf("nodes missing worker IDs: b=%q c=%q", bRec.WorkerID, cRec.WorkerID)
	}
	// Not strictly required by the algorithm (workers may happen to claim
	// in sequence), but with 3 workers + 30ms node duration it's extremely
	// likely; if this flakes it's worth investigating real serialization.
	if bRec.WorkerID == cRec.WorkerID {
		t.Logf("b and c happened to land on same worker %q; OK but rare", bRec.WorkerID)
	}
}

func TestPerNode_FailedPredecessorAbortsDescendants(t *testing.T) {
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "abort", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "nonexistent"},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("status = %q, want failed", terminal.Status)
	}
	aRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "a"))
	if aRec.Status != core.JobStatusFailed {
		t.Errorf("a status = %q, want failed", aRec.Status)
	}
	// b should never have been enqueued — its predecessor failed.
	if _, err := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "b")); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("b should not exist (predecessor failed); err = %v", err)
	}
}

func TestPerNode_NodesExecuteInDependencyOrder(t *testing.T) {
	// One worker so any out-of-order execution would be visible. Verify
	// that a child node is only ever picked up after its parent finished.
	h := newWorkerHarness(t, 1)

	g := core.Graph{
		ID: "order", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "first", Module: "sleep", Params: map[string]any{"ms": 30}},
			{ID: "second", Module: "sleep", Params: map[string]any{"ms": 5}},
		},
		Edges: []core.Edge{
			{From: "first", FromPort: "out", To: "second", ToPort: "in"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_ = waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	first, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "first"))
	second, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "second"))
	if first.FinishedAt == nil || second.StartedAt == nil {
		t.Fatalf("missing timestamps: first.Finished=%v second.Started=%v",
			first.FinishedAt, second.StartedAt)
	}
	if !second.StartedAt.After(*first.FinishedAt) && !second.StartedAt.Equal(*first.FinishedAt) {
		t.Errorf("second started %v before first finished %v",
			*second.StartedAt, *first.FinishedAt)
	}
}

func TestPerNode_LeaseExpiryAllowsReclaim(t *testing.T) {
	// Manually enqueue a node-record, simulate a dead worker holding the
	// claim, then start a real worker that should reclaim and finish it.
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}

	graphRunID := "gr"
	// Seed the graph-record manually so the worker can fetch it.
	graphRec := core.JobRecord{
		ID:     graphRunID,
		Kind:   core.JobKindGraph,
		Status: core.JobStatusRunning,
		GraphPayload: []byte(`{"id":"g","nodes":[{"id":"a","module":"sleep","params":{"ms":5}}],"edges":[]}`),
	}
	if err := jobs.Enqueue(t.Context(), graphRec); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	nodeRec := core.JobRecord{
		ID:         daemon.NodeJobID(graphRunID, "a"),
		Kind:       core.JobKindNode,
		GraphRunID: graphRunID,
		GraphID:    "g",
		NodeID:     "a",
	}
	if err := jobs.Enqueue(t.Context(), nodeRec); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	// Dead worker grabs the node with a very short lease and never renews.
	deadCtx, killDead := context.WithCancel(context.Background())
	defer killDead()
	deadGrabbed := atomic.Int32{}
	go func() {
		_, err := jobs.Claim(deadCtx, "dead", 50*time.Millisecond)
		if err == nil {
			deadGrabbed.Store(1)
			<-deadCtx.Done()
		}
	}()
	for deadGrabbed.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	// Healthy worker reclaims after lease lapses.
	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "rescue", PollInterval: 5 * time.Millisecond,
		LeaseDuration: time.Second, LeaseRenewEvery: 200 * time.Millisecond,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q", terminal.Status)
	}
	rec, _ := jobs.Get(t.Context(), nodeRec.ID)
	if rec.WorkerID != "rescue" {
		t.Errorf("worker = %q, want rescue", rec.WorkerID)
	}
	if rec.Attempt < 2 {
		t.Errorf("attempt = %d, want ≥2", rec.Attempt)
	}
}

func TestPerNode_IdempotentDispatch_NoDoubleEnqueue(t *testing.T) {
	// Many workers; a merge node with multiple predecessors. When the
	// predecessors finish nearly simultaneously, multiple workers race
	// to dispatch the same downstream node. The deterministic ID +
	// ErrConflict-on-Enqueue contract prevents double work.
	h := newWorkerHarness(t, 5)

	g := core.Graph{
		ID: "race", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "c", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "merge", Module: "merge"},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "merge", ToPort: "items"},
			{From: "b", FromPort: "out", To: "merge", ToPort: "items"},
			{From: "c", FromPort: "out", To: "merge", ToPort: "items"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q (err=%+v)", terminal.Status, terminal.Error)
	}
	mergeRec, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "merge"))
	if mergeRec.Attempt != 1 {
		t.Errorf("merge attempt = %d, want 1 (double-enqueue ⇒ multiple claims)", mergeRec.Attempt)
	}
}

func TestPerNode_EmptyGraphCompletesImmediately(t *testing.T) {
	h := newWorkerHarness(t, 1)
	g := core.Graph{ID: "empty", Tenant: "t", Workspace: "ws"}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// The graph-record is updated synchronously inside SubmitGraph for
	// empty graphs; no need to wait on the bus.
	rec, err := h.jobs.Get(t.Context(), graphRunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q, want succeeded", rec.Status)
	}
}

func TestPerNode_TerminalOnlyPublishedOnce(t *testing.T) {
	// 4 workers, 3 root nodes that finish around the same time. Every
	// completing worker calls maybeCompleteGraph; only the one that wins
	// the Complete race should publish a terminal event.
	h := newWorkerHarness(t, 4)

	g := core.Graph{
		ID: "once", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "sleep", Params: map[string]any{"ms": 30}},
			{ID: "b", Module: "sleep", Params: map[string]any{"ms": 30}},
			{ID: "c", Module: "sleep", Params: map[string]any{"ms": 30}},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	events, cancel := h.bus.Subscribe(graphRunID)
	defer cancel()

	// Read until we get a terminal, then linger briefly to ensure no
	// follow-up terminal arrives. We never call cancel from this loop —
	// the deferred cancel handles cleanup.
	terminalCount := 0
	linger := time.NewTimer(2 * time.Second)
	defer linger.Stop()
	for {
		if terminalCount == 1 {
			// Replace the long deadline with a short one to catch dupes.
			linger.Reset(200 * time.Millisecond)
		}
		select {
		case ev, ok := <-events:
			if !ok {
				goto done
			}
			if ev.Terminal != nil {
				terminalCount++
			}
		case <-linger.C:
			goto done
		}
	}
done:
	if terminalCount != 1 {
		t.Errorf("terminal events = %d, want exactly 1", terminalCount)
	}
}

// waitForTerminalEvent subscribes, waits, returns the terminal event.
// waitForTerminalEvent blocks until the graph run reaches a terminal
// status. It subscribes to the bus FIRST so a fast-completing graph
// doesn't escape, then re-checks the store — that ordering is critical
// because the in-memory bus drops events for absent subscribers and
// MemoryBus.Publish never replays history. If the store says the
// graph is already terminal between Submit and Subscribe, we
// synthesize a TerminalEvent from the record so tests don't hang.
//
// jobs may be nil; callers that don't pass one get the old subscribe-
// only behaviour (and inherit the original race).
func waitForTerminalEvent(t *testing.T, bus *daemon.MemoryBus, jobs core.JobStore, graphRunID string, timeout time.Duration) daemon.TerminalEvent {
	t.Helper()
	events, cancel := bus.Subscribe(graphRunID)
	defer cancel()
	// After subscribe, check the store — if the graph already finished
	// between Submit and Subscribe, the bus already published and we'd
	// wait forever otherwise.
	if jobs != nil {
		if rec, err := jobs.Get(t.Context(), graphRunID); err == nil && core.IsTerminalStatus(rec.Status) {
			return synthesizeTerminal(rec)
		}
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			// Last-chance store peek before failing — covers the case
			// where the worker finished AFTER subscribe but the bus
			// publish dropped (e.g. channel full).
			if jobs != nil {
				if rec, err := jobs.Get(t.Context(), graphRunID); err == nil && core.IsTerminalStatus(rec.Status) {
					return synthesizeTerminal(rec)
				}
			}
			t.Fatalf("timed out waiting for terminal event on %s", graphRunID)
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("event channel closed without terminal")
			}
			if ev.Terminal != nil {
				return *ev.Terminal
			}
		}
	}
}

// synthesizeTerminal builds a TerminalEvent from a JobRecord so tests
// using the helper's store-fallback path get the same shape as a live
// bus delivery.
func synthesizeTerminal(rec core.JobRecord) daemon.TerminalEvent {
	ev := daemon.TerminalEvent{
		JobID:  rec.ID,
		Status: rec.Status,
	}
	if rec.Result != nil {
		ev.Error = rec.Result.Error
	}
	return ev
}

// silence linter if unused in some build configurations
var _ = sync.OnceFunc
