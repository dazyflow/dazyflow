// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"slices"
	"sort"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

func cronFlow(id, expr string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "t", Workspace: "ws",
		Nodes:    []core.Node{{ID: "a", Module: "noop"}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: expr}},
	}
}

func newScheduleSvc(t *testing.T) (*Service, *workspace.Store, *MemScheduleStore) {
	t.Helper()
	ws, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	store := NewMemScheduleStore()
	svc := &Service{
		Workspaces: MapWorkspaces{"t/ws": ws},
		Jobs:       jobstore.NewMemory(),
		Engine:     &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
		Bus:        NewMemoryBus(),
		Schedules:  store,
	}
	return svc, ws, store
}

func specKeys(t *testing.T, store ScheduleStore) []string {
	t.Helper()
	specs, err := store.ListSchedules(context.Background())
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.EntryKey)
	}
	return out
}

// The projection must follow a flow through its whole lifecycle. Each of these
// transitions is a separate call site in service.go, and a missed one is a
// schedule that silently stops (or keeps) firing.
func TestReprojectSchedule_FollowsFlowLifecycle(t *testing.T) {
	t.Parallel()
	svc, _, store := newScheduleSvc(t)
	ctx := context.Background()
	p := covAdminPrincipal

	// Saved but not published: not enrolled.
	if _, err := svc.SaveGraph(ctx, p, cronFlow("f1", "*/5 * * * *")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := specKeys(t, store); len(got) != 0 {
		t.Fatalf("unpublished flow enrolled: %v", got)
	}

	// Published: enrolled.
	if _, err := svc.PublishFlow(ctx, p, "t", "ws", "f1", "", ""); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := specKeys(t, store); len(got) != 1 {
		t.Fatalf("after publish = %v, want 1 entry", got)
	}

	// Cadence edited on the draft: takes effect without republishing.
	if _, err := svc.SaveGraph(ctx, p, cronFlow("f1", "*/7 * * * *")); err != nil {
		t.Fatalf("save edit: %v", err)
	}
	specs, _ := store.ListSchedules(ctx)
	if len(specs) != 1 || specs[0].Cron != "*/7 * * * *" {
		t.Fatalf("after cadence edit = %+v, want the new expression", specs)
	}

	// Paused: every trigger comes offline.
	if _, err := svc.SetFlowEnabled(ctx, p, "t", "ws", "f1", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := specKeys(t, store); len(got) != 0 {
		t.Fatalf("disabled flow still enrolled: %v", got)
	}

	// Resumed.
	if _, err := svc.SetFlowEnabled(ctx, p, "t", "ws", "f1", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := specKeys(t, store); len(got) != 1 {
		t.Fatalf("after re-enable = %v, want 1 entry", got)
	}

	// Unpublished: offline again.
	if err := svc.UnpublishFlow(ctx, p, "t", "ws", "f1"); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if got := specKeys(t, store); len(got) != 0 {
		t.Fatalf("unpublished flow still enrolled: %v", got)
	}

	// Re-published then deleted: the rows must go with the flow.
	if _, err := svc.PublishFlow(ctx, p, "t", "ws", "f1", "", ""); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if got := specKeys(t, store); len(got) != 1 {
		t.Fatalf("after republish = %v, want 1 entry", got)
	}
	if err := svc.DeleteGraph(ctx, p, "t", "ws", "f1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := specKeys(t, store); len(got) != 0 {
		t.Fatalf("deleted flow still enrolled: %v", got)
	}
}

func TestReconcileSchedules_RepairsDriftAndPrunes(t *testing.T) {
	t.Parallel()
	svc, ws, store := newScheduleSvc(t)
	ctx := context.Background()

	// Publish two flows behind the projection's back, as an install upgrading
	// into the projection for the first time would look.
	for _, id := range []string{"a", "b"} {
		commit, err := ws.Save(cronFlow(id, "*/5 * * * *"), "u")
		if err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		if err := ws.PromoteToEnvironment(id, workspace.PublishedEnv, commit); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	// And a stale row for a flow that no longer exists.
	if err := store.ReplaceFlowSchedules(ctx, "t", "ws", "ghost", []ScheduleSpec{{
		Tenant: "t", Workspace: "ws", GraphID: "ghost",
		EntryKey: "t/ws/ghost#cron:* * * * *|", SpecKey: "cron:* * * * *|", Cron: "* * * * *",
	}}); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}

	n, err := svc.ReconcileSchedules(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 2 {
		t.Fatalf("reconcile examined %d flows, want 2", n)
	}
	got := specKeys(t, store)
	if len(got) != 2 {
		t.Fatalf("after reconcile = %v, want the two live flows only", got)
	}
	for _, k := range got {
		if k == "t/ws/ghost#cron:* * * * *|" {
			t.Fatalf("reconcile kept a row for a deleted flow: %v", got)
		}
	}
}

// The scheduler must enroll from the store when one is wired, without touching
// the workspaces — that is the whole point of the projection.
func TestScheduler_EnrollsFromScheduleStore(t *testing.T) {
	t.Parallel()
	svc, _, store := newScheduleSvc(t)
	ctx := context.Background()
	if err := store.ReplaceFlowSchedules(ctx, "t", "ws", "f1", []ScheduleSpec{{
		Tenant: "t", Workspace: "ws", GraphID: "f1",
		EntryKey: "t/ws/f1#cron:*/5 * * * *|", SpecKey: "cron:*/5 * * * *|", Cron: "*/5 * * * *",
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	sched := NewScheduler(svc)
	if err := sched.rescan(ctx); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if sched.TrackedCount() != 1 {
		t.Fatalf("tracked=%d, want 1", sched.TrackedCount())
	}

	// An entry that leaves the store leaves the tracked set on the next pass.
	if err := store.ReplaceFlowSchedules(ctx, "t", "ws", "f1", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := sched.rescan(ctx); err != nil {
		t.Fatalf("rescan 2: %v", err)
	}
	if sched.TrackedCount() != 0 {
		t.Fatalf("tracked=%d after clear, want 0", sched.TrackedCount())
	}
}

func TestDeriveScheduleSpecs(t *testing.T) {
	t.Parallel()
	base := func(nodes []core.Node, triggers []core.GraphTrigger) core.Graph {
		return core.Graph{ID: "g", Tenant: "t", Workspace: "ws", Nodes: nodes, Triggers: triggers}
	}
	tests := []struct {
		name  string
		graph core.Graph
		want  []string // SpecKeys, in order
	}{
		{"graph cron", base(nil, []core.GraphTrigger{{Type: "cron", Cron: "* * * * *", TZ: "UTC"}}),
			[]string{"cron:* * * * *|UTC"}},
		{"webhook ignored", base(nil, []core.GraphTrigger{{Type: "webhook"}}), nil},
		{"legacy graph poll ignored", base(nil, []core.GraphTrigger{{Type: "poll"}}), nil},
		{"bad cron dropped", base(nil, []core.GraphTrigger{{Type: "cron", Cron: "not a cron"}}), nil},
		{"bad tz dropped", base(nil, []core.GraphTrigger{{Type: "cron", Cron: "* * * * *", TZ: "Mars/Olympus"}}), nil},
		{"identical triggers collapse", base(nil, []core.GraphTrigger{
			{Type: "cron", Cron: "* * * * *"}, {Type: "cron", Cron: "* * * * *"},
		}), []string{"cron:* * * * *|"}},
		{"two cadences on one flow", base(nil, []core.GraphTrigger{
			{Type: "cron", Cron: "* * * * *"}, {Type: "cron", Cron: "*/5 * * * *"},
		}), []string{"cron:* * * * *|", "cron:*/5 * * * *|"}},
		{"cron node", base([]core.Node{{ID: "n", Module: "cron_trigger",
			Params: map[string]any{"cron": "*/5 * * * *", "tz": "Europe/Stockholm"}}}, nil),
			[]string{"cron:*/5 * * * *|Europe/Stockholm"}},
		{"cron node without expression", base([]core.Node{{ID: "n", Module: "cron_trigger",
			Params: map[string]any{}}}, nil), nil},
		{"poll node", base([]core.Node{{ID: "n", Module: "poll_trigger",
			Params: map[string]any{"interval_seconds": 300}}}, nil), []string{"poll:300"}},
		{"google form node", base([]core.Node{{ID: "n", Module: "google_form_trigger",
			Params: map[string]any{"interval_seconds": 60.0}}}, nil), []string{"poll:60"}},
		{"poll interval unset", base([]core.Node{{ID: "n", Module: "poll_trigger",
			Params: map[string]any{}}}, nil), nil},
		{"poll interval negative", base([]core.Node{{ID: "n", Module: "poll_trigger",
			Params: map[string]any{"interval_seconds": -5}}}, nil), nil},
		{"poll interval over ceiling", base([]core.Node{{ID: "n", Module: "poll_trigger",
			Params: map[string]any{"interval_seconds": core.MaxPollIntervalSeconds + 1}}}, nil), nil},
		{"node-level pause", base([]core.Node{{ID: "n", Module: "cron_trigger", Disabled: true,
			Params: map[string]any{"cron": "* * * * *"}}}, nil), nil},
		{"node-level pause via param", base([]core.Node{{ID: "n", Module: "cron_trigger",
			Params: map[string]any{"cron": "* * * * *", "disabled": true}}}, nil), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveScheduleSpecs(scheduleCronParser, "t", "ws", tc.graph, nil)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d specs %+v, want %d", len(got), got, len(tc.want))
			}
			for i, spec := range got {
				if spec.SpecKey != tc.want[i] {
					t.Errorf("spec %d key = %q, want %q", i, spec.SpecKey, tc.want[i])
				}
			}
		})
	}

	// A paused flow yields nothing regardless of what its triggers say.
	disabled := base([]core.Node{{ID: "n", Module: "poll_trigger",
		Params: map[string]any{"interval_seconds": 60}}},
		[]core.GraphTrigger{{Type: "cron", Cron: "* * * * *"}})
	disabled.Disabled = true
	if got := DeriveScheduleSpecs(scheduleCronParser, "t", "ws", disabled, nil); len(got) != 0 {
		t.Fatalf("disabled flow derived %+v, want none", got)
	}
}

// trackedKeys snapshots what the scheduler is currently enrolled to fire.
func trackedKeys(s *Scheduler) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tracked))
	for k := range s.tracked {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// richWorkspace publishes one flow of every trigger shape the scheduler
// understands, plus the shapes it must ignore.
func richWorkspace(t *testing.T, ws *workspace.Store) {
	t.Helper()
	flows := []core.Graph{
		{ID: "graph-cron", Tenant: "t", Workspace: "ws",
			Nodes:    []core.Node{{ID: "a", Module: "noop"}},
			Triggers: []core.GraphTrigger{{Type: "cron", Cron: "*/5 * * * *", TZ: "Europe/Stockholm"}}},
		{ID: "two-cadences", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "a", Module: "noop"}},
			Triggers: []core.GraphTrigger{
				{Type: "cron", Cron: "0 * * * *"}, {Type: "cron", Cron: "30 * * * *"}}},
		{ID: "duplicate-trigger", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "a", Module: "noop"}},
			Triggers: []core.GraphTrigger{
				{Type: "cron", Cron: "15 * * * *"}, {Type: "cron", Cron: "15 * * * *"}}},
		{ID: "cron-node", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "n1", Module: "cron_trigger",
				Params: map[string]any{"cron": "*/10 * * * *", "tz": "UTC"}}}},
		{ID: "poll-node", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "n1", Module: "poll_trigger",
				Params: map[string]any{"interval_seconds": 300}}}},
		{ID: "gform-node", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "n1", Module: "google_form_trigger",
				Params: map[string]any{"interval_seconds": 60}}}},
		{ID: "mixed", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{
				{ID: "n1", Module: "cron_trigger", Params: map[string]any{"cron": "0 9 * * *"}},
				{ID: "n2", Module: "poll_trigger", Params: map[string]any{"interval_seconds": 900}}},
			Triggers: []core.GraphTrigger{{Type: "cron", Cron: "0 6 * * *"}}},
		{ID: "paused-node", Tenant: "t", Workspace: "ws",
			Nodes: []core.Node{{ID: "n1", Module: "cron_trigger", Disabled: true,
				Params: map[string]any{"cron": "* * * * *"}}}},
		{ID: "webhook-only", Tenant: "t", Workspace: "ws",
			Nodes:    []core.Node{{ID: "a", Module: "noop"}},
			Triggers: []core.GraphTrigger{{Type: "webhook"}}},
		{ID: "bad-cron", Tenant: "t", Workspace: "ws",
			Nodes:    []core.Node{{ID: "a", Module: "noop"}},
			Triggers: []core.GraphTrigger{{Type: "cron", Cron: "not a cron"}}},
	}
	for _, g := range flows {
		commit, err := ws.Save(g, "u")
		if err != nil {
			t.Fatalf("save %s: %v", g.ID, err)
		}
		if err := ws.PromoteToEnvironment(g.ID, workspace.PublishedEnv, commit); err != nil {
			t.Fatalf("publish %s: %v", g.ID, err)
		}
	}
	// A published-then-paused flow, and an unpublished draft with a schedule.
	paused := core.Graph{ID: "paused-flow", Tenant: "t", Workspace: "ws", Disabled: true,
		Nodes:    []core.Node{{ID: "a", Module: "noop"}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "* * * * *"}}}
	commit, err := ws.Save(paused, "u")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.PromoteToEnvironment(paused.ID, workspace.PublishedEnv, commit); err != nil {
		t.Fatal(err)
	}
	draft := core.Graph{ID: "draft", Tenant: "t", Workspace: "ws",
		Nodes:    []core.Node{{ID: "a", Module: "noop"}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "* * * * *"}}}
	if _, err := ws.Save(draft, "u"); err != nil {
		t.Fatal(err)
	}
}

