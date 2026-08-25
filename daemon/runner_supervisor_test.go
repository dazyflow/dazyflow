// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/engine"
)

// The property under test is that a runner which will not connect stays
// REGISTERED and visible, rather than disappearing.
//
// Registration dials, so the natural implementation — register once at boot —
// silently drops every runner that happens to be down at that moment. Its
// steps then vanish from the palette with nothing to explain them, which is
// the worst of the available failures: a flow author sees a step they built
// with simply not exist.
//
// These tests use unreachable endpoints deliberately. Standing up a real mTLS
// runner would test the gRPC handshake, which engine's own tests already cover;
// what is unique here is the bookkeeping around failure.

func supervisorFor(t *testing.T, rs *Runners) *RunnerSupervisor {
	t.Helper()
	cat := engine.NewRemoteCatalog()
	cat.DialTimeout = 250 * time.Millisecond
	t.Cleanup(func() { _ = cat.Close() })
	return NewRunnerSupervisor(rs, cat)
}

// unreachable points a runner at a port nothing is listening on.
func unreachable(t *testing.T, rs *Runners, tenant, name string) {
	t.Helper()
	r := sampleRunner(t, tenant, name)
	r.Endpoint = "127.0.0.1:1" // reserved, refuses instantly
	if err := rs.Put(t.Context(), r); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestSupervisor_DownRunnerStaysVisible(t *testing.T) {
	rs := testRunners(t)
	unreachable(t, rs, "acme", "invoices")
	sup := supervisorFor(t, rs)

	connected, err := sup.Sync(t.Context())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if connected != 0 {
		t.Fatalf("connected = %d, want 0", connected)
	}

	st := sup.Status("acme")
	if len(st) != 1 {
		t.Fatalf("status = %d entries, want the runner to still be listed", len(st))
	}
	if st[0].State != RunnerUnreachable {
		t.Errorf("state = %q, want %q", st[0].State, RunnerUnreachable)
	}
	if st[0].Error == "" {
		t.Error("no error recorded — an admin has nothing to act on")
	}
	if st[0].LastAttempt.IsZero() {
		t.Error("no attempt time recorded")
	}
}

// A down runner must not be re-dialled on every pass. Without the backoff, a
// five-second reconcile loop becomes a five-second connection storm against a
// host that is already struggling.
func TestSupervisor_BacksOffBetweenAttempts(t *testing.T) {
	rs := testRunners(t)
	unreachable(t, rs, "acme", "invoices")
	sup := supervisorFor(t, rs)

	now := time.Now()
	sup.Now = func() time.Time { return now }

	// The failure counter, not LastAttempt, is what distinguishes "did not
	// dial" from "dialled again". With a frozen clock LastAttempt is the same
	// value either way — an earlier version of this test asserted on it and
	// passed with the backoff gate deleted.
	attempts := func() int {
		t.Helper()
		st := sup.Status("acme")
		if len(st) != 1 {
			t.Fatalf("status = %d entries, want 1", len(st))
		}
		return st[0].failures
	}

	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := attempts(); got != 1 {
		t.Fatalf("failures after one sync = %d, want 1", got)
	}

	// Immediately again: the backoff should suppress the dial entirely.
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := attempts(); got != 1 {
		t.Errorf("failures = %d after a second sync inside the window — it re-dialled", got)
	}

	// Past the window, it tries again.
	now = now.Add(backoffFor(1) + time.Second)
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := attempts(); got != 2 {
		t.Errorf("failures = %d after the window elapsed, want 2", got)
	}
}

// Backoff lengthens with consecutive failures, and stops lengthening. A runner
// down for a day is being worked on; an admin who fixes it should not wait an
// hour to learn that they did.
func TestSupervisor_BackoffGrowsThenCaps(t *testing.T) {
	prev := time.Duration(0)
	for i := range runnerBackoff {
		d := backoffFor(i)
		if i > 0 && d < prev {
			t.Errorf("backoff shrank at %d: %v after %v", i, d, prev)
		}
		prev = d
	}
	capped := backoffFor(len(runnerBackoff) * 100)
	if capped != runnerBackoff[len(runnerBackoff)-1] {
		t.Errorf("backoff = %v after many failures, want it capped at %v", capped, runnerBackoff[len(runnerBackoff)-1])
	}
	if capped > 10*time.Minute {
		t.Errorf("backoff cap of %v is long enough to look broken to an admin", capped)
	}
}

// Editing a registration is almost always an attempt to fix the failure, so it
// must not have to wait out the backoff.
func TestSupervisor_ForgetClearsTheBackoff(t *testing.T) {
	rs := testRunners(t)
	unreachable(t, rs, "acme", "invoices")
	sup := supervisorFor(t, rs)

	now := time.Now()
	sup.Now = func() time.Time { return now }
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// Still inside the backoff window, so a plain Sync would not dial.
	sup.Forget("acme", "invoices")
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// Forget clears the counter too, so a retry shows up as a fresh first
	// failure rather than no change at all.
	if got := sup.Status("acme")[0].failures; got != 1 {
		t.Errorf("failures = %d, want 1 — Forget did not cause an immediate retry", got)
	}
}

// A disabled runner is a deliberate state, not a failure — it should not be
// dialled at all, and should not accrue backoff.
func TestSupervisor_DisabledRunnerIsNotDialled(t *testing.T) {
	rs := testRunners(t)
	r := sampleRunner(t, "acme", "invoices")
	r.Endpoint = "127.0.0.1:1"
	r.Enabled = false
	if err := rs.Put(t.Context(), r); err != nil {
		t.Fatalf("Put: %v", err)
	}
	sup := supervisorFor(t, rs)

	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	st := sup.Status("acme")
	if len(st) != 1 || st[0].State != RunnerDisabled {
		t.Fatalf("status = %+v, want a single disabled entry", st)
	}
	if !st[0].LastAttempt.IsZero() {
		t.Error("a disabled runner was dialled")
	}
}

// Deleting a runner should take its status with it, or the admin list grows
// entries for things that no longer exist.
func TestSupervisor_ForgetsDeletedRunners(t *testing.T) {
	rs := testRunners(t)
	unreachable(t, rs, "acme", "invoices")
	sup := supervisorFor(t, rs)
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sup.Status("acme")) != 1 {
		t.Fatal("runner not tracked")
	}

	if err := rs.Delete(t.Context(), "acme", "invoices"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := sup.Status("acme"); len(got) != 0 {
		t.Errorf("status still lists %+v after the runner was deleted", got)
	}
}

// One tenant must not see another's runners in its admin list.
func TestSupervisor_StatusIsScopedToTenant(t *testing.T) {
	rs := testRunners(t)
	unreachable(t, rs, "acme", "invoices")
	unreachable(t, rs, "globex", "billing")
	sup := supervisorFor(t, rs)
	if _, err := sup.Sync(t.Context()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	acme := sup.Status("acme")
	if len(acme) != 1 || acme[0].Name != "invoices" {
		t.Fatalf("acme sees %+v", acme)
	}
	globex := sup.Status("globex")
	if len(globex) != 1 || globex[0].Name != "billing" {
		t.Fatalf("globex sees %+v", globex)
	}
}

// An unreadable store is a real failure — nothing can be reconciled — and must
// surface rather than looking like "no runners".
func TestSupervisor_SyncReportsAStoreFailure(t *testing.T) {
	rs := testRunners(t)
	rs.Store = brokenRunnerStore{}
	sup := supervisorFor(t, rs)
	if _, err := sup.Sync(t.Context()); err == nil {
		t.Fatal("Sync hid a store failure")
	} else if !strings.Contains(err.Error(), "list runners") {
		t.Errorf("err = %v, want it to name the failing step", err)
	}
}

type brokenRunnerStore struct{ RunnerStore }

func (brokenRunnerStore) ListAll(context.Context) ([]Runner, error) {
	return nil, context.DeadlineExceeded
}
