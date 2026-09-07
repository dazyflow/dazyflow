// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// A failure email used to be suppressed for a "manual" run — one someone
// started from the app — on the reasoning that they were watching the canvas
// turn red and did not need telling twice.
//
// That reasoning did not survive the ways a run actually gets started. The
// same endpoints serve dzctl, the MCP server and anybody's own cron, and all
// of them submit as Manual, so the flag meant "tell nobody" for exactly the
// unattended runs that need telling. Nothing separates those callers
// server-side either: an API-key principal and a browser-session principal
// look alike by the time a submission arrives.
//
// So the suppression is gone, and the hourly per-flow throttle carries the
// job instead: someone iterating on a broken flow gets one email an hour, not
// one per attempt. JobRecord.Manual still gates breakpoints, which is what it
// was for.

func TestFailureNotify_AppStartedRunStillEmailsTheOwner(t *testing.T) {
	svc, srv := ownerEmailHarness(t, auth.User{Email: "owner@example.com"})
	graph := core.Graph{
		ID: "daily", Name: "Daily Report", Tenant: "t", Workspace: "ws",
		Owner: "owner@example.com",
	}
	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	})

	data, to := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("no email: a run started from the app or the API must still report its failure")
	}
	if !strings.Contains(strings.Join(to, ","), "owner@example.com") {
		t.Errorf("email went to %v", to)
	}
}

func TestFailureNotify_AppStartedRunStillEmailsThePerFlowAddress(t *testing.T) {
	// The per-flow address is usually a shared inbox or an on-call alias. It
	// was suppressed for manual runs too, which meant a flow driven entirely
	// by API calls paged nobody, ever.
	svc, srv := ownerEmailHarness(t, auth.User{Email: "owner@example.com"})
	graph := core.Graph{
		ID: "daily", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Email: "alerts@example.com"},
	}
	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	})

	data, to := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("the per-flow alert address was not emailed")
	}
	if !strings.Contains(strings.Join(to, ","), "alerts@example.com") {
		t.Errorf("email went to %v", to)
	}
}

// The webhook was never suppressed, and still is not: it is a machine channel
// the flow's author wired deliberately.
func TestFailureNotify_ManualRunStillPostsTheWebhook(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	runID := "run-manual-webhook"
	terminateAndSweep(t, svc, graph, runID, core.JobStatusFailed,
		&core.JobError{Code: "boom", Message: "nope"})

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
	})

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
	})

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
	})

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
	})

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
	})

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
	})

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
	})

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
	})

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("an unanswerable throttle check swallowed the email")
	}
}

// The escalation. A flow that keeps failing used to send ONE email at the
// start of the outage and then nothing, however long it lasted: the sliding
// "any other failure in the last hour?" test is always true while a flow is
// broken. So a flow down for a week produced a single email, sent a week ago,
// and the documented fallback — somebody noticing in the Runs list — is
// exactly the assumption this whole exercise exists to doubt.
//
// The window tumbles now, so a continuing outage mails once per window, and
// the mail says how many runs failed in the window before it. "It failed" and
// "it has failed 47 times and you have not noticed" want different reactions
// and used to read identically.
func TestFailureEmailThrottle_ContinuingOutageEscalates(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	window := time.Now().Truncate(FailureEmailWindow)

	// A dozen failures in the PREVIOUS window: the outage the reader missed.
	for i := 0; i < 12; i++ {
		seedFailedRun(t, svc, graph, fmt.Sprintf("prev-%d", i),
			window.Add(-FailureEmailWindow).Add(time.Duration(i)*time.Minute))
	}
	// The first failure of the CURRENT window — still broken.
	seedFailedRun(t, svc, graph, "now-1", window.Add(time.Minute))

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "now-1", ErrorMessage: "boom",
	})

	data, _ := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("a continuing outage sent no email in the new window")
	}
	// The count has to be in the mail, or a repeat is indistinguishable from
	// a first failure and the reader has no reason to treat it differently.
	if !strings.Contains(data, "12") {
		t.Errorf("email does not say how many other runs failed:\n%s", data)
	}
}

// The first failure of an outage is not an escalation and must not claim to
// be: nothing failed before it.
func TestFailureEmailThrottle_FirstFailureSaysNothingAboutRepeats(t *testing.T) {
	svc, srv, graph := throttleHarness(t)
	seedFailedRun(t, svc, graph, "only-1", time.Now())

	svc.fireFailureNotification(t.Context(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "only-1", ErrorMessage: "boom",
	})

	data, _ := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("first failure sent no email")
	}
	if strings.Contains(data, "not a one-off") {
		t.Errorf("a first failure claimed to be a repeat:\n%s", data)
	}
}
