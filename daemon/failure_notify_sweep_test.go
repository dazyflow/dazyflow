// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// storeTerminalRun writes a run in the state the dispatcher (or the reaper, or
// a cancel) would have left it, WITHOUT notifying — the state every silent
// failure was in.
func storeTerminalRun(t *testing.T, svc *Service, g core.Graph, runID string, status core.JobStatus, jerr *core.JobError) {
	t.Helper()
	payload, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	if err := svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: g.ID, NodeID: "*",
		Tenant: g.Tenant, Workspace: g.Workspace,
		Status: core.JobStatusRunning, GraphPayload: payload,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	res := &core.Result{JobID: runID, Status: core.StatusError, Error: jerr}
	if jerr == nil {
		res = &core.Result{JobID: runID, Status: core.StatusOK}
	}
	if err := svc.Jobs.Complete(t.Context(), runID, status, res); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// The point of the whole change: a run that fails with nobody watching still
// gets reported. This is the post-restart / post-reap / lease-recovery shape —
// no watcher was ever armed for this run, because the process that submitted
// it is gone.
func TestNotifySweep_ReportsARunNoWatcherWasArmedFor(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	storeTerminalRun(t, svc, graph, "run-orphan", core.JobStatusFailed,
		&core.JobError{Code: "lease_expired", Message: "worker died"})

	svc.SweepFailureNotifications(t.Context())
	fw.wait(t, 1, 2*time.Second)
}

// A second pass must not mail about the same run again: the claim is what
// makes the sweep safe to run every few seconds, on every replica.
func TestNotifySweep_DoesNotRepeatItself(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	storeTerminalRun(t, svc, graph, "run-once", core.JobStatusFailed,
		&core.JobError{Code: "boom"})

	svc.SweepFailureNotifications(t.Context())
	fw.wait(t, 1, 2*time.Second)
	for i := 0; i < 3; i++ {
		svc.SweepFailureNotifications(t.Context())
	}
	time.Sleep(200 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 1 {
		t.Fatalf("%d POSTs after four sweeps, want 1", len(fw.received))
	}
}

// #6: the platform killing a run on its wall-clock timeout terminates it as
// CANCELLED, which the old watcher ignored entirely — it only ever looked for
// `failed`. So the one failure the platform itself causes was the one nobody
// was told about, and the Runs list just said "cancelled", which reads as if
// somebody meant it.
func TestNotifySweep_ReportsATimeoutCancel(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	storeTerminalRun(t, svc, graph, "run-timeout", core.JobStatusCancelled,
		&core.JobError{Code: CancelCodeTimeout, Message: "graph timeout after 30m0s"})

	svc.SweepFailureNotifications(t.Context())
	fw.wait(t, 1, 2*time.Second)
}

// The other half of that rule: somebody stopping their own run does not need
// an email about it.
func TestNotifySweep_IgnoresAPersonsCancel(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	storeTerminalRun(t, svc, graph, "run-stopped", core.JobStatusCancelled,
		&core.JobError{Code: CancelCodeByPerson, Message: "cancelled by alice"})

	svc.SweepFailureNotifications(t.Context())
	time.Sleep(300 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("emailed about a run its own owner stopped (%d POSTs)", len(fw.received))
	}
}

func TestNotifySweep_IgnoresSuccess(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	storeTerminalRun(t, svc, graph, "run-ok", core.JobStatusSucceeded, nil)

	svc.SweepFailureNotifications(t.Context())
	time.Sleep(200 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("%d POSTs for a successful run", len(fw.received))
	}
}

// A send that fails releases its claim, so a later pass tries again — a mail
// host having a bad minute used to mean the notification was simply gone.
// Bounded, so a host that refuses forever cannot make the sweep loop forever.
func TestNotifySweep_RetriesAFailedSendThenGivesUp(t *testing.T) {
	svc := newFailureNotifyHarness(t)
	// A mailer pointed at a port nothing is listening on: every send fails.
	mailer, err := NewMailerFromURL("smtp://127.0.0.1:1?tls=none", "noreply@example.com")
	if err != nil {
		t.Fatalf("mailer: %v", err)
	}
	users, _ := auth.OpenJSONUserStore("")
	if err := users.PutUser(t.Context(), auth.User{Email: "owner@example.com"}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	svc.Mailer, svc.Users = mailer, users

	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws", Owner: "owner@example.com",
	}
	storeTerminalRun(t, svc, graph, "run-smtp-down", core.JobStatusFailed,
		&core.JobError{Code: "boom"})

	notifier, ok := svc.Jobs.(core.FailureNotifier)
	if !ok {
		t.Fatal("job store does not implement core.FailureNotifier")
	}
	_ = err
	// Each pass claims, fails to send, and releases — until the attempt
	// ceiling stops it being reconsidered.
	for i := 0; i < notifyMaxAttempts; i++ {
		svc.SweepFailureNotifications(t.Context())
	}
	claimed, err := notifier.ClaimUnnotified(t.Context(), NotifySweepLookback, notifyMaxAttempts, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("run still eligible after %d attempts; the retry is unbounded", notifyMaxAttempts)
	}
}

// A deploy after a quiet weekend must not mail about every failure since
// Friday. The lookback is what keeps the first sweep after downtime from
// being a mailstorm.
func TestNotifySweep_IgnoresRunsOlderThanTheLookback(t *testing.T) {
	fw := newFakeWebhook(t)
	svc := newFailureNotifyHarness(t)
	graph := core.Graph{
		ID: "g", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Webhook: fw.server.URL},
	}
	storeTerminalRun(t, svc, graph, "run-ancient", core.JobStatusFailed,
		&core.JobError{Code: "boom"})

	old := NotifySweepLookback
	NotifySweepLookback = time.Nanosecond
	t.Cleanup(func() { NotifySweepLookback = old })
	time.Sleep(5 * time.Millisecond)

	svc.SweepFailureNotifications(t.Context())
	time.Sleep(200 * time.Millisecond)
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if len(fw.received) != 0 {
		t.Errorf("mailed about a run older than the lookback (%d POSTs)", len(fw.received))
	}
}
