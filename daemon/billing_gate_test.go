package daemon_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
)

func gateGraph(id string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "src", Module: "source"}},
	}
}

// Free tenant at the cap: the (cap+1)-th submission is refused with
// core.ErrPlanLimit and writes no run state; the runs under the cap all
// went through. Counters come from the REAL metering path, not a fixture.
func TestPlanGate_FreeTenantCappedAtLimit(t *testing.T) {
	h := newSkipHarness(t)
	h.svc.Plans = daemon.NewMemPlanStore()
	h.svc.FreeRunsPerMonth = 2

	for i := 0; i < 2; i++ {
		runID, err := h.svc.SubmitGraph(t.Context(), h.principal, gateGraph("gated"))
		if err != nil {
			t.Fatalf("run %d under cap: %v", i+1, err)
		}
		waitForTerminalEvent(t, h.bus, h.jobs, runID, 5*time.Second)
	}

	_, err := h.svc.SubmitGraph(t.Context(), h.principal, gateGraph("gated"))
	if !errors.Is(err, core.ErrPlanLimit) {
		t.Fatalf("over-cap submit err = %v, want ErrPlanLimit", err)
	}
	if !strings.Contains(err.Error(), "2 of 2") {
		t.Errorf("error %q should name the cap (2 of 2)", err)
	}
	// The refused run never metered.
	buckets, _ := h.usage.Usage(t.Context(), "t", 1)
	if len(buckets) != 1 || buckets[0].GraphRuns != 2 {
		t.Errorf("usage = %+v, want exactly 2 runs", buckets)
	}
}

// A pro tenant sails past the free cap.
func TestPlanGate_ProTenantUnlimited(t *testing.T) {
	h := newSkipHarness(t)
	plans := daemon.NewMemPlanStore()
	_ = plans.SetPlan(t.Context(), daemon.TenantPlan{Tenant: "t", Plan: daemon.PlanPro})
	h.svc.Plans = plans
	h.svc.FreeRunsPerMonth = 1

	for i := 0; i < 3; i++ {
		runID, err := h.svc.SubmitGraph(t.Context(), h.principal, gateGraph("pro"))
		if err != nil {
			t.Fatalf("pro run %d: %v", i+1, err)
		}
		waitForTerminalEvent(t, h.bus, h.jobs, runID, 5*time.Second)
	}
}

// FreeRunsPerMonth unset (the self-hosted default) = no enforcement,
// even with a plan store wired and the tenant on free.
func TestPlanGate_DisabledByDefault(t *testing.T) {
	h := newSkipHarness(t)
	h.svc.Plans = daemon.NewMemPlanStore()
	// FreeRunsPerMonth left at zero.

	for i := 0; i < 3; i++ {
		runID, err := h.svc.SubmitGraph(t.Context(), h.principal, gateGraph("open"))
		if err != nil {
			t.Fatalf("run %d with gate off: %v", i+1, err)
		}
		waitForTerminalEvent(t, h.bus, h.jobs, runID, 5*time.Second)
	}
}
