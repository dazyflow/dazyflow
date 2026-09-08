// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

func fireGraphSvc(t *testing.T) (*Service, *workspace.Store, core.JobStore) {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "ed", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)
	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        NewMemoryBus(),
	}
	return svc, wsStore, jobs
}

func TestFireGraph_TriggerQuotaSkip(t *testing.T) {
	t.Parallel()
	svc, _, jobs := fireGraphSvc(t)
	svc.FreePollingDisabled = true
	plans := NewMemPlanStore()
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "acme", Plan: PlanFree})
	svc.Plans = plans

	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "g", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	runs, _ := jobs.ListByGraph(t.Context(), "g")
	if len(runs) != 1 {
		t.Fatalf("trigger-gated fire produced %d records, want 1 (the skip marker)", len(runs))
	}
	if runs[0].Status != core.JobStatusSkipped {
		t.Errorf("marker status = %q, want skipped (nothing ran, and nothing was lost)", runs[0].Status)
	}
	if runs[0].Result == nil || runs[0].Result.Error == nil || runs[0].Result.Error.Code != "plan_polling_off" {
		t.Errorf("marker does not say why: %+v", runs[0].Result)
	}
}

func TestFireGraph_RunCapSkip(t *testing.T) {
	t.Parallel()
	svc, ws, jobs := fireGraphSvc(t)
	plans := NewMemPlanStore()
	_ = plans.SetPlan(t.Context(), TenantPlan{Tenant: "acme", Plan: PlanFree})
	svc.Plans = plans
	svc.FreeRunsPerMonth = 1
	usage := NewMemUsageStore()
	svc.Usage = usage

	// Must be the CURRENT month: the run cap (BillingService.runsThisMonth)
	// reads the real-time current-month bucket via usagePeriod(time.Now()), so a
	// hardcoded month would stop matching once the calendar rolls past it.
	now := time.Now().UTC()
	_ = usage.AddRun(t.Context(), "acme", now) // consume the only allowed run

	g := core.Graph{
		ID: "capped", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	commit, _ := ws.Save(g, "u")
	_ = ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit)

	sched := NewScheduler(svc)
	sched.SetClock(func() time.Time { return now })
	e := &scheduledGraph{graphID: "capped", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	recs, err := jobs.ListGraphRuns(t.Context(), core.ListGraphRunsOpts{
		Tenant: "acme", Status: core.JobStatusSkipped, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListGraphRuns: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("skipped markers = %d, want 1", len(recs))
	}
	buckets, _ := usage.Usage(t.Context(), "acme", 1)
	if len(buckets) == 0 || buckets[0].SkippedRuns == 0 {
		t.Errorf("skipped-run counter not incremented: %+v", buckets)
	}
}

// Covers the belt-and-braces not-published gate inside fireGraph: a saved-but-
// unpublished flow never fires.
func TestFireGraph_NotPublishedSkip(t *testing.T) {
	t.Parallel()
	svc, ws, jobs := fireGraphSvc(t)
	// Save to HEAD but do NOT promote/publish.
	_, _ = ws.Save(core.Graph{
		ID: "draft", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}, "u")

	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "draft", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	runs, _ := jobs.ListByGraph(t.Context(), "draft")
	if len(runs) != 0 {
		t.Fatalf("unpublished flow fired %d runs, want 0", len(runs))
	}
}

// Covers fireGraph's open-workspace failure leg. No run is submitted — the
// flow cannot even be reached — but this is the case that most needed a
// record: the schedule is dead and stays dead, and it used to leave nothing
// behind but one line in the daemon log. It is marked FAILED rather than
// skipped, because "did not run and the owner has to fix something" is a
// different fact from "did not run, nothing lost", and only the failed one
// reaches the notification sweep.
func TestFireGraph_OpenWorkspaceError(t *testing.T) {
	t.Parallel()
	svc, _, jobs := fireGraphSvc(t)
	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "g", tenant: "ghost", workspace: "nope"}
	sched.fireGraph(context.Background(), e)
	runs, _ := jobs.ListByGraph(t.Context(), "g")
	if len(runs) != 1 {
		t.Fatalf("missing-workspace fire produced %d records, want 1 (the broken-schedule marker)", len(runs))
	}
	if runs[0].Status != core.JobStatusFailed {
		t.Errorf("marker status = %q, want failed so the notification sweep picks it up", runs[0].Status)
	}
	if runs[0].Result == nil || runs[0].Result.Error == nil ||
		runs[0].Result.Error.Code != "workspace_unavailable" {
		t.Errorf("marker does not say why: %+v", runs[0].Result)
	}
	// Coalesced: a per-minute cron against a dead workspace must not write
	// 1440 identical records a day.
	sched.fireGraph(context.Background(), e)
	runs, _ = jobs.ListByGraph(t.Context(), "g")
	if len(runs) != 1 {
		t.Errorf("second tick wrote another marker (%d total); it should coalesce", len(runs))
	}
}

func TestFireGraph_HappyPath(t *testing.T) {
	t.Parallel()
	svc, ws, jobs := fireGraphSvc(t)
	g := core.Graph{
		ID: "live", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	commit, err := ws.Save(g, "u")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	sched := NewScheduler(svc)
	e := &scheduledGraph{graphID: "live", tenant: "acme", workspace: "ws1"}
	sched.fireGraph(context.Background(), e)

	runs, _ := jobs.ListByGraph(t.Context(), "live")
	if len(runs) == 0 {
		t.Fatal("published fire produced no run")
	}
}

func onlyEntry(t *testing.T, s *Scheduler) *scheduledGraph {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tracked) != 1 {
		t.Fatalf("tracked = %d entries, want exactly 1", len(s.tracked))
	}
	for _, e := range s.tracked {
		return e
	}
	return nil
}

// The regression test for the cron-edit-takes-effect bug: editing a published
// flow's cron must recompute the next fire on the next rescan, not keep the
// stale next-fire from the old expression. Previously rescan preserved
// scheduleAt whenever the entry key still existed, so tightening a yearly
// schedule to every-minute idled until the old yearly fire elapsed.
func TestRescan_CronEditRecomputesScheduleAt(t *testing.T) {
	t.Parallel()
	svc, ws, _ := fireGraphSvc(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	g := core.Graph{
		ID: "edit", Tenant: "acme", Workspace: "ws1",
		Nodes:    []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "0 0 1 1 *"}},
	}
	commit, _ := ws.Save(g, "u")
	_ = ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit)

	sched := NewScheduler(svc)
	sched.SetClock(func() time.Time { return now })
	if err := sched.rescan(context.Background()); err != nil {
		t.Fatalf("rescan 1: %v", err)
	}
	before := onlyEntry(t, sched)
	if before.scheduleAt.Year() != 2027 {
		t.Fatalf("yearly first fire = %v, want 2027", before.scheduleAt)
	}

	// Tighten the schedule to every minute and rescan: the next fire must be
	// recomputed (about a minute out), not the stale 2027 value.
	g.Triggers[0].Cron = "* * * * *"
	commit2, _ := ws.Save(g, "u")
	_ = ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit2)
	if err := sched.rescan(context.Background()); err != nil {
		t.Fatalf("rescan 2: %v", err)
	}
	after := onlyEntry(t, sched)
	if !after.scheduleAt.Before(before.scheduleAt) {
		t.Errorf("scheduleAt not recomputed after cron edit: before=%v after=%v", before.scheduleAt, after.scheduleAt)
	}
	if after.scheduleAt.After(now.Add(2 * time.Minute)) {
		t.Errorf("every-minute fire = %v, want within ~1 min of %v", after.scheduleAt, now)
	}
}

