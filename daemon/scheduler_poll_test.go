package daemon_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazyflow/drops"
	"git.sr.ht/~klahr/hazyflow/workspace"
)

// pollHarness mirrors the cron test's setup — Service + worker +
// scheduler with controllable clock. Pulled into a helper since the
// poll tests use the same scaffolding three times.
type pollHarness struct {
	svc     *daemon.Service
	jobs    core.JobStore
	wsStore *workspace.Store
	sched   *daemon.Scheduler
	now     time.Time
	mu      sync.Mutex
}

func newPollHarness(t *testing.T) *pollHarness {
	t.Helper()
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

	h := &pollHarness{
		svc:     svc,
		jobs:    jobs,
		wsStore: wsStore,
		now:     time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	h.sched = daemon.NewScheduler(svc)
	h.sched.SetInterval(5*time.Millisecond, 50*time.Millisecond)
	h.sched.SetClock(func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	})

	schedCtx, schedCancel := context.WithCancel(context.Background())
	t.Cleanup(schedCancel)
	go func() { _ = h.sched.Run(schedCtx) }()
	return h
}

func (h *pollHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

// waitForGraphRuns polls the jobstore until at least `want` completed
// graph runs exist for graphID, or the deadline passes.
func (h *pollHarness) waitForGraphRuns(t *testing.T, graphID string, want int, deadline time.Duration) int {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		records, _ := h.jobs.ListByGraph(t.Context(), graphID)
		got := 0
		for _, r := range records {
			if r.Kind == core.JobKindGraph && r.Status == core.JobStatusSucceeded {
				got++
			}
		}
		if got >= want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	records, _ := h.jobs.ListByGraph(t.Context(), graphID)
	got := 0
	for _, r := range records {
		if r.Kind == core.JobKindGraph && r.Status == core.JobStatusSucceeded {
			got++
		}
	}
	return got
}

func TestScheduler_FiresGraphWithPollTrigger(t *testing.T) {
	h := newPollHarness(t)
	graph := core.Graph{
		ID: "poll-1", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "tick", Module: "poll_trigger"},
		},
		Triggers: []core.GraphTrigger{
			{Type: "poll", IntervalSeconds: 60},
		},
	}
	if _, err := h.wsStore.Save(graph, "test"); err != nil {
		t.Fatalf("save graph: %v", err)
	}
	// Wait for rescan to pick it up.
	time.Sleep(80 * time.Millisecond)
	if h.sched.TrackedCount() != 1 {
		t.Fatalf("tracked=%d, want 1", h.sched.TrackedCount())
	}
	// Advance past the first fire (initial scheduleAt = now+60s).
	h.advance(70 * time.Second)
	got := h.waitForGraphRuns(t, "poll-1", 1, 3*time.Second)
	if got < 1 {
		t.Fatalf("first fire didn't happen within 3s")
	}
}

func TestScheduler_PollTriggerFiresRepeatedlyOnInterval(t *testing.T) {
	// Confirm the interval-anchored schedule advances correctly —
	// after one fire, the next should be `interval` seconds later.
	h := newPollHarness(t)
	graph := core.Graph{
		ID: "poll-rep", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "tick", Module: "poll_trigger"}},
		Triggers: []core.GraphTrigger{
			{Type: "poll", IntervalSeconds: 60},
		},
	}
	_, _ = h.wsStore.Save(graph, "test")
	time.Sleep(80 * time.Millisecond)

	// Advance 70s → first fire.
	h.advance(70 * time.Second)
	if h.waitForGraphRuns(t, "poll-rep", 1, 3*time.Second) < 1 {
		t.Fatalf("first fire missed")
	}
	// Advance another 70s → second fire.
	h.advance(70 * time.Second)
	if h.waitForGraphRuns(t, "poll-rep", 2, 3*time.Second) < 2 {
		t.Fatalf("second fire missed (got %d)", h.waitForGraphRuns(t, "poll-rep", 0, 0))
	}
}

func TestScheduler_PollAndCronCoexistOnSameGraph(t *testing.T) {
	// The trigger-index suffix in the tracked key means a graph with
	// both cron AND poll triggers gets two scheduler entries — one
	// per trigger — rather than one clobbering the other.
	h := newPollHarness(t)
	graph := core.Graph{
		ID: "hybrid", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "tick", Module: "poll_trigger"}},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "* * * * *"},   // every minute
			{Type: "poll", IntervalSeconds: 30}, // every 30s
		},
	}
	_, _ = h.wsStore.Save(graph, "test")
	time.Sleep(80 * time.Millisecond)
	if h.sched.TrackedCount() != 2 {
		t.Errorf("tracked=%d, want 2 (one per trigger)", h.sched.TrackedCount())
	}
}

func TestScheduler_BadPollIntervalIsIgnored(t *testing.T) {
	// Negative / zero interval is operator error — the scheduler
	// logs and skips it rather than panicking or scheduling at
	// time.Now+0 (which would tight-loop the worker).
	h := newPollHarness(t)
	graph := core.Graph{
		ID: "bad-poll", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "tick", Module: "poll_trigger"}},
		Triggers: []core.GraphTrigger{
			{Type: "poll", IntervalSeconds: 0},
		},
	}
	_, _ = h.wsStore.Save(graph, "test")
	time.Sleep(80 * time.Millisecond)
	if h.sched.TrackedCount() != 0 {
		t.Errorf("tracked=%d, want 0 (bad interval should be skipped)", h.sched.TrackedCount())
	}
}

// TestScheduler_HugePollIntervalIsIgnored guards an integer-overflow
// foot-gun: time.Duration is int64 nanoseconds (~292 years max), so
// `time.Duration(IntervalSeconds) * time.Second` wraps NEGATIVE for a
// large enough IntervalSeconds. A negative interval makes nextFireFrom
// return a time in the PAST, so the poll fires every scheduler tick —
// a runaway-run loop from one fat-fingered config value. The scheduler
// must reject an out-of-range interval the way it rejects <= 0.
func TestScheduler_HugePollIntervalIsIgnored(t *testing.T) {
	h := newPollHarness(t)
	graph := core.Graph{
		ID: "huge-poll", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "tick", Module: "poll_trigger"}},
		Triggers: []core.GraphTrigger{
			// >> maxInt64/1e9 seconds: the *time.Second multiply overflows.
			{Type: "poll", IntervalSeconds: 1 << 60},
		},
	}
	_, _ = h.wsStore.Save(graph, "test")
	time.Sleep(120 * time.Millisecond) // let rescan + several ticks elapse
	if h.sched.TrackedCount() != 0 {
		t.Errorf("tracked=%d, want 0 (overflowing interval must be skipped)", h.sched.TrackedCount())
	}
	// With the overflow bug present this tight-loops; assert no runs fired.
	if n := h.waitForGraphRuns(t, "huge-poll", 1, 300*time.Millisecond); n != 0 {
		t.Errorf("runs=%d, want 0 — overflowing interval fired a runaway loop", n)
	}
}
