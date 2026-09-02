// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// poolHarness is newHarness with a worker POOL, so a case can reproduce what a
// default dzd actually runs: DAZYFLOW_WORKER_COUNT defaults to 2, and
// Worker.Run is a strictly serial claim → process loop, so a deployment
// executes at most two nodes at a time across every tenant.
func newPoolHarness(t *testing.T, workers int) *harness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, key, err := auth.IssueAPIKey(ks, t.Context(), "qa", "acme", "ws1", "qa@acme", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	wsStore, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:          auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces:    daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:          jobs,
		Engine:        eng,
		Bus:           bus,
		MaxGraphNodes: 1000,
		MaxGraphEdges: 5000,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for i := range workers {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID: "chaos-w" + itoa(i), PollInterval: 5 * time.Millisecond, MaxRetries: 1,
		}, jobs, eng, bus)
		w.SubGraphRunner = svc
		go func() { _ = w.Run(wctx) }()
	}
	p, err := svc.Authenticate(t.Context(), key)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return &harness{svc: svc, jobs: jobs, ws: wsStore, p: p, t: t}
}

// A parked approval holds no worker slot — TestParkedApprovals_DoNotStarveWorkers
// pins that. The Wait step does not park: executeDelay blocks inside the drop
// on a timer, on the worker goroutine that claimed it, and Worker.Run claims
// exactly one node at a time. So a Wait step is a worker slot held for the
// duration of the wait.
//
// The pool is shared by every tenant on the daemon and defaults to TWO
// (DAZYFLOW_WORKER_COUNT). A flow with as many parallel Wait steps as there are
// workers therefore stops the whole daemon for as long as it waits — no
// privilege needed beyond being able to run one flow, and nothing in the flow
// looks abnormal. `ms` is bounded per step (one year) and each occupancy is
// bounded by the 30-minute DefaultNodeTimeout backstop, but neither bounds how
// many slots a flow may hold at once, nor how long in total: Wait steps chain.
func TestParallelWaits_DoNotStarveWorkers(t *testing.T) {
	const (
		workers = 2
		waitMS  = 8000
	)
	h := newPoolHarness(t, workers)

	// One flow, `workers` parallel Wait steps hanging off one source.
	hog := graph("waithog", []core.Node{textNode("src", "go")}, nil)
	for i := range workers {
		id := "wait" + itoa(i)
		hog.Nodes = append(hog.Nodes, core.Node{
			ID: id, Module: "delay", Params: map[string]any{"ms": waitMS},
		})
		hog.Edges = append(hog.Edges, core.Edge{
			From: "src", FromPort: "out", To: id, ToPort: "pass",
		})
	}
	if _, err := h.svc.SubmitGraph(t.Context(), h.p, hog); err != nil {
		t.Fatalf("submit the hog: %v", err)
	}

	// Give the hog time to claim every slot, then submit an ordinary flow:
	// one text step, microseconds of work.
	time.Sleep(1500 * time.Millisecond)
	start := time.Now()
	status, err := h.submit(graph("bystander", []core.Node{textNode("a", "hello")}, nil), 30*time.Second)
	if err != nil {
		t.Fatalf("submit the bystander: %v", err)
	}
	waited := time.Since(start)
	t.Logf("an unrelated one-step flow finished %s after submit (status=%s) while %d Wait steps held the pool",
		waited.Round(100*time.Millisecond), status, workers)

	if status == statusHung {
		t.Errorf("FINDING: an unrelated one-step flow never finished while %d Wait steps held the pool", workers)
	}
	if waited > 2*time.Second {
		t.Errorf("FINDING: %d parallel Wait steps in ONE flow starved every other run on the daemon "+
			"for %s — a Wait blocks the worker that claimed it instead of parking like an approval",
			workers, waited.Round(100*time.Millisecond))
	}
}

// The same lever at volume: one submit, 60 Waits, 120 worker-seconds of queued
// work, all ready at once. Before the deferral this left an unrelated one-step
// flow unfinished after 45 seconds.
//
// NOT a general fairness guarantee, and deliberately named for what it does
// cover. The shared pool is still FIFO with no per-run or per-tenant share, so
// a flow whose steps are genuinely WORKING — 60 HTTP calls to a slow host —
// still occupies it, bounded only by DefaultNodeTimeout per step. What is
// fixed is that pure WAITING is no longer a way to hold a slot at all; real
// fairness is a scheduling policy the daemon has not adopted (see
// cmd/dzd/main.go, MAX_CONCURRENT_JOBS).
func TestQueuedWaits_DoNotMonopolizeThePool(t *testing.T) {
	const workers = 2
	h := newPoolHarness(t, workers)

	// 60 independent Wait steps of 2s each: 120 worker-seconds of queued work
	// from one submit, all ready at once.
	hog := graph("waitqueue", []core.Node{textNode("src", "go")}, nil)
	for i := range 60 {
		id := "w" + itoa(i)
		hog.Nodes = append(hog.Nodes, core.Node{
			ID: id, Module: "delay", Params: map[string]any{"ms": 2000},
		})
		hog.Edges = append(hog.Edges, core.Edge{From: "src", FromPort: "out", To: id, ToPort: "pass"})
	}
	if _, err := h.svc.SubmitGraph(t.Context(), h.p, hog); err != nil {
		t.Fatalf("submit the hog: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	start := time.Now()
	status, _ := h.submit(graph("bystander2", []core.Node{textNode("a", "hello")}, nil), 45*time.Second)
	waited := time.Since(start)
	t.Logf("bystander status=%s after %s (60 queued 2s Waits from one flow, %d workers)",
		status, waited.Round(100*time.Millisecond), workers)
	if waited > 5*time.Second || status == statusHung {
		t.Errorf("FINDING: one flow's 60 queued Wait steps delayed an unrelated one-step flow by %s "+
			"— a Wait must not hold a worker slot at all", waited.Round(100*time.Millisecond))
	}
}
