// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// A failure email is for a run that failed while nobody was looking. Someone
// who pressed Run in the editor and is watching the canvas turn red does not
// need to be told by email — and being told anyway is how people learn to
// ignore the mail that does matter.
//
// So: a manual run sends no email, on either channel. The webhook still fires,
// because that is a machine channel the flow's author wired deliberately.

func TestFailureNotify_ManualRunSendsNoOwnerEmail(t *testing.T) {
	svc, srv := ownerEmailHarness(t, auth.User{Email: "owner@example.com"})
	graph := core.Graph{
		ID: "daily", Name: "Daily Report", Tenant: "t", Workspace: "ws",
		Owner: "owner@example.com",
	}
	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, true)

	// Nothing, and the wait has to be long enough that "nothing yet" is not
	// what is being measured.
	if data, _ := waitForEmail(t, srv, 700*time.Millisecond); data != "" {
		t.Errorf("a run the author was watching still emailed them:\n%s", data)
	}
}

func TestFailureNotify_ManualRunSendsNoPerFlowEmail(t *testing.T) {
	// The per-flow address is off too. It is usually a shared inbox or an
	// on-call alias, which is exactly the audience that should not be paged
	// because somebody was testing a flow.
	svc, srv := ownerEmailHarness(t, auth.User{Email: "owner@example.com"})
	graph := core.Graph{
		ID: "daily", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Email: "alerts@example.com"},
	}
	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, true)

	if data, to := waitForEmail(t, srv, 700*time.Millisecond); data != "" {
		t.Errorf("manual run emailed %v:\n%s", to, data)
	}
}

// The other half of the rule: an automatic run is unchanged. Worth its own test
// because suppressing the email is one line, and suppressing it for everything
// would look exactly the same in the tests above.
func TestFailureNotify_AutomaticRunStillEmails(t *testing.T) {
	svc, srv := ownerEmailHarness(t, auth.User{Email: "owner@example.com"})
	graph := core.Graph{
		ID: "daily", Name: "Daily Report", Tenant: "t", Workspace: "ws",
		Owner: "owner@example.com",
	}
	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, false)

	data, to := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("a scheduled run's failure sent no email")
	}
	if !strings.Contains(strings.Join(to, ","), "owner@example.com") {
		t.Errorf("email went to %v", to)
	}
}

// The webhook is a machine channel, not a person being interrupted, so a manual
// run still fires it. This is the deliberate half of "no email".
func TestFailureNotify_ManualRunStillPostsTheWebhook(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-manual-webhook"
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning, Manual: true,
	})

	svc.startFailureNotifier(graph, runID, true)
	svc.bus().Publish(runID, BusEvent{Terminal: &TerminalEvent{
		JobID: runID, Status: core.JobStatusFailed,
		Error: &core.JobError{Code: "boom", Message: "nope"},
	}})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fw.mu.Lock()
		n := len(fw.received)
		fw.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("a manual run's failure did not reach the configured webhook")
}

// A manual run on an email-only flow must not even spawn the watcher: it has
// nothing left to do, and these goroutines are per-run.
func TestFailureNotify_ManualRunArmsNothingWhenEmailIsTheOnlyChannel(t *testing.T) {
	// The full harness this time: it has a job store, so the watcher's
	// race-recheck has a terminal record to find.
	svc := newFailureNotifyHarness(t)
	srv := attachOwnerEmail(t, svc, auth.User{Email: "owner@example.com"})
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		Owner:         "owner@example.com",
		FailureNotify: &core.FailureNotify{Email: "alerts@example.com"},
	}
	runID := "run-nothing-to-do"
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusFailed, Manual: true,
	})

	// Already terminal and failed: the watcher's race-recheck would fire
	// immediately if one were armed.
	svc.startFailureNotifier(graph, runID, true)
	if data, _ := waitForEmail(t, srv, 500*time.Millisecond); data != "" {
		t.Errorf("watcher armed and delivered anyway:\n%s", data)
	}
}

// ---- the flag has to survive the trip ----------------------------------

// A run can be PARKED at the concurrency limit and promoted minutes later, in a
// goroutine that never saw the person who pressed Run. That is the whole reason
// this is stored on the record rather than passed down a call stack — so the
// stored value is what these check.
func TestSubmitGraphOpts_RecordsWhoStartedTheRun(t *testing.T) {
	h := newGatewayHarness(t)
	p := core.Principal{Subject: "alice", Tenant: "t", Workspace: "ws",
		Roles: []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}}}
	g := core.Graph{
		ID: "one", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "delay", Params: map[string]any{"ms": 1}}},
	}

	manualID, err := h.svc.SubmitGraphOpts(t.Context(), p, g, SubmitOpts{Manual: true})
	if err != nil {
		t.Fatalf("submit manual: %v", err)
	}
	rec, err := h.store.Get(t.Context(), manualID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !rec.Manual {
		t.Error("a run started from the app was not recorded as manual")
	}

	// And the default is the automatic one, because every trigger path — the
	// scheduler, webhooks, forms — reaches the queue through the zero value.
	autoID, err := h.svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("submit automatic: %v", err)
	}
	auto, err := h.store.Get(t.Context(), autoID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if auto.Manual {
		t.Error("an ordinary submission was recorded as manual — every trigger would stop emailing")
	}
}

// ---- the throttle ------------------------------------------------------

// A flow that breaks usually breaks repeatedly — a poll trigger every five
// minutes against a service that is down is twelve identical failures an hour.
// Twelve identical emails teach the reader to filter the lot, so the first one
// speaks for the window and the rest are silent.

