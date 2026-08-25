// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Registering a runner is one command with a token, so the token IS the
// security boundary. These tests are mostly about what a token can and cannot
// do: it joins exactly one organisation, exactly once, for a short while.

func testRunners(t *testing.T) *Runners {
	t.Helper()
	return &Runners{Store: NewMemRunnerStore()}
}

// register mints a token and redeems it, which is what the real flow does.
func register(t *testing.T, rs *Runners, tenant, name string, labels ...string) (Runner, string) {
	t.Helper()
	tok, err := rs.MintToken(t.Context(), tenant, "admin@"+tenant)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	r, cred, err := rs.Register(t.Context(), tok.Token, name, labels, "0.1.0")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return r, cred
}

// ---- tokens -----------------------------------------------------------

// The organisation comes from the token, never from the request. An agent says
// who it is; it does not get to say whose work queue it joins.
func TestRegister_TenantComesFromTheToken(t *testing.T) {
	rs := testRunners(t)
	r, _ := register(t, rs, "acme", "box")
	if r.Tenant != "acme" {
		t.Fatalf("tenant = %q, want the token's", r.Tenant)
	}
}

// The store is where the tenant guarantee actually lives, so test it there:
// even a caller that names a tenant gets the token's.
//
// Testing this through Runners.Register cannot fail — that method never sets
// Tenant, so an "honour the caller's tenant" mutation is a no-op. Checked.
func TestRedeemToken_OverridesAnyCallerSuppliedTenant(t *testing.T) {
	store := NewMemRunnerStore()
	_, hash, err := newRunnerSecret(runnerTokenPrefix)
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	if err := store.MintToken(t.Context(), "acme", "admin@acme", hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	// A hostile agent naming someone else's organisation.
	got, err := store.RedeemToken(t.Context(), hash,
		Runner{Name: "box", Tenant: "globex"}, []byte("cred"))
	if err != nil {
		t.Fatalf("RedeemToken: %v", err)
	}
	if got.Tenant != "acme" {
		t.Fatalf("tenant = %q, want the token's — a request must not choose", got.Tenant)
	}
}

// A registration token is pasted into a terminal, so it is the secret most
// likely to survive in a scrollback or a chat message. Using it burns it.
func TestRegister_TokenIsSingleUse(t *testing.T) {
	rs := testRunners(t)
	tok, err := rs.MintToken(t.Context(), "acme", "admin@acme")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, _, err := rs.Register(t.Context(), tok.Token, "first", nil, ""); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, _, err = rs.Register(t.Context(), tok.Token, "second", nil, "")
	if !errors.Is(err, ErrBadRunnerToken) {
		t.Fatalf("err = %v, want the token to have been spent", err)
	}
}

func TestRegister_TokenExpires(t *testing.T) {
	rs := testRunners(t)
	now := time.Now()
	rs.Now = func() time.Time { return now }
	tok, err := rs.MintToken(t.Context(), "acme", "admin@acme")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	// The store checks against the wall clock, so move the token's expiry into
	// the past by minting it in the past.
	rs.Now = func() time.Time { return now.Add(-2 * RunnerTokenTTL) }
	expired, err := rs.MintToken(t.Context(), "acme", "admin@acme")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	rs.Now = func() time.Time { return now }

	if _, _, err := rs.Register(t.Context(), expired.Token, "late", nil, ""); !errors.Is(err, ErrBadRunnerToken) {
		t.Fatalf("err = %v, want an expired token refused", err)
	}
	// A control: the fresh token still works, so this is not just refusing
	// everything.
	if _, _, err := rs.Register(t.Context(), tok.Token, "ontime", nil, ""); err != nil {
		t.Fatalf("fresh token refused: %v", err)
	}
}

func TestRegister_UnknownTokenIsRefused(t *testing.T) {
	rs := testRunners(t)
	_, _, err := rs.Register(t.Context(), "dzrt_madeitup", "box", nil, "")
	if !errors.Is(err, ErrBadRunnerToken) {
		t.Fatalf("err = %v, want ErrBadRunnerToken", err)
	}
}

// The token and the credential are different secrets with different lifetimes,
// and confusing them is a plausible mistake. A token must not authenticate.
func TestAuthenticate_RejectsARegistrationToken(t *testing.T) {
	rs := testRunners(t)
	tok, err := rs.MintToken(t.Context(), "acme", "admin@acme")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, err := rs.Authenticate(t.Context(), tok.Token); !errors.Is(err, ErrBadRunnerCredential) {
		t.Fatalf("err = %v, want a token refused as a credential", err)
	}
}

// ---- credentials ------------------------------------------------------

func TestAuthenticate_IdentifiesTheRunnerAndRecordsTheCheckIn(t *testing.T) {
	rs := testRunners(t)
	now := time.Now()
	rs.Now = func() time.Time { return now }
	_, cred := register(t, rs, "acme", "box", "linux")

	now = now.Add(time.Minute)
	r, err := rs.Authenticate(t.Context(), cred)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if r.Name != "box" || r.Tenant != "acme" {
		t.Errorf("identified %q/%q", r.Tenant, r.Name)
	}
	if !r.LastSeen.Equal(now) {
		t.Errorf("LastSeen = %v, want the check-in recorded at %v", r.LastSeen, now)
	}
}

// Deleting a runner is how a machine is revoked, so its credential has to stop
// working — otherwise "remove" would only remove it from a list.
//
// What holds this is the runner row going away: the credential index points at
// a runner that is no longer there, and the lookup fails. Removing the index
// cleanup alone does NOT break this, which was checked — that loop keeps the
// index from growing without bound, it is not the revocation.
func TestDelete_RevokesTheCredential(t *testing.T) {
	rs := testRunners(t)
	_, cred := register(t, rs, "acme", "box")
	if err := rs.Delete(t.Context(), "acme", "box"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := rs.Authenticate(t.Context(), cred); !errors.Is(err, ErrBadRunnerCredential) {
		t.Fatalf("err = %v, want the credential dead", err)
	}
}

// A rebuilt machine registers again under the same name. The new credential
// must work and the old one must not, or a decommissioned host keeps a way in.
func TestRegister_ReplacingARunnerRetiresTheOldCredential(t *testing.T) {
	rs := testRunners(t)
	_, first := register(t, rs, "acme", "box")
	_, second := register(t, rs, "acme", "box")

	if _, err := rs.Authenticate(t.Context(), second); err != nil {
		t.Fatalf("new credential refused: %v", err)
	}
	if _, err := rs.Authenticate(t.Context(), first); !errors.Is(err, ErrBadRunnerCredential) {
		t.Fatalf("err = %v, want the superseded credential dead", err)
	}
}

// Labels route work, so they have to compare like with like however the agent
// was invoked.
func TestRegister_NormalizesLabels(t *testing.T) {
	rs := testRunners(t)
	r, _ := register(t, rs, "acme", "box", "Linux", " x64 ", "linux", "")
	want := []string{"linux", "x64"}
	if strings.Join(r.Labels, ",") != strings.Join(want, ",") {
		t.Errorf("labels = %v, want %v", r.Labels, want)
	}
}

func TestRegister_RejectsBadNames(t *testing.T) {
	rs := testRunners(t)
	for _, name := range []string{"", "Has Spaces", "UPPER", "path/like", "dots.out", strings.Repeat("x", 65)} {
		tok, err := rs.MintToken(t.Context(), "acme", "admin@acme")
		if err != nil {
			t.Fatalf("MintToken: %v", err)
		}
		if _, _, err := rs.Register(t.Context(), tok.Token, name, nil, ""); err == nil {
			t.Errorf("Register accepted the name %q", name)
		}
	}
}

// "Online" is derived from the last check-in, because there is no connection to
// observe. The window is deliberately several poll intervals: an agent running
// a long script is not polling, and must not flicker to offline for doing
// exactly what it was asked.
func TestRunner_OnlineFollowsTheLastCheckIn(t *testing.T) {
	now := time.Now()
	r := Runner{LastSeen: now}
	if !r.Online(now) {
		t.Error("a runner that just checked in reads as offline")
	}
	if !r.Online(now.Add(RunnerOnlineWindow - time.Second)) {
		t.Error("a runner inside the window reads as offline")
	}
	if r.Online(now.Add(RunnerOnlineWindow + time.Second)) {
		t.Error("a runner past the window reads as online")
	}
	if (Runner{}).Online(now) {
		t.Error("a runner that has never checked in reads as online")
	}
}

// ---- the task queue ---------------------------------------------------

func TestClaim_ByName(t *testing.T) {
	q := NewMemRunnerTaskStore()
	r := Runner{Tenant: "acme", Name: "box"}
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})

	got, err := q.Claim(t.Context(), r, time.Now(), TaskLease)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.ID != "t1" || got.ClaimedBy != "box" {
		t.Errorf("claimed %+v", got)
	}
}

