// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package jobstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

// testDB is the DSN for a database private to this package, derived from
// DAZYFLOW_TEST_DB, or "" when that is unset so each caller's t.Skip still
// gates. Needed because daemon's tests TRUNCATE the same `jobs` table in the
// shared database, and go test runs the two packages concurrently.
var testDBState struct {
	once sync.Once
	dsn  string
	err  error
}

func testDB(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DAZYFLOW_TEST_DB")
	if base == "" {
		return ""
	}
	testDBState.once.Do(func() {
		testDBState.dsn, testDBState.err = ownDatabase(base)
	})
	if testDBState.err != nil {
		t.Fatalf("provision this package's test database: %v", testDBState.err)
	}
	return testDBState.dsn
}

// ownDatabase creates "<base database>_jobstore" if absent and returns its DSN.
func ownDatabase(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse DAZYFLOW_TEST_DB: %w", err)
	}
	name := strings.TrimPrefix(u.Path, "/")
	if name == "" {
		return "", fmt.Errorf("DAZYFLOW_TEST_DB names no database: %q", base)
	}
	own := name + "_jobstore"
	// Interpolated into DDL below, so don't trust the DSN for it.
	if strings.ContainsFunc(own, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		return "", fmt.Errorf("database name is not a plain identifier: %q", own)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", name, err)
	}
	defer admin.Close()
	// No IF NOT EXISTS for CREATE DATABASE; a duplicate is the success case.
	if _, err := admin.Exec(ctx, `CREATE DATABASE "`+own+`"`); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P04" { // duplicate_database
			return "", fmt.Errorf("create %s: %w", own, err)
		}
	}
	u.Path = "/" + own
	return u.String(), nil
}