// seedFailedRun writes a terminal failed graph-record, the shape the throttle
// counts. enqueuedAt is explicit because the window is measured on it.
func seedFailedRun(t *testing.T, svc *Service, graph core.Graph, id string, enqueuedAt time.Time) {
	t.Helper()
	if err := svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: id, Kind: core.JobKindGraph, GraphID: graph.ID,
		Tenant: graph.Tenant, Workspace: graph.Workspace,
		Status: core.JobStatusFailed, EnqueuedAt: enqueuedAt,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func throttleHarness(t *testing.T) (*Service, *fakeSMTP, core.Graph) {
	t.Helper()
	svc := newFailureNotifyHarness(t)
	srv := attachOwnerEmail(t, svc, auth.User{Email: "owner@example.com"})
	return svc, srv, core.Graph{
		ID: "hourly", Tenant: "t", Workspace: "ws", Owner: "owner@example.com",
	}
}

func TestFailureEmailThrottle_FirstFailureMails(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	// The failure being reported is already terminal in the store when the
	// notification fires, so it is in the list the throttle reads — and must not
	// throttle itself.
	seedFailedRun(t, svc, graph, "run-1", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, false)

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("the first failure in the window sent no email")
	}
}

func TestFailureEmailThrottle_RepeatWithinTheWindowIsSilent(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	seedFailedRun(t, svc, graph, "run-1", time.Now().Add(-10*time.Minute))
	seedFailedRun(t, svc, graph, "run-2", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-2", ErrorMessage: "boom again",
	}, false)

	if data, _ := waitForEmail(t, srv, 700*time.Millisecond); data != "" {
		t.Errorf("a repeat failure emailed anyway:\n%s", data)
	}
}

// The rule is "no other failure in the window", not "the previous run
// succeeded" — because a flow that FLAPS (fail, succeed, fail, succeed every
// five minutes) makes every failure the first of its streak, and would defeat
// the streak version of this rule entirely.
func TestFailureEmailThrottle_CatchesAFlappingFlow(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	seedFailedRun(t, svc, graph, "run-1", time.Now().Add(-10*time.Minute))
	if err := svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: "run-2", Kind: core.JobKindGraph, GraphID: graph.ID,
		Tenant: graph.Tenant, Workspace: graph.Workspace,
		Status: core.JobStatusSucceeded, EnqueuedAt: time.Now().Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("seed success: %v", err)
	}
	seedFailedRun(t, svc, graph, "run-3", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-3", ErrorMessage: "boom",
	}, false)

	if data, _ := waitForEmail(t, srv, 700*time.Millisecond); data != "" {
		t.Errorf("a flapping flow emailed on every failure:\n%s", data)
	}
}

func TestFailureEmailThrottle_MailsAgainOnceTheWindowHasPassed(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	// Old enough to be outside the window: quiet for an hour means either fixed
	// or not running, so the next failure is news again.
	seedFailedRun(t, svc, graph, "run-old", time.Now().Add(-2*FailureEmailWindow))
	seedFailedRun(t, svc, graph, "run-new", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-new", ErrorMessage: "boom",
	}, false)

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("a failure after a quiet window sent no email")
	}
}

// One noisy flow must not silence another. The throttle is per flow, and a
// tenant-wide or workspace-wide one would lose real alerts.
func TestFailureEmailThrottle_IsPerFlow(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	noisy := core.Graph{ID: "noisy", Tenant: "t", Workspace: "ws", Owner: "owner@example.com"}
	seedFailedRun(t, svc, noisy, "noisy-1", time.Now().Add(-10*time.Minute))
	seedFailedRun(t, svc, noisy, "noisy-2", time.Now().Add(-5*time.Minute))
	seedFailedRun(t, svc, graph, "quiet-1", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "quiet-1", ErrorMessage: "boom",
	}, false)

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("another flow's failures silenced this one")
	}
}

// The webhook is a stream for machines, not a person's inbox. It is not
// throttled, and this is the test that keeps the two from being conflated.
func TestFailureEmailThrottle_DoesNotThrottleTheWebhook(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "hooked", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	seedFailedRun(t, svc, graph, "run-1", time.Now().Add(-10*time.Minute))
	seedFailedRun(t, svc, graph, "run-2", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-2", ErrorMessage: "boom",
	}, false)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fw.mu.Lock()
		n := len(fw.received)
		fw.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("a throttled email also suppressed the webhook")
}

func TestFailureEmailThrottle_TurnsOffAtZero(t *testing.T) {
	// DAZYFLOW_FAILURE_EMAIL_WINDOW=0 restores the pre-throttle behaviour, so an
	// operator who wants one mail per failure can have it.
	prev := FailureEmailWindow
	FailureEmailWindow = 0
	t.Cleanup(func() { FailureEmailWindow = prev })

	svc, srv, graph := throttleHarness(t)
	seedFailedRun(t, svc, graph, "run-1", time.Now().Add(-10*time.Minute))
	seedFailedRun(t, svc, graph, "run-2", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-2", ErrorMessage: "boom",
	}, false)

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("the throttle stayed on with the window disabled")
	}
}

// Fails OPEN. A throttle that eats an alert because the store could not answer
// is worse than one that sends a duplicate.
func TestFailureEmailThrottle_SendsWhenItCannotTell(t *testing.T) {
	svc, srv := ownerEmailHarness(t, auth.User{Email: "owner@example.com"})
	// No job store on this harness at all — the throttle has nothing to read.
	graph := core.Graph{ID: "blind", Tenant: "t", Workspace: "ws", Owner: "owner@example.com"}

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, false)

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("an unanswerable throttle check swallowed the email")
	}
}
