// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// publishGraph saves a graph and promotes it to the published environment.
// The scheduler only enrolls and fires PUBLISHED flows (require-published), so
// every test that expects an auto-trigger to fire must publish first.
func publishGraph(t *testing.T, store *workspace.Store, g core.Graph) {
	t.Helper()
	commit, err := store.Save(g, "test")
	if err != nil {
		t.Fatalf("save graph: %v", err)
	}
	if err := store.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish graph: %v", err)
	}
}

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
		Nodes: []core.Node{{ID: "tick", Module: "delay", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "* * * * *"}, // every minute
		},
	}
	publishGraph(t, wsStore, graph)

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

func TestScheduler_UnpublishedFlowIsNotEnrolled(t *testing.T) {
	// Require-published: a saved-but-never-published flow with a configured
	// cron trigger must NOT be enrolled by the scheduler, so it never fires on
	// its own until the author publishes it. (The editor shows "needs publish"
	// for exactly this state.)
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
	}
	// Saved to HEAD but deliberately NOT published.
	_, _ = wsStore.Save(core.Graph{
		ID: "draft-cron", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "tick", Module: "delay", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "* * * * *"}},
	}, "test")

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(schedCtx) }()
	time.Sleep(100 * time.Millisecond)
	if got := sched.TrackedCount(); got != 0 {
		t.Errorf("tracked=%d, want 0 (unpublished flow must not be enrolled)", got)
	}
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
		Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
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
		Nodes:    []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
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

// TestScheduler_TracksCronTriggerNode verifies Phase 2: a schedule set on
// a cron_trigger NODE (not on g.Triggers) is picked up by the scheduler.
func TestScheduler_TracksCronTriggerNode(t *testing.T) {
	ks := auth.NewMemKeyStore()
	wsStore, _ := workspace.OpenFS("")
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        daemon.NewMemoryBus(),
	}
	publishGraph(t, wsStore, core.Graph{
		ID: "node-cron", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *", "tz": "Europe/Stockholm"}}},
	})

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(schedCtx) }()
	time.Sleep(100 * time.Millisecond)
	if got := sched.TrackedCount(); got != 1 {
		t.Errorf("tracked=%d, want 1 (cron_trigger node schedule should be picked up)", got)
	}
}

// TestScheduler_SkipsDisabledTriggerNode confirms a cron_trigger node with
// params.disabled=true is individually paused: not tracked, not fired —
// even though the flow itself is enabled. This is the per-trigger pause
// the Schedules page toggles, finer-grained than the whole-flow Disabled.
func TestScheduler_SkipsDisabledTriggerNode(t *testing.T) {
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
		ID: "node-cron-paused", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{
			"cron": "0 9 * * *", "tz": "Europe/Stockholm", "disabled": true,
		}}},
	}, "test")

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(schedCtx) }()
	time.Sleep(100 * time.Millisecond)
	if got := sched.TrackedCount(); got != 0 {
		t.Errorf("tracked=%d, want 0 (a disabled trigger node must not fire)", got)
	}
}

// TestScheduler_IgnoresCronTriggerNodeWithoutSchedule confirms a blank
// schedule on the node means "run only on demand" — not tracked, not fired.
func TestScheduler_IgnoresCronTriggerNodeWithoutSchedule(t *testing.T) {
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
		ID: "node-cron-blank", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "sched", Module: "cron_trigger", Params: map[string]any{}}},
	}, "test")

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sched.Run(schedCtx) }()
	time.Sleep(100 * time.Millisecond)
	if got := sched.TrackedCount(); got != 0 {
		t.Errorf("tracked=%d, want 0 (blank node schedule must not fire)", got)
	}
}

// TestScheduler_ImpossibleCronDateDoesNotFire guards a runaway-loop:
// "0 0 30 2 *" (Feb 30 — never exists) PARSES fine, but cron.Schedule.
// Next() gives up after 5 years and returns the ZERO time. The fire
// check (!scheduleAt.After(now)) treats a zero time as "due now", so the
// graph would fire on every tick forever. A never-fires schedule must be
// dormant, not perpetually due.
func TestScheduler_ImpossibleCronDateDoesNotFire(t *testing.T) {
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "s", Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1}, jobs, svc.Engine, bus)
	go func() { _ = w.Run(wctx) }()

	_, _ = wsStore.Save(core.Graph{
		ID: "feb30", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "0 0 30 2 *"}}, // Feb 30: never
	}, "test")

	sched := daemon.NewScheduler(svc)
	sched.SetInterval(5*time.Millisecond, 30*time.Millisecond)
	schedCtx, schedCancel := context.WithCancel(context.Background())
	defer schedCancel()
	go func() { _ = sched.Run(schedCtx) }()

	// Many ticks elapse. A never-firing schedule must produce zero runs;
	// the zero-time bug would tight-loop and rack up runs.
	time.Sleep(200 * time.Millisecond)
	records, _ := jobs.ListByGraph(t.Context(), "feb30")
	runs := 0
	for _, r := range records {
		if r.Kind == core.JobKindGraph {
			runs++
		}
	}
	if runs != 0 {
		t.Errorf("runs=%d, want 0 — an impossible cron date fired a runaway loop", runs)
	}
}
