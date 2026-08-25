// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// One suite, both stores.
//
// The memory store and the Postgres one have to be indistinguishable, because
// the daemon picks between them at boot and every test above this file runs
// against the memory one. A behaviour that holds only in memory is a behaviour
// that does not hold in production — and the interesting half of these rules
// (single-use tokens, exclusive claims) is enforced by SQL predicates in one
// implementation and by Go under a mutex in the other, so agreement is not
// something either gets for free.

func pgRunnerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres runner tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ---- the runner registry ----------------------------------------------

func runnerStoreContract(t *testing.T, store RunnerStore) {
	t.Helper()
	ctx := context.Background()
	rs := &Runners{Store: store}

	mint := func(tenant string) string {
		t.Helper()
		tok, err := rs.MintToken(ctx, tenant, "admin@"+tenant)
		if err != nil {
			t.Fatalf("MintToken: %v", err)
		}
		return tok.Token
	}

	t.Run("the tenant comes from the token", func(t *testing.T) {
		r, cred, err := rs.Register(ctx, mint("acme"), "box", []string{"Linux", " x64 ", "linux"}, "0.1.0")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if r.Tenant != "acme" {
			t.Errorf("tenant = %q, want acme", r.Tenant)
		}
		if r.CreatedBy != "admin@acme" {
			t.Errorf("created_by = %q", r.CreatedBy)
		}
		// Labels are normalised on the way in, so routing compares like with
		// like however the agent was invoked.
		if got := strings.Join(r.Labels, ","); got != "linux,x64" {
			t.Errorf("labels = %q, want linux,x64", got)
		}
		if !strings.HasPrefix(cred, runnerCredentialPrefix) {
			t.Errorf("credential %q lacks its prefix", cred)
		}
	})

	// Going through Register cannot show this, because Register never sets a
	// tenant for the store to override — so a store that trusted the caller
	// would pass that test. This one calls the store directly with a caller
	// that has already named someone else's organisation.
	t.Run("the store overrides a caller-supplied tenant", func(t *testing.T) {
		tok := mint("acme")
		_, credHash, err := newRunnerSecret(runnerCredentialPrefix)
		if err != nil {
			t.Fatalf("newRunnerSecret: %v", err)
		}
		hostile := Runner{Tenant: "globex", Name: "hostile", LastSeen: time.Now()}
		stored, err := store.RedeemToken(ctx, hashRunnerSecret(tok), hostile, credHash)
		if err != nil {
			t.Fatalf("RedeemToken: %v", err)
		}
		if stored.Tenant != "acme" {
			t.Fatalf("tenant = %q: the caller chose its own organisation", stored.Tenant)
		}
		// And it landed in acme's list, not globex's.
		if _, err := store.Get(ctx, "globex", "hostile"); !errors.Is(err, ErrRunnerNotFound) {
			t.Errorf("the runner was written under globex (err = %v)", err)
		}
		if _, err := store.Get(ctx, "acme", "hostile"); err != nil {
			t.Errorf("the runner is not under acme: %v", err)
		}
	})

	t.Run("a token works exactly once", func(t *testing.T) {
		tok := mint("acme")
		if _, _, err := rs.Register(ctx, tok, "first", nil, "0.1.0"); err != nil {
			t.Fatalf("first Register: %v", err)
		}
		_, _, err := rs.Register(ctx, tok, "second", nil, "0.1.0")
		if !errors.Is(err, ErrBadRunnerToken) {
			t.Fatalf("err = %v, want a spent token refused", err)
		}
		// And the second machine was not created as a side effect.
		if _, err := store.Get(ctx, "acme", "second"); !errors.Is(err, ErrRunnerNotFound) {
			t.Errorf("a refused registration still created the runner (err = %v)", err)
		}
	})

	t.Run("an unknown token is refused", func(t *testing.T) {
		_, _, err := rs.Register(ctx, "dzrt_nope", "box2", nil, "0.1.0")
		if !errors.Is(err, ErrBadRunnerToken) {
			t.Errorf("err = %v, want ErrBadRunnerToken", err)
		}
	})

	t.Run("an expired token is refused", func(t *testing.T) {
		// Mint one that was already stale when it was written.
		rs.Now = func() time.Time { return time.Now().Add(-2 * RunnerTokenTTL) }
		tok := mint("acme")
		rs.Now = nil
		_, _, err := rs.Register(ctx, tok, "expired", nil, "0.1.0")
		if !errors.Is(err, ErrBadRunnerToken) {
			t.Errorf("err = %v, want an expired token refused", err)
		}
	})

	t.Run("a credential identifies its runner and records the check-in", func(t *testing.T) {
		_, cred, err := rs.Register(ctx, mint("acme"), "seen", nil, "0.1.0")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		at := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
		rs.Now = func() time.Time { return at }
		got, err := rs.Authenticate(ctx, cred)
		rs.Now = nil
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if got.Name != "seen" || got.Tenant != "acme" {
			t.Errorf("resolved to %+v", got)
		}
		// "Online" is derived from this, so it has to be persisted, not just
		// returned.
		back, err := store.Get(ctx, "acme", "seen")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !back.LastSeen.UTC().Truncate(time.Second).Equal(at) {
			t.Errorf("last_seen = %v, want %v", back.LastSeen, at)
		}
	})

	t.Run("an unknown credential resolves to nothing", func(t *testing.T) {
		_, err := rs.Authenticate(ctx, runnerCredentialPrefix+"nosuchthing")
		if !errors.Is(err, ErrBadRunnerCredential) {
			t.Errorf("err = %v, want ErrBadRunnerCredential", err)
		}
	})

	t.Run("re-registering replaces the machine and retires its credential", func(t *testing.T) {
		first, oldCred, err := rs.Register(ctx, mint("acme"), "rebuilt", []string{"old"}, "0.1.0")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		second, newCred, err := rs.Register(ctx, mint("acme"), "rebuilt", []string{"new"}, "0.2.0")
		if err != nil {
			t.Fatalf("re-Register: %v", err)
		}
		// A rebuilt machine keeps the date it first appeared.
		if !second.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("created_at moved: %v -> %v", first.CreatedAt, second.CreatedAt)
		}
		if strings.Join(second.Labels, ",") != "new" || second.Version != "0.2.0" {
			t.Errorf("replacement did not take: %+v", second)
		}
		// The old credential is dead, or a decommissioned agent could keep
		// claiming work under the replacement's name.
		if _, err := rs.Authenticate(ctx, oldCred); !errors.Is(err, ErrBadRunnerCredential) {
			t.Errorf("the retired credential still works (err = %v)", err)
		}
		if _, err := rs.Authenticate(ctx, newCred); err != nil {
			t.Errorf("the new credential does not work: %v", err)
		}
	})

	t.Run("deleting a runner revokes it", func(t *testing.T) {
		_, cred, err := rs.Register(ctx, mint("acme"), "doomed", nil, "0.1.0")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := store.Delete(ctx, "acme", "doomed"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		// Deleting is the revocation: a decommissioned machine stops being able
		// to claim work whether or not anyone remembers to stop the agent.
		if _, err := rs.Authenticate(ctx, cred); !errors.Is(err, ErrBadRunnerCredential) {
			t.Errorf("a deleted runner's credential still works (err = %v)", err)
		}
		if err := store.Delete(ctx, "acme", "doomed"); !errors.Is(err, ErrRunnerNotFound) {
			t.Errorf("second Delete: err = %v, want ErrRunnerNotFound", err)
		}
	})

	t.Run("listing is scoped to one organisation", func(t *testing.T) {
		if _, _, err := rs.Register(ctx, mint("globex"), "theirs", nil, "0.1.0"); err != nil {
			t.Fatalf("Register: %v", err)
		}
		theirs, err := store.List(ctx, "globex")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(theirs) != 1 || theirs[0].Name != "theirs" {
			t.Fatalf("globex sees %+v", theirs)
		}
		ours, err := store.List(ctx, "acme")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, r := range ours {
			if r.Name == "theirs" {
				t.Fatal("one organisation's list contains another's runner")
			}
		}
		// Sorted by name, so the admin table is stable between loads.
		for i := 1; i < len(ours); i++ {
			if ours[i-1].Name > ours[i].Name {
				t.Fatalf("list is not sorted by name: %v", ours)
			}
		}
	})

	t.Run("an unknown runner is not found", func(t *testing.T) {
		if _, err := store.Get(ctx, "acme", "ghost"); !errors.Is(err, ErrRunnerNotFound) {
			t.Errorf("err = %v, want ErrRunnerNotFound", err)
		}
		if err := store.Delete(ctx, "acme", "ghost"); !errors.Is(err, ErrRunnerNotFound) {
			t.Errorf("err = %v, want ErrRunnerNotFound", err)
		}
	})
}