// The regression test for the leader- failover re-fire bug: a follower
// inherits a frozen scheduleAt that is never advanced, so on takeover a stale
// (past) value would fire a tick the old leader already handled. reanchor must
// push it to the next fire after now.
func TestReanchor_AdvancesStaleScheduleAt(t *testing.T) {
	t.Parallel()
	svc, _, _ := fireGraphSvc(t)
	sched := NewScheduler(svc)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	stale := now.Add(-time.Hour) // a tick the "old leader" already fired
	sched.tracked = map[string]*scheduledGraph{
		"acme/ws1/g@poll": {
			graphID: "g", tenant: "acme", workspace: "ws1",
			interval: time.Minute, scheduleAt: stale,
		},
	}
	sched.reanchor(context.Background(), now)
	got := sched.tracked["acme/ws1/g@poll"].scheduleAt
	if !got.After(now) {
		t.Errorf("reanchor left a non-future fire: %v (now %v)", got, now)
	}
}

func publishPollFlow(t *testing.T, ws *workspace.Store, id string, seconds int) string {
	t.Helper()
	g := core.Graph{
		ID: id, Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{
			ID:     "tick",
			Module: "poll_trigger",
			Params: map[string]any{"interval_seconds": seconds},
		}},
	}
	commit, err := ws.Save(g, "u")
	if err != nil {
		t.Fatalf("save %s: %v", id, err)
	}
	if err := ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish %s: %v", id, err)
	}
	return "acme/ws1/" + id + "@tick"
}

