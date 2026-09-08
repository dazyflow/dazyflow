// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// Measures what the stress rig structurally
// cannot: how long ONE run takes end to end on an otherwise IDLE fleet.
//
// The rig answers "how many steps per second at saturation", where the queue is
// never empty and therefore no worker ever waits on its poll interval. The
// number a person actually experiences — press Run, watch the canvas light up —
// is the opposite case, and it is dominated by how a worker learns there is
// work: it polls, and finds nothing, and sleeps.
//
// Reported, not asserted on a threshold: this is a measurement harness, and a
// wall-clock assertion would be flaky on a loaded CI box.
func TestRunLatencyByPollInterval(t *testing.T) {
	for _, wake := range []bool{false, true} {
		for _, poll := range []time.Duration{100 * time.Millisecond, 25 * time.Millisecond} {
			for _, steps := range []int{1, 4, 12} {
				name := fmt.Sprintf("wake=%v/poll=%s/steps=%d", wake, poll, steps)
				t.Run(name, func(t *testing.T) {
					h := newLatencyHarness(t, 4, poll, wake)
					var total time.Duration
					const reps = 5
					for range reps {
						total += h.runChain(t, steps)
					}
					mean := total / reps
					t.Logf("%-30s mean end-to-end %6.1fms  (%5.1fms per step)",
						name, float64(mean)/1e6, float64(mean)/1e6/float64(steps))
				})
			}
		}
	}
}

// blockingDrop occupies its worker for a real duration, unlike `delay`, which
// is deferrable and hands its slot back — the stress rig's README makes the
// same point about why `delay` cannot be used to reason about worker
// occupancy. Without a step that actually holds a worker, a fan-out test
// measures nothing: the root finishes in microseconds, before the other
// workers have even finished waking from the submit, so they are all still
// awake when its dependents appear and no poll is ever waited on.
const blockingDropID = "test_block"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: blockingDropID, Version: "1.0", Summary: "occupies a worker",
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
		},
		Execute: func(ctx context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			ms, _ := j.Params["ms"].(float64)
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
			case <-ctx.Done():
			}
			return core.Result{JobID: j.ID, Status: core.StatusOK}, nil
		},
	})
}

// The shape TestRunLatencyByPollInterval cannot see.
//
// That test uses a linear chain, where a worker completing step N goes
// straight back and claims step N+1 itself — so the whole chain costs one
// wake-up at the front and the per-step cost of dispatch is invisible. A
// FAN-OUT is the opposite: one step completing makes several ready at once,
// the completing worker can only take one of them, and every other branch
// waits for some other worker to notice. On an idle fleet that means a poll
// interval per wave, no matter how trivial the steps are.
func TestRunLatencyFanOut(t *testing.T) {
	for _, wake := range []bool{false, true} {
		for _, branches := range []int{4, 12} {
			t.Run(fmt.Sprintf("wake=%v/branches=%d", wake, branches), func(t *testing.T) {
				h := newLatencyHarness(t, 4, 100*time.Millisecond, wake)
				var total time.Duration
				const reps = 5
				for range reps {
					total += h.runFanOut(t, branches)
				}
				t.Logf("wake=%-5v branches=%-3d mean end-to-end %6.1fms",
					wake, branches, float64(total/reps)/1e6)
			})
		}
	}
}

func TestApprovalResumeLatency(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprintf("wake=%v", wake), func(t *testing.T) {
			h := newLatencyHarness(t, 4, 100*time.Millisecond, wake)
			var total time.Duration
			const reps = 5
			for range reps {
				total += h.timeApprovalResume(t)
			}
			t.Logf("wake=%-5v mean approve -> next step done %6.1fms", wake, float64(total/reps)/1e6)
		})
	}
}

