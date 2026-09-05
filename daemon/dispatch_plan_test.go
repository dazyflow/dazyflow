// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

// planFixture is a run of the diamond a→{b,c}→d with the given records
// already in the store, and a dispatcher over it that counts point reads.
func planFixture(t *testing.T, recs ...core.JobRecord) (*Dispatcher, *readCountingStore, core.Graph) {
	t.Helper()
	g := core.Graph{
		ID: "g", Tenant: "t",
		Nodes: []core.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}},
		Edges: []core.Edge{
			{From: "a", FromPort: core.PassPort, To: "b", ToPort: core.PassPort},
			{From: "a", FromPort: core.PassPort, To: "c", ToPort: core.PassPort},
			{From: "b", FromPort: core.PassPort, To: "d", ToPort: core.PassPort},
			{From: "c", FromPort: core.PassPort, To: "d", ToPort: core.PassPort},
		},
	}
	mem := jobstore.NewMemory()
	for _, r := range recs {
		if err := mem.Enqueue(context.Background(), r); err != nil {
			t.Fatalf("enqueue %s: %v", r.ID, err)
		}
	}
	st := &readCountingStore{Memory: mem}
	return NewDispatcher(st, NewMemoryBus(), nil, nil), st, g
}

type readCountingStore struct {
	*jobstore.Memory
	gets int
}

func (c *readCountingStore) Get(ctx context.Context, id string) (core.JobRecord, error) {
	c.gets++
	return c.Memory.Get(ctx, id)
}

func planNode(run, id string, status core.JobStatus) core.JobRecord {
	return core.JobRecord{ID: NodeJobID(run, id), Kind: core.JobKindNode, GraphRunID: run, GraphID: "g", NodeID: id, Tenant: "t", Status: status,
		Result: &core.Result{Status: core.StatusOK}}
}

// A dependent this node alone feeds is released in the plan, and deciding it
// costs no store read: the completing node's own record is what the index
// needs, and the plan has it in hand.
func TestPlanAdvance_ReleasesOwnDependentsWithoutReading(t *testing.T) {
	d, st, g := planFixture(t)
	plan := d.PlanAdvance(context.Background(), g, "run", "a", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, false)
	if plan.pause || plan.revisit {
		t.Errorf("plan = %+v, want a plain release", plan)
	}
	if len(plan.enqueue) != 2 || plan.enqueue[0].NodeID != "b" || plan.enqueue[1].NodeID != "c" {
		t.Errorf("enqueue = %+v, want b and c", plan.enqueue)
	}
	if plan.enqueue[0].ID != NodeJobID("run", "b") || plan.enqueue[0].Job.NodeID != "b" || plan.enqueue[0].Tenant != "t" {
		t.Errorf("record = %+v, want a fully addressed queued node record", plan.enqueue[0])
	}
	if st.gets != 0 {
		t.Errorf("%d store reads planning a's dependents, want 0", st.gets)
	}
}

// A join whose other feeder is still running is NOT decided before the
// commit — only a read after our own write is visible may conclude the other
// side is unfinished — so it is left for the post-commit pass.
func TestPlanAdvance_DefersJoinOnRunningSibling(t *testing.T) {
	d, _, g := planFixture(t, planNode("run", "c", core.JobStatusRunning))
	plan := d.PlanAdvance(context.Background(), g, "run", "b", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, false)
	if len(plan.enqueue) != 0 || !plan.revisit {
		t.Errorf("plan = %+v, want nothing released and a revisit", plan)
	}
}

// With the other feeder already terminal the join is safe to release early:
// a finished record cannot change under us.
func TestPlanAdvance_ReleasesJoinOnTerminalSibling(t *testing.T) {
	d, _, g := planFixture(t, planNode("run", "c", core.JobStatusSucceeded))
	plan := d.PlanAdvance(context.Background(), g, "run", "b", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, false)
	if len(plan.enqueue) != 1 || plan.enqueue[0].NodeID != "d" || plan.revisit {
		t.Errorf("plan = %+v, want d released outright", plan)
	}
}

// The post-commit pass releases the join the plan deferred, and counts it, so
// the run is not wrongly declared complete.
func TestFinishAdvance_RevisitReleasesJoin(t *testing.T) {
	d, st, g := planFixture(t,
		core.JobRecord{ID: "run", Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Status: core.JobStatusRunning},
		planNode("run", "b", core.JobStatusRunning),
		planNode("run", "c", core.JobStatusSucceeded),
	)
	plan := d.PlanAdvance(context.Background(), g, "run", "b", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, false)
	// c was terminal, so the plan releases d itself…
	if len(plan.enqueue) != 1 {
		t.Fatalf("plan = %+v, want d", plan)
	}
	// …but pretend the store had seen c still running: the plan then defers.
	plan = advancePlan{revisit: true}
	adv, err := st.CompleteAndEnqueue(context.Background(), NodeJobID("run", "b"), "", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, nil)
	if err != nil {
		t.Fatalf("complete b: %v", err)
	}
	d.FinishAdvance(context.Background(), g, "run", "b", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, plan, adv)
	if rec, err := st.Memory.Get(context.Background(), NodeJobID("run", "d")); err != nil || rec.Status != core.JobStatusQueued {
		t.Errorf("d = %+v, %v; want queued by the revisit", rec, err)
	}
	if run, _ := st.Memory.Get(context.Background(), "run"); run.Status != core.JobStatusRunning {
		t.Errorf("run = %q, want still running (d is queued)", run.Status)
	}
}

// A cancelled run, seen in the same write as the completion, stops the
// advance: no revisit, no completion check.
func TestFinishAdvance_CancelledRunStopsAdvance(t *testing.T) {
	d, st, g := planFixture(t,
		core.JobRecord{ID: "run", Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Status: core.JobStatusCancelled},
		planNode("run", "b", core.JobStatusRunning),
	)
	plan := advancePlan{revisit: true}
	d.FinishAdvance(context.Background(), g, "run", "b", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}, plan,
		core.Advance{RunStatus: core.JobStatusCancelled})
	if _, err := st.Memory.Get(context.Background(), NodeJobID("run", "d")); err == nil {
		t.Error("d was dispatched under a cancelled run")
	}
	if st.gets != 0 {
		t.Errorf("%d store reads after a cancelled run, want 0", st.gets)
	}
}

// A watched breakpoint releases nothing; an unwatched one is ignored.
func TestPlanAdvance_BreakpointPausesOnlyWhenWatched(t *testing.T) {
	d, _, g := planFixture(t)
	g.Nodes[0].Breakpoint = true
	if plan := d.PlanAdvance(context.Background(), g, "run", "a", core.JobStatusSucceeded, nil, true); !plan.pause || len(plan.enqueue) != 0 {
		t.Errorf("watched = %+v, want a pause", plan)
	}
	if plan := d.PlanAdvance(context.Background(), g, "run", "a", core.JobStatusSucceeded, nil, false); plan.pause || len(plan.enqueue) != 2 {
		t.Errorf("unwatched = %+v, want b and c released", plan)
	}
}

// A failure that propagates releases nothing; the run fails instead.
func TestPlanAdvance_PropagatingFailureReleasesNothing(t *testing.T) {
	d, _, g := planFixture(t)
	plan := d.PlanAdvance(context.Background(), g, "run", "a", core.JobStatusFailed, &core.Result{Status: core.StatusError}, false)
	if len(plan.enqueue) != 0 || plan.revisit {
		t.Errorf("plan = %+v, want nothing", plan)
	}
}
