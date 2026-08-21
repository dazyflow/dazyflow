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

	"git.sr.ht/~klahr/dazyflow/core"
)

// Gated on DAZYFLOW_TEST_DB (a real Postgres), like the other Pg*Store
// tests. The RecordAttempt SQL is the reason this exists: its two CASE
// expressions are the only place that decides whether a mirror looks healthy,
// and they can't be exercised against anything but Postgres.
func TestPgGitMirrorStore(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres git-mirror tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPgGitMirrorStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgGitMirrorStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE git_mirrors"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Absent is core.ErrNotFound, not an empty row — the handler branches on
	// it to answer configured=false.
	if _, err := store.Get(ctx, "acme", "main"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get on an unconfigured workspace = %v, want core.ErrNotFound", err)
	}

	cfg := GitMirror{
		Tenant:    "acme",
		Workspace: "main",
		RemoteURL: "git@github.com:acme/flows.git",
		Account:   "deploy",
		Enabled:   true,
		PushOn:    PushOnPublish,
		UpdatedBy: "anna@acme.com",
	}
	if err := store.Upsert(ctx, cfg); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := store.Get(ctx, "acme", "main")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RemoteURL != cfg.RemoteURL || got.Account != cfg.Account || !got.Enabled ||
		got.PushOn != PushOnPublish || got.UpdatedBy != cfg.UpdatedBy {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be stamped on write")
	}
	if got.LastAttemptAt != nil || got.LastSuccessAt != nil {
		t.Errorf("a fresh mirror should have no push history: %+v", got)
	}

	// Another workspace is a separate row — the (tenant, workspace) key is
	// the tenant-isolation boundary for this feature.
	other := cfg
	other.Tenant = "globex"
	other.RemoteURL = "git@gitlab.com:globex/flows.git"
	if err := store.Upsert(ctx, other); err != nil {
		t.Fatalf("Upsert other tenant: %v", err)
	}
	if g, err := store.Get(ctx, "acme", "main"); err != nil || g.RemoteURL != cfg.RemoteURL {
		t.Fatalf("acme's row changed when globex was written: %+v (%v)", g, err)
	}

	// A successful push advances both timestamps and the commit.
	firstSuccess := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err := store.RecordAttempt(ctx, "acme", "main", MirrorAttempt{At: firstSuccess, Commit: "cafebabe"}); err != nil {
		t.Fatalf("RecordAttempt success: %v", err)
	}
	got, err = store.Get(ctx, "acme", "main")
	if err != nil {
		t.Fatalf("Get after success: %v", err)
	}
	if got.LastError != "" || got.LastCommit != "cafebabe" {
		t.Errorf("after a success: LastError=%q LastCommit=%q", got.LastError, got.LastCommit)
	}
	if got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(firstSuccess) {
		t.Errorf("LastSuccessAt = %v, want %v", got.LastSuccessAt, firstSuccess)
	}

	// A FAILING push records the reason and moves last_attempt_at, but must
	// leave last_success_at and last_commit where they were. That pairing is
	// the whole point: it's what tells an admin the remote's copy is an hour
	// old rather than current.
	failedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.RecordAttempt(ctx, "acme", "main", MirrorAttempt{
		At:     failedAt,
		Commit: "deadbeef",
		Err:    "permission denied (publickey)",
	}); err != nil {
		t.Fatalf("RecordAttempt failure: %v", err)
	}
	got, err = store.Get(ctx, "acme", "main")
	if err != nil {
		t.Fatalf("Get after failure: %v", err)
	}
	if got.LastError != "permission denied (publickey)" {
		t.Errorf("LastError = %q, want the git failure", got.LastError)
	}
	if got.LastAttemptAt == nil || !got.LastAttemptAt.Equal(failedAt) {
		t.Errorf("LastAttemptAt = %v, want %v", got.LastAttemptAt, failedAt)
	}
	if got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(firstSuccess) {
		t.Errorf("LastSuccessAt = %v; a failed push must not advance it (want %v)", got.LastSuccessAt, firstSuccess)
	}
	if got.LastCommit != "cafebabe" {
		t.Errorf("LastCommit = %q; a failed push must not claim the remote moved", got.LastCommit)
	}

	// Upsert must not clobber the status — an admin editing the URL should
	// still see why the last push failed.
	cfg.RemoteURL = "git@github.com:acme/flows-2.git"
	cfg.Enabled = false
	if err := store.Upsert(ctx, cfg); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got, err = store.Get(ctx, "acme", "main")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.RemoteURL != cfg.RemoteURL || got.Enabled {
		t.Errorf("config update didn't take: %+v", got)
	}
	if got.LastError == "" || got.LastCommit != "cafebabe" {
		t.Errorf("config update erased push status: %+v", got)
	}

	// A transport error can carry a whole remote-side banner. The column is
	// read on every panel load, so it is bounded — and the truncation must
	// keep the FRONT, where git names the cause.
	long := "permission denied (publickey)" + strings.Repeat(" trailing noise", 500)
	if err := store.RecordAttempt(ctx, "acme", "main", MirrorAttempt{At: time.Now().UTC(), Err: long}); err != nil {
		t.Fatalf("RecordAttempt long error: %v", err)
	}
	got, err = store.Get(ctx, "acme", "main")
	if err != nil {
		t.Fatalf("Get after long error: %v", err)
	}
	if len(got.LastError) > maxMirrorErrorLen+4 {
		t.Errorf("stored error is %d bytes, want it truncated near %d", len(got.LastError), maxMirrorErrorLen)
	}
	if !strings.HasPrefix(got.LastError, "permission denied (publickey)") {
		t.Errorf("truncation dropped the cause: %q", got.LastError[:min(60, len(got.LastError))])
	}

	// RecordAttempt on a mirror that no longer exists is a no-op, not an
	// error: a push in flight can outlive a "stop mirroring".
	if err := store.RecordAttempt(ctx, "acme", "gone", MirrorAttempt{At: time.Now()}); err != nil {
		t.Errorf("RecordAttempt for a missing mirror = %v, want nil", err)
	}

	// Delete is idempotent and takes the status with it.
	if err := store.Delete(ctx, "acme", "main"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, "acme", "main"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want core.ErrNotFound", err)
	}
	if err := store.Delete(ctx, "acme", "main"); err != nil {
		t.Errorf("second Delete = %v, want nil (idempotent)", err)
	}
	// The other tenant's row survived all of it.
	if _, err := store.Get(ctx, "globex", "main"); err != nil {
		t.Errorf("globex's mirror should be untouched: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE git_mirrors"); err != nil {
		t.Fatalf("cleanup truncate: %v", err)
	}
}