// A label lets a pool of interchangeable machines share a queue.
func TestClaim_ByLabel(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"linux"}, Script: "x", State: TaskQueued})

	unlabelled := Runner{Tenant: "acme", Name: "a"}
	if _, err := q.Claim(t.Context(), unlabelled, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Error("a runner without the label claimed a labelled task")
	}
	labelled := Runner{Tenant: "acme", Name: "b", Labels: []string{"linux"}}
	if _, err := q.Claim(t.Context(), labelled, time.Now(), TaskLease); err != nil {
		t.Fatalf("labelled runner refused: %v", err)
	}
}

// The whole isolation story: a script is about to run on someone's machine, so
// a task must never be claimable by another organisation's runner.
func TestClaim_NeverCrossesTenants(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})

	intruder := Runner{Tenant: "globex", Name: "box"} // same name, other org
	if _, err := q.Claim(t.Context(), intruder, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Fatal("another organisation's runner claimed the task")
	}
}

// A task with neither a name nor a label has no target. Letting anything take
// it would run someone's script on an arbitrary machine.
func TestClaim_RefusesAnUntargetedTask(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Script: "x", State: TaskQueued})
	r := Runner{Tenant: "acme", Name: "box", Labels: []string{"linux"}}
	if _, err := q.Claim(t.Context(), r, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Fatal("an untargeted task was claimed")
	}
}