func TestMemRunnerStore_Contract(t *testing.T) {
	runnerStoreContract(t, NewMemRunnerStore())
}

func TestPgRunnerStore_Contract(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	store, err := NewPgRunnerStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_runners, runner_tokens"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	runnerStoreContract(t, store)
}

// A registration has to survive the process that created it. This is the whole
// reason the Postgres store exists: with the memory one, restarting the daemon
// tells every agent in the fleet it is no longer registered — which the agent
// correctly treats as terminal, because that is what deletion looks like.
func TestPgRunnerStore_SurvivesARestart(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	first, err := NewPgRunnerStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_runners, runner_tokens"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	rs := &Runners{Store: first}
	tok, err := rs.MintToken(ctx, "acme", "admin@acme")
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, _, err := rs.Register(ctx, tok.Token, "survivor", []string{"linux"}, "0.1.0"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, cred, err := rs.Register(ctx, mustMint(t, rs, "acme"), "second", nil, "0.1.0")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A brand-new store over the same database is what a restart looks like.
	restarted, err := NewPgRunnerStore(ctx, pool)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	after := &Runners{Store: restarted}
	got, err := after.Authenticate(ctx, cred)
	if err != nil {
		t.Fatalf("the agent's credential stopped working across a restart: %v", err)
	}
	if got.Name != "second" || got.Tenant != "acme" {
		t.Errorf("resolved to %+v", got)
	}
	list, err := after.List(ctx, "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("saw %d runner(s) after a restart, want 2", len(list))
	}
}

func mustMint(t *testing.T, rs *Runners, tenant string) string {
	t.Helper()
	tok, err := rs.MintToken(t.Context(), tenant, "admin@"+tenant)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	return tok.Token
}

// ---- the task queue ---------------------------------------------------

func runnerTaskStoreContract(t *testing.T, q RunnerTaskStore) {
	t.Helper()
	ctx := context.Background()
	box := Runner{Tenant: "acme", Name: "box", Labels: []string{"linux", "build"}}

	enqueue := func(t *testing.T, task RunnerTask) {
		t.Helper()
		if task.CreatedAt.IsZero() {
			task.CreatedAt = time.Now()
		}
		if task.State == "" {
			task.State = TaskQueued
		}
		if err := q.Enqueue(ctx, task); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	t.Run("a task round-trips whole", func(t *testing.T) {
		enqueue(t, RunnerTask{
			ID: "whole", Tenant: "acme", Runner: "box", Script: "./x.sh",
			Shell: "python",
			Env:   map[string]string{"A": "1", "B": "two"}, Stdin: "on stdin",
			Timeout: 90 * time.Second,
		})
		got, err := q.Claim(ctx, box, time.Now(), TaskLease)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if got.Script != "./x.sh" || got.Stdin != "on stdin" {
			t.Errorf("task = %+v", got)
		}
		// The claim is the only place the chosen interpreter reaches the agent:
		// dropped here, a Python script runs under sh and fails as if the flow
		// author had written it wrong.
		if got.Shell != "python" {
			t.Errorf("shell = %q, want the one the step chose", got.Shell)
		}
		if got.Timeout != 90*time.Second {
			t.Errorf("timeout = %v, want 90s", got.Timeout)
		}
		if got.Env["A"] != "1" || got.Env["B"] != "two" {
			t.Errorf("env = %v", got.Env)
		}
		if err := q.Complete(ctx, box, "whole", RunnerTaskResult{Stdout: "out"}, time.Now()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		done, err := q.Get(ctx, "acme", "whole")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if done.State != TaskDone || done.Result == nil || done.Result.Stdout != "out" {
			t.Errorf("finished task = %+v", done)
		}
	})

	t.Run("a label matches any runner carrying it", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "bylabel", Tenant: "acme", Label: "build", Script: "x"})
		other := Runner{Tenant: "acme", Name: "other", Labels: []string{"windows"}}
		if _, err := q.Claim(ctx, other, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
			t.Errorf("a runner without the label claimed it (err = %v)", err)
		}
		got, err := q.Claim(ctx, box, time.Now(), TaskLease)
		if err != nil || got.ID != "bylabel" {
			t.Fatalf("labelled claim: %+v err=%v", got, err)
		}
	})

	t.Run("a name is exact", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "byname", Tenant: "acme", Runner: "box", Script: "x"})
		nearly := Runner{Tenant: "acme", Name: "box2", Labels: []string{"linux"}}
		if _, err := q.Claim(ctx, nearly, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
			t.Errorf("another machine claimed a task addressed by name (err = %v)", err)
		}
		if _, err := q.Claim(ctx, box, time.Now(), TaskLease); err != nil {
			t.Fatalf("the named runner was refused: %v", err)
		}
	})

	t.Run("nothing crosses organisations", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "ours", Tenant: "acme", Runner: "box", Script: "x"})
		intruder := Runner{Tenant: "globex", Name: "box", Labels: []string{"linux", "build"}}
		if _, err := q.Claim(ctx, intruder, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
			t.Fatal("another organisation's runner claimed the task")
		}
		// Nor can it read the task by id.
		if _, err := q.Get(ctx, "globex", "ours"); err == nil {
			t.Fatal("another organisation read the task by id")
		}
		if _, err := q.Claim(ctx, box, time.Now(), TaskLease); err != nil {
			t.Fatalf("our own runner was refused: %v", err)
		}
	})

	t.Run("an untargeted task is claimed by nobody", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "notarget", Tenant: "acme", Script: "x"})
		if _, err := q.Claim(ctx, box, time.Now(), TaskLease); !errors.Is(err, ErrNoTask) {
			t.Fatal("a task with no target was claimed")
		}
	})

	t.Run("a claim is exclusive, and stays exclusive after it lapses", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "excl", Tenant: "acme", Label: "build", Script: "x"})
		now := time.Now()
		if _, err := q.Claim(ctx, box, now, TaskLease); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		mate := Runner{Tenant: "acme", Name: "mate", Labels: []string{"build"}}
		if _, err := q.Claim(ctx, mate, now, TaskLease); !errors.Is(err, ErrNoTask) {
			t.Fatal("two runners hold the same task")
		}
		// The deliberate difference from the job queue: a lapsed claim is not
		// handed out again, because nobody knows how far the script got.
		later := now.Add(TaskLease + time.Second)
		if _, err := q.Claim(ctx, mate, later, TaskLease); !errors.Is(err, ErrNoTask) {
			t.Fatal("a lapsed task was handed out to be run a second time")
		}
	})

	t.Run("a held task can be extended, by its holder only", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "ext", Tenant: "acme", Runner: "box", Script: "x"})
		now := time.Now()
		if _, err := q.Claim(ctx, box, now, TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := q.Extend(ctx, box, "ext", now.Add(10*TaskLease), "halfway through"); err != nil {
			t.Fatalf("Extend: %v", err)
		}
		got, err := q.Get(ctx, "acme", "ext")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.LeaseUntil.After(now.Add(TaskLease)) {
			t.Errorf("lease_until = %v, want it pushed out", got.LeaseUntil)
		}
		// The line the script printed has to reach the row: the step waiting
		// for it may be on a different daemon, so there is no other channel.
		if got.Progress != "halfway through" {
			t.Errorf("progress = %q, want the reported line", got.Progress)
		}
		// A bare heartbeat must not blank out what the script last said.
		if err := q.Extend(ctx, box, "ext", now.Add(11*TaskLease), ""); err != nil {
			t.Fatalf("Extend: %v", err)
		}
		if again, _ := q.Get(ctx, "acme", "ext"); again.Progress != "halfway through" {
			t.Errorf("progress = %q after an empty ping, want it kept", again.Progress)
		}
		if err := q.Extend(ctx, Runner{Tenant: "acme", Name: "someone-else"}, "ext", now.Add(time.Hour), ""); !errors.Is(err, ErrTaskNotClaimable) {
			t.Errorf("a foreign extend was allowed (err = %v)", err)
		}
	})

	t.Run("a non-zero exit fails the task", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "nonzero", Tenant: "acme", Runner: "box", Script: "x"})
		if _, err := q.Claim(ctx, box, time.Now(), TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := q.Complete(ctx, box, "nonzero",
			RunnerTaskResult{ExitCode: 2, Stderr: "boom"}, time.Now()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		got, err := q.Get(ctx, "acme", "nonzero")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// The step should fail the way any other step fails, not succeed with
		// an error buried in its output.
		if got.State != TaskFailed {
			t.Errorf("state = %q, want failed", got.State)
		}
		if got.Result == nil || got.Result.Stderr != "boom" || got.Result.ExitCode != 2 {
			t.Errorf("result = %+v", got.Result)
		}
	})

	t.Run("a result from a runner that does not hold the task is refused", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "foreign", Tenant: "acme", Runner: "box", Script: "x"})
		if _, err := q.Claim(ctx, box, time.Now(), TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		err := q.Complete(ctx, Runner{Tenant: "acme", Name: "impostor"}, "foreign", RunnerTaskResult{Stdout: "hi"}, time.Now())
		if !errors.Is(err, ErrTaskNotClaimable) {
			t.Errorf("err = %v, want ErrTaskNotClaimable", err)
		}
	})

	// Names are only unique per organisation — tenant_runners is keyed on
	// (tenant, name) — so a same-named runner in another org is a DIFFERENT
	// machine, and the ownership check has to say so. Without the tenant
	// predicate only the task id's randomness stands between them.
	t.Run("a same-named runner in another organisation is refused", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "crosstenant", Tenant: "acme", Runner: "box", Script: "x"})
		now := time.Now()
		if _, err := q.Claim(ctx, box, now, TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		twin := Runner{Tenant: "other", Name: "box", Labels: []string{"linux", "build"}}
		if err := q.Complete(ctx, twin, "crosstenant",
			RunnerTaskResult{Stdout: "not mine to write"}, now); !errors.Is(err, ErrTaskNotClaimable) {
			t.Errorf("another org's runner wrote the result (err = %v)", err)
		}
		if err := q.Extend(ctx, twin, "crosstenant", now.Add(time.Hour), ""); !errors.Is(err, ErrTaskNotClaimable) {
			t.Errorf("another org's runner held the lease open (err = %v)", err)
		}
		got, err := q.Get(ctx, "acme", "crosstenant")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.State != TaskRunning || got.Result != nil {
			t.Errorf("task = %+v, want it untouched and still running", got)
		}
		// The real holder is unaffected.
		if err := q.Complete(ctx, box, "crosstenant", RunnerTaskResult{Stdout: "mine"}, now); err != nil {
			t.Fatalf("the holder was refused its own task: %v", err)
		}
	})

	// Both shapes the sweeper closes, listed by the same query on both stores.
	// Without this listing nothing outside the live Dispatch goroutine owns a
	// task, so a redeploy leaves a queued row claimable forever.
	//
	// Targeted at a runner of its own rather than `box`, so the rows this
	// subtest leaves behind cannot be claimed by a later one — the store is
	// shared across the whole contract.
	t.Run("orphaned tasks are listed, live ones are not", func(t *testing.T) {
		now := time.Now()
		grace := 30 * time.Second
		ceiling := time.Hour
		enqueue(t, RunnerTask{
			ID: "orph-queued", Tenant: "acme", Runner: "orphbox", Script: "x",
			Timeout: 30 * time.Second, CreatedAt: now.Add(-10 * time.Minute),
		})
		enqueue(t, RunnerTask{
			ID: "orph-live", Tenant: "acme", Runner: "orphbox", Script: "x",
			Timeout: time.Hour, CreatedAt: now,
		})
		enqueue(t, RunnerTask{
			ID: "orph-untimed", Tenant: "acme", Runner: "orphbox", Script: "x",
			CreatedAt: now.Add(-2 * time.Minute),
		})
		// Claimed with a clock three minutes ago, so its lease has lapsed by
		// the time the sweep looks.
		enqueue(t, RunnerTask{
			ID: "orph-held", Tenant: "acme", Runner: "orphheld", Script: "x",
			CreatedAt: now.Add(-3 * time.Minute),
		})
		if got, err := q.Claim(ctx, Runner{Tenant: "acme", Name: "orphheld"}, now.Add(-3*time.Minute), TaskLease); err != nil || got.ID != "orph-held" {
			t.Fatalf("Claim: %+v err=%v", got, err)
		}
		rows, err := q.OrphanedTasks(ctx, now, grace, ceiling, 100)
		if err != nil {
			t.Fatalf("OrphanedTasks: %v", err)
		}
		seen := map[string]RunnerTaskState{}
		for _, r := range rows {
			seen[r.ID] = r.State
		}
		if seen["orph-queued"] != TaskQueued {
			t.Errorf("a queued task past its own timeout was not listed (rows = %v)", seen)
		}
		if seen["orph-held"] != TaskRunning {
			t.Errorf("a running task whose lease lapsed was not listed (rows = %v)", seen)
		}
		if _, ok := seen["orph-live"]; ok {
			t.Error("a task still inside its own deadline was listed as orphaned")
		}
		if _, ok := seen["orph-untimed"]; ok {
			t.Error("an untimed task was listed before the ceiling elapsed")
		}
		// The tenant has to come back, or the sweeper cannot close the row.
		for _, r := range rows {
			if r.Tenant == "" {
				t.Errorf("listed task %q carries no tenant", r.ID)
			}
		}
	})

	t.Run("an abandoned claim is condemned, not retried", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "gone", Tenant: "acme", Runner: "box", Script: "x"})
		now := time.Now()
		if _, err := q.Claim(ctx, box, now, TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if failed, err := q.FailAbandoned(ctx, "acme", "gone", now.Add(time.Second)); err != nil || failed {
			t.Fatalf("condemned a task whose lease was good: failed=%v err=%v", failed, err)
		}
		lapsed := now.Add(TaskLease + time.Second)
		failed, err := q.FailAbandoned(ctx, "acme", "gone", lapsed)
		if err != nil || !failed {
			t.Fatalf("FailAbandoned: failed=%v err=%v", failed, err)
		}
		got, err := q.Get(ctx, "acme", "gone")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.State != TaskFailed {
			t.Errorf("state = %q, want failed", got.State)
		}
		if got.Result == nil || !strings.Contains(got.Result.Error, "box") {
			t.Errorf("result = %+v, want an error naming the runner", got.Result)
		}
		// A late result is refused: the step has already failed.
		if err := q.Complete(ctx, box, "gone", RunnerTaskResult{Stdout: "late"}, time.Now()); !errors.Is(err, ErrTaskNotClaimable) {
			t.Errorf("a late result was accepted (err = %v)", err)
		}
	})

	t.Run("a real result beats a guess that the runner was gone", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "raced", Tenant: "acme", Runner: "box", Script: "x"})
		now := time.Now()
		if _, err := q.Claim(ctx, box, now, TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := q.Complete(ctx, box, "raced", RunnerTaskResult{Stdout: "made it"}, now); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if failed, err := q.FailAbandoned(ctx, "acme", "raced", now.Add(TaskLease+time.Second)); err != nil || failed {
			t.Fatalf("overwrote a real result: failed=%v err=%v", failed, err)
		}
		got, _ := q.Get(ctx, "acme", "raced")
		if got.State != TaskDone || got.Result.Stdout != "made it" {
			t.Errorf("task = %+v, want the agent's own answer kept", got)
		}
	})

	// The scenario: a step gives up because the machine is switched off, and the
	// machine is switched on an hour later. Without this the agent would claim
	// the still-queued task and run a script whose step failed long ago.
	t.Run("a cancelled task cannot be claimed later", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "gaveup", Tenant: "acme", Runner: "box", Script: "./invoices.sh"})
		now := time.Now()
		cancelled, err := q.CancelQueued(ctx, "acme", "gaveup", cancelledResult("nobody was there"), now)
		if err != nil || !cancelled {
			t.Fatalf("CancelQueued: cancelled=%v err=%v", cancelled, err)
		}
		// An hour later the machine comes back.
		if _, err := q.Claim(ctx, box, now.Add(time.Hour), TaskLease); !errors.Is(err, ErrNoTask) {
			t.Fatal("a runner claimed a script the step had already given up on")
		}
		got, err := q.Get(ctx, "acme", "gaveup")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.State != TaskFailed || got.Result == nil ||
			!strings.Contains(got.Result.Error, "nobody was there") {
			t.Errorf("task = %+v, want it closed with the reason", got)
		}
	})

	// A claimed task belongs to the agent holding it. Cancelling it would
	// create exactly the ambiguity the lease rules exist to avoid.
	t.Run("cancelling refuses a task already claimed", func(t *testing.T) {
		enqueue(t, RunnerTask{ID: "taken", Tenant: "acme", Runner: "box", Script: "x"})
		now := time.Now()
		if _, err := q.Claim(ctx, box, now, TaskLease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		cancelled, err := q.CancelQueued(ctx, "acme", "taken", cancelledResult("too late"), now)
		if err != nil {
			t.Fatalf("CancelQueued: %v", err)
		}
		if cancelled {
			t.Fatal("cancelled a task a runner was already holding")
		}
		// And the agent can still report on it.
		if err := q.Complete(ctx, box, "taken", RunnerTaskResult{Stdout: "fine"}, now); err != nil {
			t.Errorf("the holder could no longer finish its task: %v", err)
		}
	})

	t.Run("cancelling an unknown task is an error, not a silent no-op", func(t *testing.T) {
		if _, err := q.CancelQueued(ctx, "acme", "nosuch", cancelledResult("x"), time.Now()); err == nil {
			t.Error("CancelQueued invented a task")
		}
		// And one organisation cannot close another's task.
		enqueue(t, RunnerTask{ID: "theirs", Tenant: "acme", Runner: "box", Script: "x"})
		if _, err := q.CancelQueued(ctx, "globex", "theirs", cancelledResult("x"), time.Now()); err == nil {
			t.Error("another organisation closed our task")
		}
	})

	t.Run("the queue drains oldest first", func(t *testing.T) {
		base := time.Now().Add(-time.Hour)
		enqueue(t, RunnerTask{ID: "second", Tenant: "acme", Runner: "box", Script: "x", CreatedAt: base.Add(time.Minute)})
		enqueue(t, RunnerTask{ID: "first", Tenant: "acme", Runner: "box", Script: "x", CreatedAt: base})
		got, err := q.Claim(ctx, box, time.Now(), TaskLease)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if got.ID != "first" {
			t.Errorf("claimed %q, want the oldest task first", got.ID)
		}
	})

	t.Run("an unknown task is not found", func(t *testing.T) {
		if _, err := q.Get(ctx, "acme", "nosuchtask"); err == nil {
			t.Error("Get invented a task")
		}
		if _, err := q.FailAbandoned(ctx, "acme", "nosuchtask", time.Now()); err == nil {
			t.Error("FailAbandoned invented a task")
		}
	})
}

