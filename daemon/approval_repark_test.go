// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// reparkHarness wires one worker around a pausing drop whose Execute blocks
// until the test releases it, so the window between "the node finished
// executing" and "the worker writes the park" can be opened at will. That
// window is where a duplicate approval email came from.
type reparkHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	principal core.Principal
	started   chan struct{}
	release   chan struct{}
	notified  *atomic.Int32
}

func newReparkHarness(t *testing.T) *reparkHarness {
	t.Helper()

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	if _, _, err := auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil); err != nil {
		t.Fatalf("issue key: %v", err)
	}
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	h := &reparkHarness{
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
		notified: &atomic.Int32{},
	}

	reg := engine.NewRegistry()
	if err := reg.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "slow_gate",
			Summary:        "Test fixture: parks like await_approval, on the test's schedule.",
			Examples:       []core.ParamsExample{{Title: "default"}},
			Outputs:        []core.Port{{Port: "pending_url", MIME: []string{"text/plain"}}},
			AwaitsApproval: true,
		},
		Execute: func(ctx context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			h.started <- struct{}{}
			select {
			case <-h.release:
			case <-ctx.Done():
				return core.Result{}, ctx.Err()
			}
			return core.Result{
				JobID:  j.ID,
				Status: core.StatusAwaiting,
				Output: map[string]core.Ref{
					"pending_url": {MIME: "text/plain", Inline: "https://app.example/approve/x"},
				},
			}, nil
		},
	}); err != nil {
		t.Fatalf("register slow_gate: %v", err)
	}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: reg}}
	h.svc = &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	h.jobs = jobs
	h.principal = p

	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:           "w",
		PollInterval: 5 * time.Millisecond,
		MaxRetries:   1,
		// Long lease and renew interval on purpose: no renew tick fires
		// while Execute is blocked, so the worker reaches its park write
		// without having noticed that the record moved underneath it. That
		// is the real-world window — lease-loss detection is only as fresh
		// as the last renew tick — and it is the store's park fence, not the
		// renew loop, that has to hold here.
		LeaseDuration:   time.Hour,
		LeaseRenewEvery: time.Hour,
		OnNodeAwaiting: func(_ context.Context, _ core.Graph, _, _ string, _ core.Result) {
			h.notified.Add(1)
		},
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()
	return h
}

func (h *reparkHarness) submit(t *testing.T) string {
	t.Helper()
	g := core.Graph{
		ID: "gate-flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "gate", Module: "slow_gate"}},
	}
	runID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("SubmitGraph: %v", err)
	}
	return runID
}

// waitForNotifySettled gives the worker time to run its post-park path (the
// notify hook fires right after the status write) and returns the count. A
// bare read could pass while the second notification was still in flight.
func (h *reparkHarness) waitForNotifySettled(t *testing.T, want int32) int32 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.notified.Load(); got > want {
			return got // already too many — report immediately
		}
		if h.notified.Load() == want && want > 0 {
			// Keep watching briefly for a late duplicate.
			time.Sleep(50 * time.Millisecond)
			return h.notified.Load()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h.notified.Load()
}

// TestPark_SecondParkIsFencedAndDoesNotNotify reproduces the duplicate
// approval email.
//
// A node that parks is announced by OnNodeAwaiting, which mails the
// approvers. The announcement hangs off the park's status write, so it is
// only sent once if that write can only commit once. It could commit twice:
// an expired lease lets a second worker reclaim and re-execute a node the
// first worker is still running, and awaiting → awaiting was an accepted
// transition, so BOTH executions committed a park and BOTH mailed the
// approvers — the same request, the same link, twice.
//
// The ownership fence (worker_id) was supposed to stop the late writer, but
// dzd gave every process the same worker IDs ("dzd-dev-w0"), so across two
// instances it compared equal and passed. This test pins the store-level
// guard that holds even then: the record is parked out-of-band WITHOUT
// changing its worker id — the late worker still "owns" it — and the park
// must still be refused, with no second notification.
func TestPark_SecondParkIsFencedAndDoesNotNotify(t *testing.T) {
	t.Parallel()
	h := newReparkHarness(t)
	runID := h.submit(t)
	recID := daemon.NodeJobID(runID, "gate")

	// Wait until the worker is inside Execute holding its claim.
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started the node")
	}

	// The other instance parks first. Plain Complete leaves worker_id alone,
	// so the blocked worker's ownership fence will still match — exactly the
	// shared-worker-ID case that defeated it in production.
	firstPark := &core.Result{
		JobID:  recID,
		Status: core.StatusAwaiting,
		Output: map[string]core.Ref{
			"pending_url": {MIME: "text/plain", Inline: "https://app.example/approve/first"},
		},
	}
	if err := h.jobs.Complete(t.Context(), recID, core.JobStatusAwaiting, firstPark); err != nil {
		t.Fatalf("simulated first park: %v", err)
	}

	// Let the blocked worker finish and attempt its own park.
	close(h.release)

	if got := h.waitForNotifySettled(t, 0); got != 0 {
		t.Errorf("OnNodeAwaiting fired %d time(s) for an already-parked node; "+
			"each one mails the approvers, so this is the duplicate approval email", got)
	}

	// The first park's result must survive: a fenced writer must not clobber
	// the record it lost, or the approval link in the mail that already went
	// out would stop matching the one the record carries.
	rec, err := h.jobs.Get(t.Context(), recID)
	if err != nil {
		t.Fatalf("Get %s: %v", recID, err)
	}
	if rec.Status != core.JobStatusAwaiting {
		t.Errorf("status = %q, want awaiting", rec.Status)
	}
	if rec.Result == nil {
		t.Fatal("record lost its result")
	}
	url, _ := rec.Result.Output["pending_url"].Inline.(string)
	if url != "https://app.example/approve/first" {
		t.Errorf("pending_url = %q, want the first park's link (the fenced writer overwrote it)", url)
	}
}

// TestPark_FirstParkNotifiesOnce is the positive control: the guard above
// must not have cost the ONLY notification. A node that parks normally still
// announces itself exactly once.
func TestPark_FirstParkNotifiesOnce(t *testing.T) {
	t.Parallel()
	h := newReparkHarness(t)
	runID := h.submit(t)

	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never started the node")
	}
	close(h.release)

	if got := h.waitForNotifySettled(t, 1); got != 1 {
		t.Errorf("OnNodeAwaiting fired %d time(s), want exactly 1", got)
	}
	rec, err := h.jobs.Get(t.Context(), daemon.NodeJobID(runID, "gate"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Status != core.JobStatusAwaiting {
		t.Errorf("status = %q, want awaiting", rec.Status)
	}
}