// A claim is exclusive while the lease holds, so two agents polling at once do
// not both run the same script.
func TestClaim_IsExclusiveWhileLeased(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"linux"}, Script: "x", State: TaskQueued})
	a := Runner{Tenant: "acme", Name: "a", Labels: []string{"linux"}}
	b := Runner{Tenant: "acme", Name: "b", Labels: []string{"linux"}}
	now := time.Now()

	if _, err := q.Claim(t.Context(), a, now, TaskLease); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := q.Claim(t.Context(), b, now, TaskLease); !errors.Is(err, ErrNoTask) {
		t.Fatal("a second runner claimed a leased task")
	}
	// And it stays exclusive AFTER the lease lapses. This is the one place a
	// runner task deliberately differs from a daemon job: the job queue would
	// hand the work to another worker, but nobody knows how far an arbitrary
	// script got before its machine died, so re-running it is a second side
	// effect rather than a retry.
	if _, err := q.Claim(t.Context(), b, now.Add(TaskLease+time.Second), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Fatalf("a lapsed task was handed to a second runner: err = %v", err)
	}
}

// A lapsed claim has to become visibly failed, or the step waiting on it waits
// forever for a machine that is gone.
func TestFailAbandoned_CondemnsALapsedClaim(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	now := time.Now()
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"}, now, TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// Still held: nothing to condemn.
	if failed, err := q.FailAbandoned(t.Context(), "acme", "t1", now.Add(time.Second)); err != nil || failed {
		t.Fatalf("failed a task whose lease was still good: failed=%v err=%v", failed, err)
	}

	lapsed := now.Add(TaskLease + time.Second)
	failed, err := q.FailAbandoned(t.Context(), "acme", "t1", lapsed)
	if err != nil || !failed {
		t.Fatalf("FailAbandoned: failed=%v err=%v", failed, err)
	}
	got, err := q.Get(t.Context(), "acme", "t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != TaskFailed {
		t.Errorf("state = %q, want failed", got.State)
	}
	// The message has to name the machine — that is the whole actionable part.
	if got.Result == nil || !strings.Contains(got.Result.Error, "box") {
		t.Errorf("result = %+v, want an error naming the runner", got.Result)
	}
}

