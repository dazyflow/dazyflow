package daemon_test

import (
	"context"
	"sync"
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

func TestScheduler_FiresGraphWithCronTrigger(t *testing.T) {
	// Build a Service + worker, save a graph with a cron trigger that
	// fires every minute, then advance a synthetic clock past the
	// schedule and observe the worker run the graph.
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "scheduler-test", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	// Seed a graph that fires every minute and contains a single sleep.
	graph := core.Graph{
		ID: "hourly", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "tick", Module: "sleep", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "* * * * *"}, // every minute
		},
	}
	if _, err := wsStore.Save(graph, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	sched := daemon.NewScheduler(svc)
	// Fast tick + rescan so the test doesn't have to wait real time.
	sched.SetInterval(5*time.Millisecond, 50*time.Millisecond)

	// Use a controllable clock. The scheduler reads it from a goroutine
	// while the test mutates it from another, so we guard with a mutex.
	var (
		clockMu sync.Mutex
		now     = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	)
	sched.SetClock(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	})

	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go func() { _ = sched.Run(schedCtx) }()

	// Give it time to do its first rescan, then advance the clock past
	// the next-minute boundary.
	time.Sleep(60 * time.Millisecond)
	if sched.TrackedCount() != 1 {
		t.Fatalf("tracked=%d, want 1", sched.TrackedCount())
	}
	// Jump past the next minute so the cron entry becomes due.
	clockMu.Lock()
	now = now.Add(2 * time.Minute)
	clockMu.Unlock()

	// Wait up to 3 seconds for the graph to run.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		records, _ := jobs.ListByGraph(t.Context(), "hourly")
		var graphRunFound bool
		for _, r := range records {
			if r.Kind == core.JobKindGraph && r.Status == core.JobStatusSucceeded {
				graphRunFound = true
				break
			}
		}
		if graphRunFound {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("scheduler did not fire the cron-triggered graph within 3s")
}

func TestScheduler_IgnoresGraphWithoutTrigger(t *testing.T) {
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
	}
	// A graph with no triggers must not be picked up.
	_, _ = wsStore.Save(core.Graph{
		ID: "manual", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
	}, "test")

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(schedCtx) }()
	time.Sleep(100 * time.Millisecond)
	if got := sched.TrackedCount(); got != 0 {
		t.Errorf("tracked=%d, want 0 (graph has no triggers)", got)
	}
}

func TestScheduler_RejectsBadCron(t *testing.T) {
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
	}
	_, _ = wsStore.Save(core.Graph{
		ID: "bad-cron", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "a", Module: "sleep", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "this is not a cron expression"}},
	}, "test")

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(schedCtx) }()
	time.Sleep(80 * time.Millisecond)
	// Bad cron should not be tracked.
	if got := sched.TrackedCount(); got != 0 {
		t.Errorf("tracked=%d, want 0 (bad cron expr should be rejected)", got)
	}
}