func (h *latencyHarness) timeApprovalResume(t *testing.T) time.Duration {
	t.Helper()
	h.seq++
	runID := fmt.Sprintf("gate-%d", h.seq)
	g := core.Graph{
		ID: runID, Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "gate", Module: "await_approval", Params: map[string]any{"prompt": "ok?"}},
			{ID: "after", Module: blockingDropID, Params: map[string]any{"ms": 0}},
		},
		Edges: []core.Edge{{From: "gate", FromPort: "approved", To: "after", ToPort: "pass"}},
	}
	id, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	h.waitForStatus(t, daemon.NodeJobID(id, "gate"), core.JobStatusAwaiting)
	time.Sleep(150 * time.Millisecond)

	start := time.Now()
	if err := h.svc.Approve(t.Context(), id, "gate", daemon.ApprovalDecision{
		Decision: "approve", Approver: "someone",
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	h.waitForStatus(t, daemon.NodeJobID(id, "after"), core.JobStatusSucceeded)
	return time.Since(start)
}

func (h *latencyHarness) waitForStatus(t *testing.T, jobID string, want core.JobStatus) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if rec, err := h.jobs.Get(t.Context(), jobID); err == nil && rec.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s never reached %q", jobID, want)
}

type latencyHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       daemon.Bus
	principal core.Principal
	seq       int
}

func newLatencyHarness(t *testing.T, workers int, poll time.Duration, wake bool) *latencyHarness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil); err != nil {
		t.Fatalf("issue key: %v", err)
	}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	var sig *daemon.WorkSignal
	if wake {
		sig = daemon.NewWorkSignal()
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs, Engine: eng, Bus: bus, Wake: sig,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for i := range workers {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID:              fmt.Sprintf("w%d", i),
			PollInterval:    poll,
			LeaseDuration:   5 * time.Second,
			LeaseRenewEvery: 1 * time.Second,
			Wake:            sig,
		}, jobs, eng, bus)
		go func() { _ = w.Run(ctx) }()
	}
	time.Sleep(2 * poll)
	return &latencyHarness{svc: svc, jobs: jobs, bus: bus, principal: p}
}

func (h *latencyHarness) runFanOut(t *testing.T, branches int) time.Duration {
	t.Helper()
	h.seq++
	g := core.Graph{ID: fmt.Sprintf("fan-%d", h.seq), Tenant: "t", Workspace: "ws"}
	g.Nodes = append(g.Nodes, core.Node{
		ID: "root", Module: blockingDropID, Params: map[string]any{"ms": 250},
	})
	for i := range branches {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("b%d", i), Module: blockingDropID, Params: map[string]any{"ms": 0},
		})
		g.Edges = append(g.Edges, core.Edge{
			From: "root", FromPort: "pass",
			To: fmt.Sprintf("b%d", i), ToPort: "pass",
		})
	}
	return h.timeToTerminal(t, g)
}

func (h *latencyHarness) runChain(t *testing.T, steps int) time.Duration {
	t.Helper()
	h.seq++
	g := core.Graph{ID: fmt.Sprintf("chain-%d", h.seq), Tenant: "t", Workspace: "ws"}
	for i := range steps {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("n%d", i), Module: "delay",
			Params: map[string]any{"ms": 0},
		})
		if i > 0 {
			g.Edges = append(g.Edges, core.Edge{
				From: fmt.Sprintf("n%d", i-1), FromPort: "pass",
				To: fmt.Sprintf("n%d", i), ToPort: "pass",
			})
		}
	}
	return h.timeToTerminal(t, g)
}

func (h *latencyHarness) timeToTerminal(t *testing.T, g core.Graph) time.Duration {
	t.Helper()
	start := time.Now()
	runID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("run %s did not finish", runID)
		default:
		}
		rec, err := h.jobs.Get(t.Context(), runID)
		if err == nil {
			switch rec.Status {
			case core.JobStatusSucceeded:
				return time.Since(start)
			case core.JobStatusFailed, core.JobStatusCancelled:
				t.Fatalf("run status = %q", rec.Status)
			}
		}
		time.Sleep(time.Millisecond)
	}
}