// The agent's real answer beats our guess that it was gone. A result can land
// between a caller noticing the lapse and acting on it, and reporting a failure
// that did not happen would be worse than the hang.
func TestFailAbandoned_LosesToAResultThatArrives(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	now := time.Now()
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"}, now, TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// The agent reports just before we condemn it.
	if err := q.Complete(t.Context(), Runner{Tenant: "acme", Name: "box"}, "t1", RunnerTaskResult{Stdout: "done"}, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	lapsed := now.Add(TaskLease + time.Second)
	if failed, err := q.FailAbandoned(t.Context(), "acme", "t1", lapsed); err != nil || failed {
		t.Fatalf("overwrote a real result: failed=%v err=%v", failed, err)
	}
	got, _ := q.Get(t.Context(), "acme", "t1")
	if got.State != TaskDone || got.Result.Stdout != "done" {
		t.Errorf("task = %+v, want the agent's own result kept", got)
	}
}

// Once condemned, a late result is refused: the step has already failed, and
// resurrecting it would report success for a run that already errored.
func TestComplete_RefusesAResultAfterWeGaveUp(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	now := time.Now()
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"}, now, TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := q.FailAbandoned(t.Context(), "acme", "t1", now.Add(TaskLease+time.Second)); err != nil {
		t.Fatalf("FailAbandoned: %v", err)
	}
	err := q.Complete(t.Context(), Runner{Tenant: "acme", Name: "box"}, "t1", RunnerTaskResult{Stdout: "late"}, now)
	if !errors.Is(err, ErrTaskNotClaimable) {
		t.Errorf("err = %v, want a late result refused", err)
	}
}

// A task that was never claimed has no lease, and "no lease" must not read as
// "expired lease" — that would condemn every task the moment it was queued.
func TestAbandoned_NeedsAnActualLapsedLease(t *testing.T) {
	now := time.Now()
	if abandoned(RunnerTask{State: TaskQueued}, now) {
		t.Error("a queued task reads as abandoned")
	}
	if abandoned(RunnerTask{State: TaskRunning}, now) {
		t.Error("a running task with no lease recorded reads as abandoned")
	}
	if abandoned(RunnerTask{State: TaskRunning, LeaseUntil: now.Add(time.Minute)}, now) {
		t.Error("a held lease reads as abandoned")
	}
	if !abandoned(RunnerTask{State: TaskRunning, LeaseUntil: now.Add(-time.Second)}, now) {
		t.Error("a lapsed lease does not read as abandoned")
	}
	if abandoned(RunnerTask{State: TaskDone, LeaseUntil: now.Add(-time.Hour)}, now) {
		t.Error("a finished task reads as abandoned")
	}
}

