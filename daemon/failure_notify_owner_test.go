// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// attachOwnerEmail equips svc with a fake-SMTP mailer and a JSON user
// store holding one owner account, so the account-level failure-email
// path (resolve owner → check pref → send) is exercised end to end
// without a real SMTP server or Postgres. Returns the fake SMTP to
// assert on.
func attachOwnerEmail(t *testing.T, svc *Service, owner auth.User) *fakeSMTP {
	t.Helper()
	srv := newFakeSMTP(t)
	mailer, err := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	if err != nil {
		t.Fatalf("mailer: %v", err)
	}
	users, _ := auth.OpenJSONUserStore("")
	if err := users.PutUser(t.Context(), owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	svc.Mailer = mailer
	svc.Users = users
	return srv
}

// ownerEmailHarness is the bare-Service variant for tests that drive
// fireFailureNotification directly (no bus/jobs needed).
func ownerEmailHarness(t *testing.T, owner auth.User) (*Service, *fakeSMTP) {
	t.Helper()
	svc := &Service{}
	srv := attachOwnerEmail(t, svc, owner)
	return svc, srv
}

func boolPtr(b bool) *bool { return &b }

// waitForEmail polls the fake SMTP until a body arrives or the deadline
// passes; returns the accumulated data + recipients.
func waitForEmail(t *testing.T, srv *fakeSMTP, timeout time.Duration) (string, []string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, _, data, to := srv.snapshot(); data != "" {
			return data, to
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", nil
}

// An owner whose preference is on (here: the default, never set) gets an
// account-level failure email even when the graph has no per-flow
// FailureNotify config at all.
func TestFailureNotify_OwnerEmailDefaultOn(t *testing.T) {
	owner := auth.User{Email: "owner@example.com", Subject: "owner@example.com", Tenant: "t", Workspace: "ws"}
	svc, srv := ownerEmailHarness(t, owner)

	graph := core.Graph{
		ID: "daily", Name: "Daily Report", Tenant: "t", Workspace: "ws",
		Owner: "owner@example.com",
		// No FailureNotify — the account-level channel must fire on its own.
	}
	svc.fireFailureNotification(context.Background(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, false)

	data, to := waitForEmail(t, srv, 2*time.Second)
	if data == "" {
		t.Fatal("no account-level email arrived for owner with default-on pref")
	}
	if len(to) != 1 || !strings.Contains(to[0], "owner@example.com") {
		t.Errorf("RCPT TO = %v, want one to owner@example.com", to)
	}
}

// An owner who explicitly turned the failure email off gets nothing,
// and with no other channel configured no mail is sent at all.
func TestFailureNotify_OwnerEmailOptedOut(t *testing.T) {
	owner := auth.User{
		Email: "owner@example.com", Subject: "owner@example.com", Tenant: "t", Workspace: "ws",
		Notify: auth.NotifyPrefs{EmailOnFlowFailure: boolPtr(false)},
	}
	svc, srv := ownerEmailHarness(t, owner)

	graph := core.Graph{
		ID: "daily", Tenant: "t", Workspace: "ws", Owner: "owner@example.com",
	}
	svc.fireFailureNotification(context.Background(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1",
	}, false)

	if data, to := waitForEmail(t, srv, 300*time.Millisecond); data != "" {
		t.Errorf("opted-out owner still got mail: to=%v\n%s", to, data)
	}
}

// When the owner's account email equals the per-flow FailureNotify.Email,
// the owner is deduped — exactly one message, not two.
func TestFailureNotify_OwnerEmailDedupedAgainstPerFlow(t *testing.T) {
	owner := auth.User{Email: "owner@example.com", Subject: "owner@example.com", Tenant: "t", Workspace: "ws"}
	svc, srv := ownerEmailHarness(t, owner)

	graph := core.Graph{
		ID: "daily", Name: "Daily Report", Tenant: "t", Workspace: "ws",
		Owner:         "owner@example.com",
		FailureNotify: &core.FailureNotify{Email: "owner@example.com"},
	}
	svc.fireFailureNotification(context.Background(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", ErrorMessage: "boom",
	}, false)

	// Give a possible second send time to (wrongly) arrive.
	data, to := waitForEmail(t, srv, 1*time.Second)
	if data == "" {
		t.Fatal("expected one failure email")
	}
	time.Sleep(150 * time.Millisecond)
	_, _, _, to = srv.snapshot()
	if len(to) != 1 {
		t.Errorf("got %d recipients %v, want exactly 1 (owner deduped against per-flow address)", len(to), to)
	}
}

// startFailureNotifier must spawn a watcher for an owner-only graph (no
// FailureNotify) when a user store + mailer are present, so the
// account-level email can fire off the bus.
func TestFailureNotify_OwnerOnlySpawnsWatcher(t *testing.T) {
	owner := auth.User{Email: "owner@example.com", Subject: "owner@example.com", Tenant: "t", Workspace: "ws"}
	svc := newFailureNotifyHarness(t) // full Service with a real bus + jobs
	srv := attachOwnerEmail(t, svc, owner)

	graph := core.Graph{ID: "g", Tenant: "t", Workspace: "ws", Owner: "owner@example.com"}
	runID := "run-owner-only"
	_ = svc.Jobs.Enqueue(t.Context(), core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "g", Tenant: "t", Workspace: "ws",
		Status: core.JobStatusRunning,
	})

	svc.startFailureNotifier(graph, runID, false)
	svc.bus().Publish(runID, BusEvent{Terminal: &TerminalEvent{
		JobID: runID, Status: core.JobStatusFailed,
		Error: &core.JobError{Code: "x", Message: "y"},
	}})

	if data, _ := waitForEmail(t, srv, 2*time.Second); data == "" {
		t.Fatal("owner-only graph never produced an account-level email (watcher not spawned?)")
	}
}