// Pins that a leadership takeover keeps poll flows spread out. reanchor
// recomputes every tracked entry from ONE clock read, so anchoring on a bare
// nextFireFrom(now) lands every flow sharing a cadence on the identical
// instant — the thundering herd pollJitter exists to break up, arriving right
// as a node has gone down. Cron entries carry no interval and must keep their
// exact wall-clock anchor.
func TestReanchor_PreservesPollStagger(t *testing.T) {
	t.Parallel()
	svc, _, _ := fireGraphSvc(t)
	sched := NewScheduler(svc)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	cron, err := parseCronInTZ(sched.parser, "0 * * * *", "UTC")
	if err != nil {
		t.Fatalf("parse cron: %v", err)
	}
	sched.tracked = map[string]*scheduledGraph{
		"acme/ws1/alpha@tick": {graphID: "alpha", tenant: "acme", workspace: "ws1", interval: 5 * time.Minute},
		"acme/ws1/beta@tick":  {graphID: "beta", tenant: "acme", workspace: "ws1", interval: 5 * time.Minute},
		"acme/ws1/hourly@c":   {graphID: "hourly", tenant: "acme", workspace: "ws1", scheduleFn: cron},
	}
	sched.reanchor(context.Background(), now)

	a := sched.tracked["acme/ws1/alpha@tick"].scheduleAt
	b := sched.tracked["acme/ws1/beta@tick"].scheduleAt
	if a.Equal(b) {
		t.Errorf("reanchor collapsed both poll flows onto %v; stagger lost", a)
	}
	// Still inside one interval of now, and still in the future — the stagger
	// pulls the fire earlier, it must never delay it or resurrect a past tick.
	for key, got := range map[string]time.Time{"alpha": a, "beta": b} {
		if !got.After(now) {
			t.Errorf("%s: reanchored to a non-future fire %v (now %v)", key, got, now)
		}
		if got.After(now.Add(5 * time.Minute)) {
			t.Errorf("%s: reanchored past one interval: %v (now %v)", key, got, now)
		}
	}
	if h := sched.tracked["acme/ws1/hourly@c"].scheduleAt; !h.Equal(cron.Next(now)) {
		t.Errorf("cron entry jittered: got %v, want %v", h, cron.Next(now))
	}
}

// The regression test for the
// startup re-anchor bug. Run seeded its leadership tracking with
// `s.leader == nil`, but NewScheduler always installs a non-nil predicate — so
// the test was never true, every deploy took the "just took over" branch on its
// FIRST tick, and re-anchored the entries the initial rescan had just
// staggered. That collapsed every poll flow sharing a cadence onto one instant
// permanently: they then fired on the same tick and re-added the same interval
// forever.
//
// Drives the real Run loop with an always-true leader (what single-node gets)
// and asserts the stagger set up by the initial rescan survives.
func TestRun_StartupDoesNotCollapsePollStagger(t *testing.T) {
	t.Parallel()
	svc, ws, _ := fireGraphSvc(t)
	keyA := publishPollFlow(t, ws, "alpha", 3600)
	keyB := publishPollFlow(t, ws, "beta", 3600)

	sched := NewScheduler(svc)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	sched.SetClock(func() time.Time { return now })
	sched.SetInterval(time.Millisecond, time.Hour)
	// Stand in for NewScheduler's always-true single-node predicate, while
	// counting ticks so the assertion can't pass vacuously on a loop that
	// never ran.
	var ticks atomic.Int64
	sched.SetLeader(func() bool { ticks.Add(1); return true })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = sched.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for ticks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done

	if n := ticks.Load(); n < 3 {
		t.Fatalf("scheduler ticked %d times in 5s; test would assert nothing", n)
	}

	sched.mu.Lock()
	defer sched.mu.Unlock()
	a, okA := sched.tracked[keyA]
	b, okB := sched.tracked[keyB]
	if !okA || !okB {
		t.Fatalf("flows not enrolled: %q=%v %q=%v", keyA, okA, keyB, okB)
	}
	if a.scheduleAt.Equal(b.scheduleAt) {
		t.Errorf("startup collapsed both poll flows onto %v; rescan's stagger was re-anchored away", a.scheduleAt)
	}
}

func TestRescan_IdenticalCronTriggersCollapse(t *testing.T) {
	t.Parallel()
	svc, wsStore, _ := fireGraphSvc(t)
	g := core.Graph{
		ID: "many", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 0}}},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "* * * * *"},
			{Type: "cron", Cron: "* * * * *"},
			{Type: "cron", Cron: "* * * * *"},
			{Type: "cron", Cron: "0 9 * * *"},            // a different schedule
			{Type: "cron", Cron: "0 9 * * *", TZ: "UTC"}, // and a different zone
		},
	}
	commit, err := wsStore.Save(g, "test")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := wsStore.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	sched := NewScheduler(svc)
	if err := sched.rescan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if got := sched.TrackedCount(); got != 3 {
		t.Fatalf("tracked = %d, want 3 (one per distinct schedule)", got)
	}
}