func TestExtend_KeepsALongTaskHeld(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	r := Runner{Tenant: "acme", Name: "box"}
	now := time.Now()
	if _, err := q.Claim(t.Context(), r, now, TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := q.Extend(t.Context(), r, "t1", now.Add(10*TaskLease), ""); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	other := Runner{Tenant: "acme", Name: "other"}
	if _, err := q.Claim(t.Context(), other, now.Add(TaskLease+time.Second), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Error("an extended lease still lapsed on the original schedule")
	}
	// Extending something you do not hold is refused.
	if err := q.Extend(t.Context(), Runner{Tenant: "acme", Name: "someone-else"}, "t1", now.Add(time.Hour), ""); !errors.Is(err, ErrTaskNotClaimable) {
		t.Errorf("err = %v, want a foreign extend refused", err)
	}
}

// A non-zero exit is a FAILED task. The step should fail the way any other
// step fails, not succeed with an error buried in its output.
func TestComplete_NonZeroExitFailsTheTask(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	r := Runner{Tenant: "acme", Name: "box"}
	if _, err := q.Claim(t.Context(), r, time.Now(), TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := q.Complete(t.Context(), r, "t1", RunnerTaskResult{ExitCode: 2, Stderr: "boom"}, time.Now()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, err := q.Get(t.Context(), "acme", "t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != TaskFailed {
		t.Errorf("state = %q, want %q", got.State, TaskFailed)
	}
}

func TestComplete_RefusesATaskYouDoNotHold(t *testing.T) {
	q := NewMemRunnerTaskStore()
	mustEnqueue(t, q, RunnerTask{ID: "t1", Tenant: "acme", Tags: []string{"box"}, Script: "x", State: TaskQueued})
	r := Runner{Tenant: "acme", Name: "box"}
	if _, err := q.Claim(t.Context(), r, time.Now(), TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	err := q.Complete(t.Context(), Runner{Tenant: "acme", Name: "impostor"}, "t1", RunnerTaskResult{}, time.Now())
	if !errors.Is(err, ErrTaskNotClaimable) {
		t.Fatalf("err = %v, want a foreign completion refused", err)
	}
}

func mustEnqueue(t *testing.T, q RunnerTaskStore, task RunnerTask) {
	t.Helper()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	if err := q.Enqueue(t.Context(), task); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

// ---- the dispatcher, end to end ---------------------------------------

// fakeAgent is the runner side of the contract: poll, run, report. Standing one
// up in-process proves the whole path — enqueue, claim, complete, and the step
// waking with the result — without the real binary existing yet.
func fakeAgent(t *testing.T, q RunnerTaskStore, r Runner, run func(RunnerTask) RunnerTaskResult) func() {
	t.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			task, err := q.Claim(context.Background(), r, time.Now(), TaskLease)
			if err != nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			_ = q.Complete(context.Background(), r, task.ID, run(task), time.Now())
		}
	}()
	return func() { close(stop); wg.Wait() }
}

func TestDispatch_RoundTrip(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	register(t, rs, "acme", "box")
	d := &RunnerDispatcher{Tasks: q, Runners: rs, PollInterval: 5 * time.Millisecond}

	var sawStdin string
	stopAgent := fakeAgent(t, q, Runner{Tenant: "acme", Name: "box"}, func(task RunnerTask) RunnerTaskResult {
		sawStdin = task.Stdin
		return RunnerTaskResult{Stdout: "42\n"}
	})
	defer stopAgent()

	var progress []string
	res, err := d.Dispatch(t.Context(), DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "./count.sh", Stdin: "input",
	}, func(m string) { progress = append(progress, m) })
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.Stdout != "42\n" {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if sawStdin != "input" {
		t.Errorf("the agent saw stdin %q", sawStdin)
	}
	// The step tells the author it is waiting, so a run that pauses on a
	// runner does not look stalled.
	if len(progress) == 0 || !strings.Contains(progress[0], "box") {
		t.Errorf("progress = %v, want it to name the runner", progress)
	}
}

// ---- item 4: a step must never simply hang ----------------------------

// dispatchWithin runs Dispatch and fails the test if it does not return in
// time.
//
// Every test below exercises a path whose whole purpose is to STOP waiting, so
// the failure mode of a regression is a hang, not a wrong answer. A hung test
// blocks until the package timeout and reports nothing about which guard broke,
// so the deadline lives here instead. The context is Background on purpose:
// only the dispatcher's own logic can end the call.
func dispatchWithin(t *testing.T, d *RunnerDispatcher, req DispatchRequest, limit time.Duration) error {
	t.Helper()
	ch := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, err := d.Dispatch(ctx, req, nil)
		ch <- err
	}()
	select {
	case err := <-ch:
		return err
	case <-time.After(limit):
		t.Fatalf("Dispatch did not return within %s — it hung instead of giving up", limit)
		return nil
	}
}

// registerStale registers a runner whose last check-in is old, which is what a
// switched-off machine looks like: still registered, not there.
func registerStale(t *testing.T, rs *Runners, tenant, name string, labels ...string) {
	t.Helper()
	rs.Now = func() time.Time { return time.Now().Add(-10 * RunnerOnlineWindow) }
	register(t, rs, tenant, name, labels...)
	rs.Now = nil
}

// The machine is registered but switched off. Waiting cannot help, and a step
// that hangs is the worst outcome — the run looks alive and the author has no
// idea their machine is down.
func TestDispatch_FailsWhenTheRunnerIsOffline(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	registerStale(t, rs, "acme", "box")
	d := &RunnerDispatcher{
		Tasks: q, Runners: rs,
		PollInterval: time.Millisecond,
		PickupGrace:  5 * time.Millisecond,
	}

	err := dispatchWithin(t, d, DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "./x.sh",
	}, 2*time.Second)
	if err == nil {
		t.Fatal("Dispatch waited forever for a machine that is switched off")
	}
	// Naming the runner is what makes it actionable.
	if !strings.Contains(err.Error(), "box") {
		t.Errorf("err = %v, want it to name the runner", err)
	}
}

// The label form has to say the same thing, because "nothing labelled build has
// checked in" is a different diagnosis from "no runner is labelled build".
func TestDispatch_FailsWhenNoLabelledRunnerIsOnline(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	registerStale(t, rs, "acme", "box", "build")
	d := &RunnerDispatcher{
		Tasks: q, Runners: rs,
		PollInterval: time.Millisecond,
		PickupGrace:  5 * time.Millisecond,
	}
	err := dispatchWithin(t, d, DispatchRequest{
		Tenant: "acme", Tags: []string{"build"}, Script: "./x.sh",
	}, 2*time.Second)
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("err = %v, want a failure naming the label", err)
	}
}

