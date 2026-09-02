// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/workspace"
)

// clockedScheduler runs a scheduler over the harness's service on a clock the
// test drives, so a case can jump over cron boundaries instead of waiting.
type clockedScheduler struct {
	sched *daemon.Scheduler
	mu    sync.Mutex
	now   time.Time
}

func newClockedScheduler(t *testing.T, h *harness) *clockedScheduler {
	t.Helper()
	cs := &clockedScheduler{
		sched: daemon.NewScheduler(h.svc),
		now:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	cs.sched.SetInterval(5*time.Millisecond, 25*time.Millisecond)
	cs.sched.SetClock(func() time.Time {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		return cs.now
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = cs.sched.Run(ctx) }()
	return cs
}

func (cs *clockedScheduler) advance(d time.Duration) {
	cs.mu.Lock()
	cs.now = cs.now.Add(d)
	cs.mu.Unlock()
}

// publish stores a graph through the REAL save gate, then publishes it, so
// the trigger paths (which require a published commit) can see it.
func (h *harness) publish(t *testing.T, g core.Graph) error {
	t.Helper()
	commit, err := h.svc.SaveGraph(t.Context(), h.p, g)
	if err != nil {
		return err
	}
	return h.ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit)
}

// One flow used to fire itself as often as its author cared to type: nothing
// capped len(Triggers), and the scheduler keyed entries by position in the
// array, so 2000 pasted copies of "* * * * *" were 2000 entries and 2000 runs
// a minute.
func TestTriggerArray_IsCapped(t *testing.T) {
	h := newHarness(t)
	flood := graph("trigflood", []core.Node{textNode("a", "x")}, nil)
	for range 2000 {
		flood.Triggers = append(flood.Triggers, core.GraphTrigger{Type: "cron", Cron: "* * * * *"})
	}
	if err := h.publish(t, flood); err == nil {
		t.Errorf("FINDING: %d triggers on one flow were stored", len(flood.Triggers))
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}

	// At the cap, and every trigger identical: one entry, one run per minute.
	g := graph("trigdupe", []core.Node{textNode("a", "x")}, nil)
	for range core.MaxGraphTriggers {
		g.Triggers = append(g.Triggers, core.GraphTrigger{Type: "cron", Cron: "* * * * *"})
	}
	if err := h.publish(t, g); err != nil {
		t.Fatalf("triggers at the cap were refused: %v", err)
	}

	cs := newClockedScheduler(t, h)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && cs.sched.TrackedCount() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if tracked := cs.sched.TrackedCount(); tracked != 1 {
		t.Errorf("FINDING: %d identical triggers produced %d scheduler entries, want 1",
			core.MaxGraphTriggers, tracked)
	}

	cs.advance(2 * time.Minute)
	time.Sleep(500 * time.Millisecond)
	if runs := h.countRuns(); runs > 1 {
		t.Errorf("FINDING: one clock minute started %d runs from one flow", runs)
	} else {
		t.Logf("%d scheduler entries, %d run in a clock minute", cs.sched.TrackedCount(), runs)
	}
}

// A breakpoint lives in the saved graph, so a published flow carries it into
// every run its trigger starts — with nobody there to continue it. A paused run
// is deliberately never reaped (the reaper reads its un-dispatched dependents
// as outstanding work), so each fire used to leave a non-terminal run behind
// for good, and a tenant with a max_concurrency lost its slots to runs no one
// would ever resume. Breakpoints now hold only runs somebody is watching.
func TestBreakpointInPublishedFlow_DoesNotParkTriggeredRuns(t *testing.T) {
	h := newHarness(t)
	first := textNode("a", "x")
	first.Breakpoint = true
	g := graph("bptrigger",
		[]core.Node{first, {ID: "b", Module: "delay", Params: map[string]any{"ms": 0}}},
		[]core.Edge{{From: "a", FromPort: "out", To: "b", ToPort: "pass"}})
	g.Triggers = []core.GraphTrigger{{Type: "cron", Cron: "* * * * *"}}
	if err := h.publish(t, g); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cs := newClockedScheduler(t, h)
	for range 5 {
		cs.advance(time.Minute)
		time.Sleep(300 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // every started run gets a chance to finish

	recs, err := h.jobs.ListGraphRuns(context.Background(), core.ListGraphRunsOpts{Limit: 10000})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	stuck := 0
	for _, r := range recs {
		if !core.IsTerminalStatus(r.Status) {
			stuck++
		}
	}
	t.Logf("%d unattended runs started, %d non-terminal", len(recs), stuck)
	if len(recs) == 0 {
		t.Fatal("no runs fired — the case did not exercise anything")
	}
	if stuck > 0 {
		t.Errorf("FINDING: %d triggered runs are parked on a breakpoint with nobody to continue them", stuck)
	}
}