// A stale enrollment must not fire a paused flow. The spec set no longer
// re-reads the flow's pause switch on every rescan, so fireGraph is what
// stands between a projection that failed to update and a flow the user
// believes is off.
func TestFireGraph_PausedFlowSkip(t *testing.T) {
	t.Parallel()
	svc, ws, jobs := fireGraphSvc(t)
	g := core.Graph{
		ID: "paused", Tenant: "acme", Workspace: "ws1", Disabled: true,
		Nodes: []core.Node{{ID: "n", Module: "delay", Params: map[string]any{"ms": 1}}},
	}
	commit, err := ws.Save(g, "u")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}

	sched := NewScheduler(svc)
	sched.fireGraph(context.Background(), &scheduledGraph{graphID: "paused", tenant: "acme", workspace: "ws1"})

	runs, _ := jobs.ListByGraph(t.Context(), "paused")
	if len(runs) != 0 {
		t.Fatalf("paused flow fired %d runs, want 0", len(runs))
	}
}

// A fire the scheduler owed and did not make must leave a trace. Restarts and
// failover both drop the ticks that fell in the gap — deliberately, since a
// duplicate is worse than a miss for a non-idempotent flow — but dropping them
// silently is how "the 08:00 report just didn't happen" became untraceable.
func TestRecordMissedFires_MarksTheGap(t *testing.T) {
	t.Parallel()
	svc, _, jobs := fireGraphSvc(t)
	sched := NewScheduler(svc)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sched.SetClock(func() time.Time { return now })

	e := &scheduledGraph{
		graphID: "g", tenant: "acme", workspace: "ws1",
		interval:   time.Minute,
		scheduleAt: now.Add(-30 * time.Minute),
	}
	sched.recordMissedFires(context.Background(), e, now)

	runs, _ := jobs.ListByGraph(t.Context(), "g")
	if len(runs) != 1 {
		t.Fatalf("got %d records, want 1 missed-fire marker", len(runs))
	}
	if runs[0].Status != core.JobStatusFailed {
		t.Errorf("status = %q, want failed so the notification sweep reports it", runs[0].Status)
	}
	if runs[0].Result == nil || runs[0].Result.Error == nil ||
		runs[0].Result.Error.Code != "schedule_fires_missed" {
		t.Fatalf("marker does not say why: %+v", runs[0].Result)
	}
	if !strings.Contains(runs[0].Result.Error.Message, "30") {
		t.Errorf("marker does not say how many were missed: %q", runs[0].Result.Error.Message)
	}
}

// On time is not a missed fire. The tick loop calls this on every due entry,
// so a false positive here would mark every healthy flow as broken.
func TestRecordMissedFires_QuietWhenOnTime(t *testing.T) {
	t.Parallel()
	svc, _, jobs := fireGraphSvc(t)
	sched := NewScheduler(svc)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sched.SetClock(func() time.Time { return now })

	e := &scheduledGraph{
		graphID: "g", tenant: "acme", workspace: "ws1",
		interval:   time.Minute,
		scheduleAt: now.Add(-time.Second),
	}
	sched.recordMissedFires(context.Background(), e, now)

	if runs, _ := jobs.ListByGraph(t.Context(), "g"); len(runs) != 0 {
		t.Fatalf("an on-time fire wrote %d marker(s)", len(runs))
	}
}

// Leadership takeover: the promoted leader inherits a frozen next-fire that
// the dead leader never delivered, discards it (right), and now says so.
func TestReanchor_RecordsWhatTheDeadLeaderOwed(t *testing.T) {
	t.Parallel()
	svc, _, jobs := fireGraphSvc(t)
	sched := NewScheduler(svc)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	sched.SetClock(func() time.Time { return now })
	sched.tracked = map[string]*scheduledGraph{
		"acme/ws1/g": {
			graphID: "g", tenant: "acme", workspace: "ws1",
			interval:   time.Minute,
			scheduleAt: now.Add(-10 * time.Minute),
		},
		// A healthy entry due in the future must not be reported.
		"acme/ws1/fine": {
			graphID: "fine", tenant: "acme", workspace: "ws1",
			interval:   time.Minute,
			scheduleAt: now.Add(5 * time.Minute),
		},
	}

	sched.reanchor(context.Background(), now)

	if runs, _ := jobs.ListByGraph(t.Context(), "g"); len(runs) != 1 {
		t.Errorf("takeover wrote %d marker(s) for the overdue entry, want 1", len(runs))
	}
	if runs, _ := jobs.ListByGraph(t.Context(), "fine"); len(runs) != 0 {
		t.Errorf("takeover marked a healthy entry as having missed fires (%d)", len(runs))
	}
	for k, e := range sched.tracked {
		if !e.scheduleAt.After(now) {
			t.Errorf("%s was not re-anchored (scheduleAt %s)", k, e.scheduleAt)
		}
	}
}