// The guard against the fix being worse than the bug.
//
// A runner working through a long script is NOT polling for new work, so a task
// queued behind it sits untouched for as long as that script runs. Its
// heartbeat keeps it online, and that is the signal that must hold the step
// open. Failing here would break every queue deeper than one task.
func TestDispatch_WaitsForABusyRunnerThatIsStillOnline(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	register(t, rs, "acme", "box") // fresh check-in: online, just not claiming
	d := &RunnerDispatcher{
		Tasks: q, Runners: rs,
		PollInterval: time.Millisecond,
		PickupGrace:  5 * time.Millisecond,
	}

	// No ceiling, so the only thing that can end this is the context — which
	// is what proves the step was still waiting.
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err := d.Dispatch(ctx, DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "./x.sh",
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the step to still be waiting on a busy online runner", err)
	}
}

// The agent claimed the work and then its machine went down. The step must fail
// — naming the machine — rather than hang, and the script must NOT be handed to
// anyone else.
func TestDispatch_FailsWhenTheRunnerVanishesMidTask(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	register(t, rs, "acme", "box")
	d := &RunnerDispatcher{Tasks: q, Runners: rs, PollInterval: time.Millisecond}

	// An agent that claims with a very short lease and then dies.
	go func() {
		r := Runner{Tenant: "acme", Name: "box"}
		for {
			if _, err := q.Claim(context.Background(), r, time.Now(), 10*time.Millisecond); err == nil {
				return // claimed, then silence
			}
			time.Sleep(time.Millisecond)
		}
	}()

	err := dispatchWithin(t, d, DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "./x.sh",
	}, 2*time.Second)
	if err == nil {
		t.Fatal("Dispatch hung on a runner that vanished mid-task")
	}
	if !strings.Contains(err.Error(), "box") {
		t.Errorf("err = %v, want it to name the runner that went quiet", err)
	}
	// And the task is closed, not waiting to be re-run by the next poller.
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"}, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Error("the abandoned script was handed out to be run a second time")
	}
}

// The end-to-end version of the same danger: after the step gives up because
// the machine was off, switching that machine on must not run the script.
func TestDispatch_GivingUpClosesTheTaskForGood(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	registerStale(t, rs, "acme", "box")
	d := &RunnerDispatcher{
		Tasks: q, Runners: rs,
		PollInterval: time.Millisecond,
		PickupGrace:  5 * time.Millisecond,
	}
	if err := dispatchWithin(t, d, DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "./invoices.sh",
	}, 2*time.Second); err == nil {
		t.Fatal("Dispatch did not give up on an offline runner")
	}

	// The machine comes back. There must be nothing waiting for it.
	if _, err := q.Claim(t.Context(), Runner{Tenant: "acme", Name: "box"},
		time.Now().Add(time.Hour), TaskLease); !errors.Is(err, ErrNoTask) {
		t.Fatal("the runner came back and claimed a script the step gave up on")
	}
}

// cancelRacingStore reports every cancel as "claimed in the meantime", which is
// the one race Dispatch must handle rather than guess at. Embedding the
// interface passes everything else through to the real queue.
type cancelRacingStore struct {
	RunnerTaskStore
	cancels atomic.Int32
}

func (s *cancelRacingStore) CancelQueued(context.Context, string, string, RunnerTaskResult, time.Time) (bool, error) {
	s.cancels.Add(1)
	return false, nil
}