func TestMemRunnerTaskStore_Contract(t *testing.T) {
	runnerTaskStoreContract(t, NewMemRunnerTaskStore())
}

func TestPgRunnerTaskStore_Contract(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	q, err := NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerTaskStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE runner_tasks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	runnerTaskStoreContract(t, q)
}

// Two agents polling at once must not both get the same script. In Postgres
// that is FOR UPDATE SKIP LOCKED doing the work, so it is worth proving against
// a real database rather than trusting the clause.
func TestPgRunnerTaskStore_ConcurrentClaimsDoNotOverlap(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	q, err := NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerTaskStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE runner_tasks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	const tasks = 12
	for i := range tasks {
		if err := q.Enqueue(ctx, RunnerTask{
			ID: "c" + string(rune('a'+i)), Tenant: "acme", Label: "pool",
			Script: "x", State: TaskQueued, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	const agents = 4
	type claimed struct{ id, by string }
	results := make(chan claimed, tasks*agents)
	done := make(chan struct{})
	for a := range agents {
		go func(a int) {
			r := Runner{Tenant: "acme", Name: "pool" + string(rune('0'+a)), Labels: []string{"pool"}}
			for {
				select {
				case <-done:
					return
				default:
				}
				task, err := q.Claim(ctx, r, time.Now(), TaskLease)
				if err != nil {
					return
				}
				results <- claimed{task.ID, r.Name}
			}
		}(a)
	}
	// Collect exactly `tasks` claims, then stop the agents.
	seen := map[string]string{}
	for range tasks {
		select {
		case c := <-results:
			if prev, dup := seen[c.id]; dup {
				t.Fatalf("task %s claimed twice: by %s and %s", c.id, prev, c.by)
			}
			seen[c.id] = c.by
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d tasks were claimed", len(seen), tasks)
		}
	}
	close(done)
	if len(seen) != tasks {
		t.Errorf("claimed %d distinct tasks, want %d", len(seen), tasks)
	}
}

// Terminal rows must not accumulate forever, and unfinished ones must never be
// swept: "old and still running" is a long script, and deleting it would make
// the step waiting on it fail to read its own task back.
func TestPgRunnerTaskStore_PruneKeepsLiveWork(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	q, err := NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerTaskStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE runner_tasks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	box := Runner{Tenant: "acme", Name: "box"}

	// One finished long ago, one still queued, one claimed and running.
	for _, id := range []string{"finished", "queued", "running"} {
		if err := q.Enqueue(ctx, RunnerTask{
			ID: id, Tenant: "acme", Runner: "box", Script: "x",
			State: TaskQueued, CreatedAt: old,
		}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	// "finished" is claimed and completed, back-dated.
	if _, err := q.Claim(ctx, box, old, TaskLease); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := q.Complete(ctx, box, "finished", RunnerTaskResult{Stdout: "ok"}, old); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// "queued" is next oldest; skip it and claim "running" by name.
	if _, err := pool.Exec(ctx,
		`UPDATE runner_tasks SET state='running', claimed_by='box', lease_until=$1 WHERE id='running'`,
		old.Add(TaskLease)); err != nil {
		t.Fatalf("set up running task: %v", err)
	}

	// A row that is old, stamped finished, and yet still marked running. No
	// current code path produces this — Complete and FailAbandoned both set
	// finished_at and a terminal state together — which is exactly why the
	// state filter looks redundant and must not be removed. Without it the
	// finished_at test alone would collect this row, and the step waiting on it
	// would fail to read its own task back.
	if _, err := pool.Exec(ctx, `
		INSERT INTO runner_tasks (id, tenant, runner, script, state, claimed_by, lease_until, finished_at, created_at)
		VALUES ('halfdone', 'acme', 'box', 'x', 'running', 'box', $1, $1, $1)`, old); err != nil {
		t.Fatalf("set up the half-finished row: %v", err)
	}

	n, err := q.Prune(ctx, 24*time.Hour, 500)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d row(s), want 1", n)
	}
	if _, err := q.Get(ctx, "acme", "finished"); err == nil {
		t.Error("the finished task survived the prune")
	}
	for _, id := range []string{"queued", "running", "halfdone"} {
		if _, err := q.Get(ctx, "acme", id); err != nil {
			t.Errorf("prune deleted live work (%s): %v", id, err)
		}
	}
	// A disabled retention is a no-op, not a full sweep.
	if n, err := q.Prune(ctx, 0, 500); err != nil || n != 0 {
		t.Errorf("Prune(0) = %d, %v; want a no-op", n, err)
	}
}

// The queue is a transport that happens to be durable, and what it carries is
// the script with every ${secret.…} already expanded. engine/secrets.go's
// contract is that a resolved secret "exists only in the transport.Execute
// call"; an unsealed row would hold the tenant's live credential in cleartext
// until DAZYFLOW_RUNNER_TASK_RETENTION elapsed, readable from any dump,
// replica or backup by someone with no Dazyflow permission at all.
//
// Against real Postgres because the assertion is about what is IN THE COLUMN,
// which is exactly what a store test cannot fake.
func TestPgRunnerTaskStore_SealsTheScriptAtRest(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	q, err := NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerTaskStore: %v", err)
	}
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	q.Cipher = es
	if _, err := pool.Exec(ctx, "TRUNCATE runner_tasks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	const cred = "sk_live_thisisthecredential"
	task := RunnerTask{
		ID: "sealed", Tenant: "acme", Runner: "box",
		Script: "./sync.sh --key " + cred,
		Stdin:  "invoice " + cred,
		Env:    map[string]string{"TOKEN": cred},
		State:  TaskQueued, CreatedAt: time.Now(),
	}
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	var script, stdin string
	var env []byte
	if err := pool.QueryRow(ctx,
		`SELECT script, stdin, env FROM runner_tasks WHERE id = $1`, "sealed").
		Scan(&script, &stdin, &env); err != nil {
		t.Fatalf("read the raw row: %v", err)
	}
	for name, col := range map[string]string{"script": script, "stdin": stdin, "env": string(env)} {
		if strings.Contains(col, cred) {
			t.Errorf("the %s column holds the secret in cleartext: %s", name, col)
		}
		if !strings.Contains(col, sealedPrefix) {
			t.Errorf("the %s column is not marked as sealed: %s", name, col)
		}
	}

	// And the agent still gets the real thing.
	got, err := q.Claim(ctx, Runner{Tenant: "acme", Name: "box"}, time.Now(), TaskLease)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got.Script != task.Script || got.Stdin != task.Stdin || got.Env["TOKEN"] != cred {
		t.Errorf("claimed task = %+v, want the plaintext back", got)
	}
}

// Rows written before sealing existed — and rows from a deployment with no
// master key — must still read back, or an upgrade would strand every queued
// task. The marker is what makes the two tellable apart.
func TestPgRunnerTaskStore_ReadsBackAnUnsealedRow(t *testing.T) {
	pool := pgRunnerPool(t)
	ctx := context.Background()
	plainStore, err := NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerTaskStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE runner_tasks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Written by a daemon with no cipher configured.
	if err := plainStore.Enqueue(ctx, RunnerTask{
		ID: "legacy", Tenant: "acme", Runner: "box", Script: "./old.sh",
		State: TaskQueued, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Read by one that now has one.
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	sealing, err := NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerTaskStore: %v", err)
	}
	sealing.Cipher = es
	got, err := sealing.Get(ctx, "acme", "legacy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Script != "./old.sh" {
		t.Errorf("script = %q, want the unsealed row read straight back", got.Script)
	}
}