// Integration test against a real Postgres. Skipped unless DAZYFLOW_TEST_DB
// is set, e.g.
//
//	DAZYFLOW_TEST_DB=postgres://localhost/dazyflow_test go test ./...
func TestPostgres_RoundTrip(t *testing.T) {
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()

	// Clean slate within the test's namespace.
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")

	// Kind must be node — Claim only hands out node-kind work units
	// (graph-kind records are the parent submission, never claimed).
	rec := core.JobRecord{ID: "pg-1", Kind: core.JobKindNode, GraphID: "g", NodeID: "n", Tenant: "t"}
	if err := store.Enqueue(ctx, rec); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	claimed, err := store.Claim(ctx, "worker-1", 30*time.Second)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if claimed.ID != "pg-1" {
		t.Errorf("claimed.ID = %q", claimed.ID)
	}
	if err := store.Complete(ctx, "pg-1", core.JobStatusSucceeded, &core.Result{Status: "ok"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got, err := store.Get(ctx, "pg-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != core.JobStatusSucceeded {
		t.Errorf("status = %q", got.Status)
	}
}

// TestPostgres_MaxConcurrentPerTenant exercises the per-tenant soft cap
// against a real Postgres. Skipped unless DAZYFLOW_TEST_DB is set.
func TestPostgres_MaxConcurrentPerTenant(t *testing.T) {
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")
	store.SetMaxConcurrentPerTenant(2)

	for _, id := range []string{"a1", "a2", "a3"} {
		if err := store.Enqueue(ctx, core.JobRecord{ID: id, Kind: core.JobKindNode, Tenant: "acme"}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	// Two claims succeed, then acme is at its cap.
	if _, err := store.Claim(ctx, "w", 30*time.Second); err != nil {
		t.Fatalf("claim 1: %v", err)
	}
	if _, err := store.Claim(ctx, "w", 30*time.Second); err != nil {
		t.Fatalf("claim 2: %v", err)
	}
	if _, err := store.Claim(ctx, "w", 30*time.Second); !errors.Is(err, core.ErrNoJobs) {
		t.Fatalf("claim 3 err = %v, want ErrNoJobs (acme at cap)", err)
	}

	// A different tenant is unaffected.
	if err := store.Enqueue(ctx, core.JobRecord{ID: "b1", Kind: core.JobKindNode, Tenant: "globex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "w", 30*time.Second); err != nil {
		t.Fatalf("globex claim should succeed despite acme at cap: %v", err)
	}
}

// TestPostgres_Conformance runs the shared store-conformance suite
// against real Postgres. Each subtest truncates jobs first so they don't
// interfere with each other.
func TestPostgres_Conformance(t *testing.T) {
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(store.Close)
	runConformance(t, func(t *testing.T) core.JobStore {
		_, err := store.pool.Exec(ctx, "TRUNCATE jobs")
		if err != nil {
			t.Fatalf("TRUNCATE: %v", err)
		}
		return store
	})
}

// TestPostgres_OpenPostgres_BadDSN covers the connect / schema failure
// paths so the OpenPostgres + NewPostgresFromPool branches that return
// errors get exercised.
func TestPostgres_OpenPostgres_BadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// pgxpool.New rejects an obviously malformed DSN synchronously.
	if _, err := OpenPostgres(ctx, "not-a-valid-dsn"); err == nil {
		t.Errorf("OpenPostgres on bad DSN: want error, got nil")
	}
}

// TestPostgres_NewPostgresFromPool_NilPool exercises the nil-pool guard.
func TestPostgres_NewPostgresFromPool_NilPool(t *testing.T) {
	if _, err := NewPostgresFromPool(t.Context(), nil); err == nil {
		t.Errorf("NewPostgresFromPool(nil) = nil, want error")
	}
}

// TestPostgres_CompleteOwned_FencesNonOwner mirrors the memory test
// against real Postgres. Skipped unless DAZYFLOW_TEST_DB is set.
func TestPostgres_CompleteOwned_FencesNonOwner(t *testing.T) {
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer store.Close()
	_, _ = store.pool.Exec(ctx, "TRUNCATE jobs")

	if err := store.Enqueue(ctx, core.JobRecord{ID: "j1", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "worker-A", 30*time.Second); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.CompleteOwned(ctx, "j1", "worker-B", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("CompleteOwned by non-owner = %v, want ErrConflict", err)
	}
	if rec, _ := store.Get(ctx, "j1"); rec.Status != core.JobStatusRunning {
		t.Errorf("status = %q after fenced write, want running", rec.Status)
	}
	if err := store.CompleteOwned(ctx, "j1", "worker-A", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("owner CompleteOwned: %v", err)
	}
}

// openPG opens the Postgres store with a clean jobs table, skipping when
// DAZYFLOW_TEST_DB is unset. Returns the store and a live context.
func openPG(t *testing.T) (*Postgres, context.Context) {
	t.Helper()
	url := testDB(t)
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return store, ctx
}

// TestPostgres_PruneTerminal exercises the retention sweep: only terminal,
// old-enough rows are removed, in batches.
func TestPostgres_PruneTerminal(t *testing.T) {
	store, ctx := openPG(t)

	// olderThan <= 0 is a guarded no-op.
	if n, err := store.PruneTerminal(ctx, 0, 100); err != nil || n != 0 {
		t.Errorf("PruneTerminal(0) = %d, %v; want 0, nil", n, err)
	}

	// Two terminal rows + one still-running row.
	for _, id := range []string{"t1", "t2"} {
		if err := store.Enqueue(ctx, core.JobRecord{ID: id, Kind: core.JobKindNode, Tenant: "t"}); err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(ctx, id, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Enqueue(ctx, core.JobRecord{ID: "live", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}

	// batch=1 forces the multi-iteration loop; both terminal rows go,
	// the running row stays.
	n, err := store.PruneTerminal(ctx, time.Nanosecond, 1)
	if err != nil {
		t.Fatalf("PruneTerminal: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want 2", n)
	}
	if _, err := store.Get(ctx, "live"); err != nil {
		t.Errorf("running row should survive prune: %v", err)
	}
	if _, err := store.Get(ctx, "t1"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("t1 should be pruned: %v", err)
	}
}

// seedRun writes a graph-record plus one succeeded node-record per name, then
// backdates the run's finish to age days ago and the steps' to stepAge days
// ago. A zero age leaves the graph-record non-terminal (still parked/running).
func seedRun(t *testing.T, store *Postgres, ctx context.Context, runID string, status core.JobStatus, age, stepAge int, steps ...string) {
	t.Helper()
	// Enqueued live, then completed: Complete refuses a record that is
	// already terminal, and the finished_at it stamps is what retention reads.
	enqueued := status
	if core.IsTerminalStatus(status) {
		enqueued = core.JobStatusRunning
	}
	if err := store.Enqueue(ctx, core.JobRecord{
		ID: runID, Kind: core.JobKindGraph, GraphID: "g", NodeID: "*", Tenant: "acme",
		Status: enqueued,
	}); err != nil {
		t.Fatalf("enqueue run %s: %v", runID, err)
	}
	if core.IsTerminalStatus(status) {
		if err := store.Complete(ctx, runID, status, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatalf("complete run %s: %v", runID, err)
		}
		if _, err := store.pool.Exec(ctx,
			`UPDATE jobs SET finished_at = now() - make_interval(days => $2) WHERE id = $1`,
			runID, age); err != nil {
			t.Fatalf("age run %s: %v", runID, err)
		}
	}
	for _, n := range steps {
		id := runID + ":" + n
		if err := store.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindNode, GraphRunID: runID, GraphID: "g", NodeID: n, Tenant: "acme",
		}); err != nil {
			t.Fatalf("enqueue step %s: %v", id, err)
		}
		if err := store.Complete(ctx, id, core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
			t.Fatalf("complete step %s: %v", id, err)
		}
		if _, err := store.pool.Exec(ctx,
			`UPDATE jobs SET finished_at = now() - make_interval(days => $2) WHERE id = $1`,
			id, stepAge); err != nil {
			t.Fatalf("age step %s: %v", id, err)
		}
	}
}

func gone(t *testing.T, store *Postgres, ctx context.Context, id, why string) {
	t.Helper()
	if _, err := store.Get(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("%s should be pruned (%s): %v", id, why, err)
	}
}

func kept(t *testing.T, store *Postgres, ctx context.Context, id, why string) {
	t.Helper()
	if _, err := store.Get(ctx, id); err != nil {
		t.Errorf("%s should survive the prune (%s): %v", id, why, err)
	}
}

// TestPostgres_PruneTerminal_RunScoped pins the retention unit: a RUN, whole.
// The two cases in the middle are the ones row-scoped pruning got wrong — it
// deleted the steps of runs that were still going, and the steps of runs whose
// own finish was still inside the window.
func TestPostgres_PruneTerminal_RunScoped(t *testing.T) {
	store, ctx := openPG(t)
	const window = 30 * 24 * time.Hour

	// Finished 40 days ago: the whole thing goes.
	seedRun(t, store, ctx, "old", core.JobStatusSucceeded, 40, 41, "a", "b")
	// Parked on an approval since day 41 and STILL awaiting: nothing goes.
	seedRun(t, store, ctx, "parked", core.JobStatusAwaiting, 0, 41, "a", "b")
	// Ran for weeks and finished yesterday: its early steps are older than the
	// window, but the run is not, so the run keeps all of itself.
	seedRun(t, store, ctx, "long", core.JobStatusSucceeded, 1, 41, "a", "b")
	// Finished 40 days ago but only just: inside the window, untouched.
	seedRun(t, store, ctx, "recent", core.JobStatusSucceeded, 3, 4, "a")
	// A node-record whose run is already gone, old enough to sweep.
	if err := store.Enqueue(ctx, core.JobRecord{
		ID: "orphan", Kind: core.JobKindNode, GraphRunID: "vanished", GraphID: "g", NodeID: "a", Tenant: "acme",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, "orphan", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET finished_at = now() - interval '40 days' WHERE id = 'orphan'`); err != nil {
		t.Fatal(err)
	}

	n, err := store.PruneTerminal(ctx, window, 5000)
	if err != nil {
		t.Fatalf("PruneTerminal: %v", err)
	}
	// old (1 graph + 2 nodes) + orphan.
	if n != 4 {
		t.Errorf("pruned %d row(s), want 4", n)
	}

	gone(t, store, ctx, "old", "run finished before the cutoff")
	gone(t, store, ctx, "old:a", "its run was pruned")
	gone(t, store, ctx, "old:b", "its run was pruned")
	gone(t, store, ctx, "orphan", "no run to key retention on")

	kept(t, store, ctx, "parked", "run is still awaiting")
	kept(t, store, ctx, "parked:a", "a live run's steps are its data")
	kept(t, store, ctx, "parked:b", "a live run's steps are its data")
	kept(t, store, ctx, "long", "run finished inside the window")
	kept(t, store, ctx, "long:a", "history goes whole or not at all")
	kept(t, store, ctx, "long:b", "history goes whole or not at all")
	kept(t, store, ctx, "recent", "run finished inside the window")
	kept(t, store, ctx, "recent:a", "run finished inside the window")

	// A parked run that later finishes becomes prunable as a unit.
	if err := store.Complete(ctx, "parked", core.JobStatusSucceeded, &core.Result{Status: core.StatusOK}); err != nil {
		t.Fatalf("complete parked: %v", err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET finished_at = now() - interval '40 days' WHERE id = 'parked'`); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PruneTerminal(ctx, window, 5000); err != nil || n != 3 {
		t.Errorf("second prune = %d, %v; want 3 (graph + 2 steps), nil", n, err)
	}
	gone(t, store, ctx, "parked:a", "its run has now aged out")
}

// TestPostgres_PruneTerminal_Batches checks the run-at-a-time loop drains a
// backlog that exceeds one batch, and still deletes each run whole.
func TestPostgres_PruneTerminal_Batches(t *testing.T) {
	store, ctx := openPG(t)
	for _, id := range []string{"r1", "r2", "r3"} {
		seedRun(t, store, ctx, id, core.JobStatusSucceeded, 40, 40, "a", "b")
	}
	// batch=1 forces three iterations; 3 runs × 3 rows.
	n, err := store.PruneTerminal(ctx, 30*24*time.Hour, 1)
	if err != nil {
		t.Fatalf("PruneTerminal: %v", err)
	}
	if n != 9 {
		t.Errorf("pruned %d row(s), want 9", n)
	}
	var left int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d row(s) left behind", left)
	}
}

// TestPostgres_OldestQueuedEnqueuedAt covers the queue-latency probe.
func TestPostgres_OldestQueuedEnqueuedAt(t *testing.T) {
	store, ctx := openPG(t)

	// Empty queue reports no row.
	if _, ok, err := store.OldestQueuedEnqueuedAt(ctx); err != nil || ok {
		t.Errorf("empty queue = ok %v, err %v; want false, nil", ok, err)
	}

	// A future-available row must not count as claimable. Enqueue stores no
	// available_at (only Requeue sets it), so requeue the row into the future.
	if err := store.Enqueue(ctx, core.JobRecord{ID: "later", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Requeue(ctx, "later", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if _, ok, _ := store.OldestQueuedEnqueuedAt(ctx); ok {
		t.Errorf("future-available row counted as queued")
	}

	// An immediately-claimable row is reported.
	if err := store.Enqueue(ctx, core.JobRecord{ID: "now", Kind: core.JobKindNode, Tenant: "t"}); err != nil {
		t.Fatal(err)
	}
	at, ok, err := store.OldestQueuedEnqueuedAt(ctx)
	if err != nil || !ok {
		t.Fatalf("OldestQueuedEnqueuedAt = ok %v, err %v; want true, nil", ok, err)
	}
	if at.IsZero() {
		t.Error("enqueued_at is zero")
	}
}

// TestPostgres_ClaimUnnotified covers the failure-notification claim: what is
// eligible, that a claim is exclusive (so two replicas cannot both mail about
// one run), that a release makes it eligible again, and that attempts are
// bounded.
func TestPostgres_ClaimUnnotified(t *testing.T) {
	store, ctx := openPG(t)
	const window = time.Hour

	mk := func(id string, status core.JobStatus, code string) {
		t.Helper()
		if err := store.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindGraph, GraphID: "g", NodeID: "*", Tenant: "acme",
			Status: core.JobStatusRunning,
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
		if !core.IsTerminalStatus(status) {
			return
		}
		res := &core.Result{JobID: id, Status: core.StatusError}
		if code != "" {
			res.Error = &core.JobError{Code: code, Message: "x"}
		}
		if err := store.Complete(ctx, id, status, res); err != nil {
			t.Fatalf("complete %s: %v", id, err)
		}
	}

	mk("failed", core.JobStatusFailed, "boom")
	mk("cancelled", core.JobStatusCancelled, "cancelled")
	mk("succeeded", core.JobStatusSucceeded, "")
	mk("running", core.JobStatusRunning, "")

	claimed, err := store.ClaimUnnotified(ctx, window, 3, 50)
	if err != nil {
		t.Fatalf("ClaimUnnotified: %v", err)
	}
	got := map[string]bool{}
	for _, r := range claimed {
		got[r.ID] = true
	}
	// Both bad endings are handed over; deciding whether a cancel is worth an
	// email is the caller's job (a person's cancel is not).
	if !got["failed"] || !got["cancelled"] {
		t.Errorf("claimed %v, want the failed and cancelled runs", got)
	}
	if got["succeeded"] || got["running"] {
		t.Errorf("claimed a run that owes no notification: %v", got)
	}
	// The claim carries the payload the caller needs to build a notification.
	for _, r := range claimed {
		if r.Result == nil || r.Result.Error == nil {
			t.Errorf("claimed %s without its error; the notification has nothing to say", r.ID)
		}
		if r.FinishedAt == nil {
			t.Errorf("claimed %s without a finish time", r.ID)
		}
	}

	// Exclusive: a second sweep sees nothing.
	again, err := store.ClaimUnnotified(ctx, window, 3, 50)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second sweep re-claimed %d run(s); two replicas would both mail", len(again))
	}

	// Released (a send that failed) → eligible again.
	if err := store.ReleaseNotifyClaim(ctx, "failed"); err != nil {
		t.Fatalf("release: %v", err)
	}
	retry, err := store.ClaimUnnotified(ctx, window, 3, 50)
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if len(retry) != 1 || retry[0].ID != "failed" {
		t.Fatalf("after release got %d run(s), want just the released one", len(retry))
	}

	// Bounded: attempts have now reached 2, so a ceiling of 2 stops it.
	if err := store.ReleaseNotifyClaim(ctx, "failed"); err != nil {
		t.Fatalf("release: %v", err)
	}
	capped, err := store.ClaimUnnotified(ctx, window, 2, 50)
	if err != nil {
		t.Fatalf("capped claim: %v", err)
	}
	if len(capped) != 0 {
		t.Errorf("claimed a run past the attempt ceiling: %d", len(capped))
	}
}

// A run older than the lookback is left alone, so the first sweep after a
// weekend of downtime is not a mailstorm.
func TestPostgres_ClaimUnnotified_Lookback(t *testing.T) {
	store, ctx := openPG(t)
	if err := store.Enqueue(ctx, core.JobRecord{
		ID: "old", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*", Tenant: "acme",
		Status: core.JobStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, "old", core.JobStatusFailed,
		&core.Result{JobID: "old", Status: core.StatusError, Error: &core.JobError{Code: "boom"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE jobs SET finished_at = now() - interval '3 days' WHERE id = 'old'`); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimUnnotified(ctx, time.Hour, 3, 50)
	if err != nil {
		t.Fatalf("ClaimUnnotified: %v", err)
	}
	if len(claimed) != 0 {
		t.Errorf("claimed a 3-day-old failure under a 1-hour lookback")
	}
}