// A runner that claims the work in the same instant the step gives up on it is
// there after all. The step must keep waiting for its answer, not fail beside a
// script that is about to run anyway.
func TestDispatch_KeepsWaitingIfTheTaskIsClaimedAsItGivesUp(t *testing.T) {
	q := &cancelRacingStore{RunnerTaskStore: NewMemRunnerTaskStore()}
	rs := testRunners(t)
	registerStale(t, rs, "acme", "box") // offline, so the give-up path engages
	d := &RunnerDispatcher{
		Tasks: q, Runners: rs,
		PollInterval: time.Millisecond,
		PickupGrace:  5 * time.Millisecond,
	}
	// An agent that deliberately does not claim until Dispatch has TRIED to
	// close the task. Ordering it by a sleep would be a race — the agent claims
	// in microseconds and the grace is milliseconds — and the version of this
	// test that raced passed for the wrong reason.
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		r := Runner{Tenant: "acme", Name: "box"}
		for q.cancels.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		for {
			task, err := q.Claim(context.Background(), r, time.Now(), TaskLease)
			if err == nil {
				_ = q.Complete(context.Background(), r, task.ID,
					RunnerTaskResult{Stdout: "ran anyway"}, time.Now())
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	defer func() { <-agentDone }()

	ch := make(chan RunnerTaskResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := d.Dispatch(context.Background(), DispatchRequest{
			Tenant: "acme", Tags: []string{"box"}, Script: "./x.sh",
		}, nil)
		if err != nil {
			errs <- err
			return
		}
		ch <- res
	}()
	select {
	case res := <-ch:
		if res.Stdout != "ran anyway" {
			t.Errorf("result = %+v, want the agent's answer", res)
		}
	case err := <-errs:
		t.Fatalf("gave up on a task that was claimed as it tried to close it: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch neither answered nor gave up")
	}
	// And it did try to close the task — otherwise this proves nothing.
	if q.cancels.Load() == 0 {
		t.Error("Dispatch never attempted to close the queued task")
	}
}

// The last line of defence: the step's own timeout bounds the whole wait, not
// just the script. Without it a run whose context has no deadline stays alive
// forever.
func TestDispatch_StopsAtTheCeiling(t *testing.T) {
	q := NewMemRunnerTaskStore()
	rs := testRunners(t)
	register(t, rs, "acme", "box") // online, so the offline path is not what fires
	d := &RunnerDispatcher{
		Tasks: q, Runners: rs,
		PollInterval:  time.Millisecond,
		PickupGrace:   time.Hour, // and not the pickup path either
		DispatchGrace: 20 * time.Millisecond,
	}

	err := dispatchWithin(t, d, DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "./x.sh", Timeout: 20 * time.Millisecond,
	}, 2*time.Second)
	if err == nil {
		t.Fatal("Dispatch ignored its own ceiling and waited forever")
	}
	if !strings.Contains(err.Error(), "pick") {
		t.Errorf("err = %v, want it to say nothing picked the step up", err)
	}
}

// Targeting a runner nobody registered must fail immediately with a message
// that names it. Enqueueing into a queue nothing reads would leave the run
// looking alive while nothing could ever happen.
func TestDispatch_RefusesAnUnknownRunner(t *testing.T) {
	d := &RunnerDispatcher{
		Tasks:        NewMemRunnerTaskStore(),
		Runners:      testRunners(t),
		PollInterval: 5 * time.Millisecond,
	}
	_, err := d.Dispatch(t.Context(), DispatchRequest{
		Tenant: "acme", Tags: []string{"ghost"}, Script: "x",
	}, nil)
	if err == nil {
		t.Fatal("dispatched to a runner that does not exist")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err = %v, want it to name the runner", err)
	}
}

func TestDispatch_RefusesAnUnknownLabel(t *testing.T) {
	rs := testRunners(t)
	register(t, rs, "acme", "box", "linux")
	d := &RunnerDispatcher{Tasks: NewMemRunnerTaskStore(), Runners: rs, PollInterval: 5 * time.Millisecond}
	_, err := d.Dispatch(t.Context(), DispatchRequest{
		Tenant: "acme", Tags: []string{"windows"}, Script: "x",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "windows") {
		t.Fatalf("err = %v, want it to name the label", err)
	}
}

// A cancelled run must stop waiting. Without this the step would block until
// its own timeout even though the flow is already over.
func TestDispatch_StopsWhenTheRunIsCancelled(t *testing.T) {
	rs := testRunners(t)
	register(t, rs, "acme", "box")
	d := &RunnerDispatcher{Tasks: NewMemRunnerTaskStore(), Runners: rs, PollInterval: 5 * time.Millisecond}

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	// No agent is running, so this only returns because the context ends.
	if _, err := d.Dispatch(ctx, DispatchRequest{
		Tenant: "acme", Tags: []string{"box"}, Script: "x",
	}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
}

func TestDispatch_RefusesNoTarget(t *testing.T) {
	d := &RunnerDispatcher{Tasks: NewMemRunnerTaskStore(), Runners: testRunners(t)}
	if _, err := d.Dispatch(t.Context(), DispatchRequest{Tenant: "acme", Script: "x"}, nil); err == nil {
		t.Fatal("dispatched with no runner and no label")
	}
}
