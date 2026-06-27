package daemon

import (
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// TestPgShareStore_CRUD exercises the durable ShareStore end-to-end:
// Upsert (insert + rotate), Get, Lookup, Delete, and the DeleteByTenant
// erasure-cascade hook.
func TestPgShareStore_CRUD(t *testing.T) {
	pool, ctx := covPGPool(t)
	store, err := NewPgShareStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgShareStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE workspace_shares"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Missing share -> ErrNotFound.
	if _, err := store.Get(ctx, "acme", "main"); err != core.ErrNotFound {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}

	// Insert.
	sh, err := store.Upsert(ctx, "acme", "main", "tok-1", "alice")
	if err != nil || sh.Token != "tok-1" || sh.CreatedBy != "alice" {
		t.Fatalf("Upsert = %+v / %v", sh, err)
	}

	// Get round-trips.
	got, err := store.Get(ctx, "acme", "main")
	if err != nil || got.Token != "tok-1" {
		t.Fatalf("Get = %+v / %v", got, err)
	}

	// Lookup by token.
	byTok, err := store.Lookup(ctx, "tok-1")
	if err != nil || byTok.Tenant != "acme" || byTok.Workspace != "main" {
		t.Fatalf("Lookup = %+v / %v", byTok, err)
	}
	if _, err := store.Lookup(ctx, "nope"); err != core.ErrNotFound {
		t.Fatalf("Lookup(missing) = %v, want ErrNotFound", err)
	}

	// Rotate in place (same PK, new token).
	rot, err := store.Upsert(ctx, "acme", "main", "tok-2", "bob")
	if err != nil || rot.Token != "tok-2" || rot.CreatedBy != "bob" {
		t.Fatalf("rotate = %+v / %v", rot, err)
	}
	if _, err := store.Lookup(ctx, "tok-1"); err != core.ErrNotFound {
		t.Fatalf("old token still resolvable after rotate: %v", err)
	}

	// A second workspace, so DeleteByTenant has more than one row to clear.
	_, _ = store.Upsert(ctx, "acme", "other", "tok-3", "carol")
	_, _ = store.Upsert(ctx, "elsewhere", "main", "tok-4", "dave")

	// DeleteByTenant clears only acme's shares.
	n, err := store.DeleteByTenant(ctx, "acme")
	if err != nil || n != 2 {
		t.Fatalf("DeleteByTenant = %d / %v, want 2", n, err)
	}
	if _, err := store.Get(ctx, "acme", "main"); err != core.ErrNotFound {
		t.Fatalf("acme share survived DeleteByTenant: %v", err)
	}
	if _, err := store.Get(ctx, "elsewhere", "main"); err != nil {
		t.Fatalf("other tenant's share was clobbered: %v", err)
	}

	// Delete the remaining share directly (idempotent on a missing row).
	if err := store.Delete(ctx, "elsewhere", "main"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "elsewhere", "main"); err != nil {
		t.Fatalf("Delete(idempotent): %v", err)
	}
}

// TestPgRunLogStore_TenantMethods exercises the tenant-scoped run-log methods
// that the contract test doesn't reach: DeleteRun, DeleteByTenant,
// PruneTenant, and RunLogTenants. These join the jobs table, so the test
// provisions it via the jobstore Postgres and seeds owning job records.
func TestPgRunLogStore_TenantMethods(t *testing.T) {
	pool, ctx := covPGPool(t)

	// Provision + clear the jobs table via the jobstore Postgres schema.
	js, err := jobstore.NewPostgresFromPool(ctx, pool)
	if err != nil {
		t.Fatalf("jobstore schema: %v", err)
	}
	_ = js
	if _, err := pool.Exec(ctx, "TRUNCATE jobs"); err != nil {
		t.Fatalf("truncate jobs: %v", err)
	}

	store, err := NewPgRunLogStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunLogStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE run_logs"); err != nil {
		t.Fatalf("truncate run_logs: %v", err)
	}

	// Seed two jobs owned by different tenants.
	mustEnqueue := func(id, tenant string) {
		t.Helper()
		if err := js.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindGraph, Tenant: tenant, Workspace: "ws",
			GraphID: "g", NodeID: "*", Status: core.JobStatusSucceeded,
			Job: core.Job{ID: id, GraphID: "g"},
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	mustEnqueue("run-acme", "acme")
	mustEnqueue("run-other", "elsewhere")

	old := time.Now().Add(-72 * time.Hour).UTC()
	now := time.Now().UTC()
	// Two old + one fresh line for acme; one line for elsewhere.
	for _, e := range []RunLogEntry{
		{RunID: "run-acme", TS: old, Kind: "progress", Message: "old-1"},
		{RunID: "run-acme", TS: old, Kind: "progress", Message: "old-2"},
		{RunID: "run-acme", TS: now, Kind: "terminal", Message: "fresh"},
		{RunID: "run-other", TS: old, Kind: "progress", Message: "other"},
	} {
		if err := store.AppendRunLog(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// RunLogTenants lists distinct owning tenants.
	tenants, err := store.RunLogTenants(ctx)
	if err != nil {
		t.Fatalf("RunLogTenants: %v", err)
	}
	seen := map[string]bool{}
	for _, tn := range tenants {
		seen[tn] = true
	}
	if !seen["acme"] || !seen["elsewhere"] {
		t.Fatalf("RunLogTenants = %v, want acme + elsewhere", tenants)
	}

	// PruneTenant: non-positive duration / empty tenant are no-ops.
	if n, err := store.PruneTenant(ctx, "acme", 0, 0); err != nil || n != 0 {
		t.Fatalf("PruneTenant(0 dur) = %d / %v, want 0", n, err)
	}
	if n, err := store.PruneTenant(ctx, "", time.Hour, 0); err != nil || n != 0 {
		t.Fatalf("PruneTenant(empty tenant) = %d / %v, want 0", n, err)
	}
	// PruneTenant removes only acme's OLD lines (the two), not the fresh one
	// and not elsewhere's.
	pruned, err := store.PruneTenant(ctx, "acme", 24*time.Hour, 0)
	if err != nil || pruned != 2 {
		t.Fatalf("PruneTenant = %d / %v, want 2", pruned, err)
	}
	rem, _ := store.ListRunLogs(ctx, "run-acme", 0, 0)
	if len(rem) != 1 || rem[0].Message != "fresh" {
		t.Fatalf("after PruneTenant acme has %+v, want only 'fresh'", rem)
	}
	if other, _ := store.ListRunLogs(ctx, "run-other", 0, 0); len(other) != 1 {
		t.Fatalf("elsewhere's logs were pruned: %+v", other)
	}

	// DeleteRun clears one run's lines.
	d, err := store.DeleteRun(ctx, "run-acme")
	if err != nil || d != 1 {
		t.Fatalf("DeleteRun = %d / %v, want 1", d, err)
	}

	// DeleteByTenant clears the remaining tenant's logs by jobs join.
	db, err := store.DeleteByTenant(ctx, "elsewhere")
	if err != nil || db != 1 {
		t.Fatalf("DeleteByTenant = %d / %v, want 1", db, err)
	}
}