// The projection has to enroll exactly what walking the workspaces would. This
// is the property the whole change rests on, so it is asserted directly rather
// than inferred from the two paths' unit tests.
func TestScheduleProjection_MatchesTheWorkspaceWalk(t *testing.T) {
	t.Parallel()
	svc, ws, store := newScheduleSvc(t)
	richWorkspace(t, ws)

	// The walk, as deployments without Postgres still run it.
	svc.Schedules = nil
	walkSched := NewScheduler(svc)
	if err := walkSched.rescan(context.Background()); err != nil {
		t.Fatalf("walk rescan: %v", err)
	}
	walked := trackedKeys(walkSched)
	if len(walked) == 0 {
		t.Fatal("the walk enrolled nothing; the fixture is wrong")
	}

	// The projection, filled the way a fresh install fills it.
	svc.Schedules = store
	if _, err := svc.ReconcileSchedules(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	projSched := NewScheduler(svc)
	if err := projSched.rescan(context.Background()); err != nil {
		t.Fatalf("projection rescan: %v", err)
	}
	projected := trackedKeys(projSched)

	if !slices.Equal(walked, projected) {
		t.Fatalf("projection and walk disagree:\n  walk       = %v\n  projection = %v", walked, projected)
	}
	t.Logf("both paths enrolled %d entries", len(walked))
}

// countingSchedules records how many writes the reconcile actually issues.
type countingSchedules struct {
	ScheduleStore
	replaces int
	prunes   int
}

func (c *countingSchedules) ReplaceFlowSchedules(ctx context.Context, tenant, ws, graphID string, specs []ScheduleSpec) error {
	c.replaces++
	return c.ScheduleStore.ReplaceFlowSchedules(ctx, tenant, ws, graphID, specs)
}

func (c *countingSchedules) PruneMissingFlows(ctx context.Context, live map[string]struct{}) (int, error) {
	c.prunes++
	return c.ScheduleStore.PruneMissingFlows(ctx, live)
}

// The reconcile is an hourly pass over every flow in the install. It only earns
// that if an unchanged flow costs no write.
func TestReconcileSchedules_SecondPassWritesNothing(t *testing.T) {
	t.Parallel()
	svc, ws, store := newScheduleSvc(t)
	richWorkspace(t, ws)
	counter := &countingSchedules{ScheduleStore: store}
	svc.Schedules = counter
	ctx := context.Background()

	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	first := counter.replaces
	if first == 0 {
		t.Fatal("first reconcile wrote nothing; the fixture is wrong")
	}

	counter.replaces = 0
	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if counter.replaces != 0 {
		t.Fatalf("steady-state reconcile issued %d writes, want 0", counter.replaces)
	}

	// A real change still gets written.
	edited := core.Graph{ID: "graph-cron", Tenant: "t", Workspace: "ws",
		Nodes:    []core.Node{{ID: "a", Module: "noop"}},
		Triggers: []core.GraphTrigger{{Type: "cron", Cron: "*/7 * * * *", TZ: "Europe/Stockholm"}}}
	if _, err := ws.Save(edited, "u"); err != nil {
		t.Fatal(err)
	}
	counter.replaces = 0
	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if counter.replaces != 1 {
		t.Fatalf("reconcile after one edit issued %d writes, want exactly 1", counter.replaces)
	}
}

// The reconcile's whole job is repairing a projection that drifted, so the
// repair is asserted through the scheduler rather than through the store.
func TestReconcileSchedules_RepairsADroppedRowSoTheFlowEnrollsAgain(t *testing.T) {
	t.Parallel()
	svc, ws, store := newScheduleSvc(t)
	ctx := context.Background()
	richWorkspace(t, ws)
	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	sched := NewScheduler(svc)
	if err := sched.rescan(ctx); err != nil {
		t.Fatal(err)
	}
	full := len(trackedKeys(sched))

	// Simulate a projection write that failed after its commit landed.
	if err := store.ReplaceFlowSchedules(ctx, "t", "ws", "graph-cron", nil); err != nil {
		t.Fatal(err)
	}
	if err := sched.rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(trackedKeys(sched)); got != full-1 {
		t.Fatalf("after the drop = %d entries, want %d", got, full-1)
	}

	if _, err := svc.ReconcileSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sched.rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if got := len(trackedKeys(sched)); got != full {
		t.Fatalf("reconcile did not restore the dropped enrollment: %d, want %d", got, full)
	}
}

func TestSameSpecSet(t *testing.T) {
	t.Parallel()
	mk := func(key, spec string) ScheduleSpec {
		return ScheduleSpec{Tenant: "t", Workspace: "ws", GraphID: "g", EntryKey: key, SpecKey: spec}
	}
	a, b, c := mk("k1", "cron:a"), mk("k2", "cron:b"), mk("k1", "cron:CHANGED")

	cases := []struct {
		name string
		x, y []ScheduleSpec
		want bool
	}{
		{"both empty", nil, nil, true},
		{"empty vs one", nil, []ScheduleSpec{a}, false},
		{"identical", []ScheduleSpec{a, b}, []ScheduleSpec{a, b}, true},
		{"reordered", []ScheduleSpec{a, b}, []ScheduleSpec{b, a}, true},
		{"same key, changed cadence", []ScheduleSpec{a}, []ScheduleSpec{c}, false},
		{"one added", []ScheduleSpec{a}, []ScheduleSpec{a, b}, false},
		{"one removed", []ScheduleSpec{a, b}, []ScheduleSpec{a}, false},
		{"swapped for a different key", []ScheduleSpec{a}, []ScheduleSpec{b}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameSpecSet(tc.x, tc.y); got != tc.want {
				t.Fatalf("sameSpecSet = %v, want %v", got, tc.want)
			}
			if got := sameSpecSet(tc.y, tc.x); got != tc.want {
				t.Fatalf("sameSpecSet (reversed args) = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMemScheduleStore_DeleteByTenant(t *testing.T) {
	t.Parallel()
	store := NewMemScheduleStore()
	ctx := context.Background()
	spec := func(tenant, graphID string) ScheduleSpec {
		return ScheduleSpec{Tenant: tenant, Workspace: "ws", GraphID: graphID,
			EntryKey: tenant + "/ws/" + graphID + "#a", SpecKey: "cron:a", Cron: "* * * * *"}
	}
	for _, s := range []ScheduleSpec{spec("t1", "f1"), spec("t1", "f2"), spec("t2", "f1")} {
		if err := store.ReplaceFlowSchedules(ctx, s.Tenant, "ws", s.GraphID, []ScheduleSpec{s}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.DeleteByTenant(ctx, "t1")
	if err != nil || n != 2 {
		t.Fatalf("DeleteByTenant = %d / %v, want 2", n, err)
	}
	all, _ := store.ListSchedules(ctx)
	if len(all) != 1 || all[0].Tenant != "t2" {
		t.Fatalf("after erasure = %+v, want only t2", all)
	}
	// A tenant whose name is a prefix of another must not take it with it.
	if n, _ := store.DeleteByTenant(ctx, "t"); n != 0 {
		t.Fatalf("DeleteByTenant(\"t\") removed %d rows from tenant \"t2\"", n)
	}
}

// failingSchedules rejects every write, standing in for a database that is down
// at the moment a flow is saved.
type failingSchedules struct{ ScheduleStore }

func (failingSchedules) ReplaceFlowSchedules(context.Context, string, string, string, []ScheduleSpec) error {
	return errors.New("schedule store is down")
}

// The projection is written after the flow's own commit has already landed, so
// a projection failure must never surface as the user's error — the reconcile
// is what repairs it. A save that failed here would be a save the user is told
// did not happen, on a flow that is already saved.
func TestReprojectSchedule_StoreFailureDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()
	svc, ws, _ := newScheduleSvc(t)
	svc.Schedules = failingSchedules{NewMemScheduleStore()}
	svc.Logger = log.New(io.Discard, "", 0)
	ctx := context.Background()
	p := covAdminPrincipal

	if _, err := svc.SaveGraph(ctx, p, cronFlow("f1", "*/5 * * * *")); err != nil {
		t.Fatalf("save failed because the projection did: %v", err)
	}
	if _, err := svc.PublishFlow(ctx, p, "t", "ws", "f1", "", ""); err != nil {
		t.Fatalf("publish failed because the projection did: %v", err)
	}
	if _, err := svc.SetFlowEnabled(ctx, p, "t", "ws", "f1", false); err != nil {
		t.Fatalf("pause failed because the projection did: %v", err)
	}
	if err := svc.UnpublishFlow(ctx, p, "t", "ws", "f1"); err != nil {
		t.Fatalf("unpublish failed because the projection did: %v", err)
	}
	if err := svc.DeleteGraph(ctx, p, "t", "ws", "f1"); err != nil {
		t.Fatalf("delete failed because the projection did: %v", err)
	}
	// And the flow really is gone from the workspace, not merely un-projected.
	if _, err := ws.Load("f1"); err == nil {
		t.Fatal("flow still present after delete")
	}
}

// A Service with no ScheduleStore at all must behave exactly as before —
// deployments without Postgres still run the workspace walk.
func TestReprojectSchedule_NilStoreIsANoop(t *testing.T) {
	t.Parallel()
	svc, _, _ := newScheduleSvc(t)
	svc.Schedules = nil
	ctx := context.Background()
	if _, err := svc.SaveGraph(ctx, covAdminPrincipal, cronFlow("f1", "*/5 * * * *")); err != nil {
		t.Fatalf("save with no schedule store: %v", err)
	}
	if _, err := svc.PublishFlow(ctx, covAdminPrincipal, "t", "ws", "f1", "", ""); err != nil {
		t.Fatalf("publish with no schedule store: %v", err)
	}
	// The walk still enrolls it.
	sched := NewScheduler(svc)
	if err := sched.rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if sched.TrackedCount() != 1 {
		t.Fatalf("tracked=%d, want 1", sched.TrackedCount())
	}
}
