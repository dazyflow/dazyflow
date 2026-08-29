// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"github.com/jackc/pgx/v5/pgxpool"
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

// covPGPool dials the gated test database and returns a pool plus a
// cancelable context. Mirrors pgBusPool but kept separate so coverage
// tests don't depend on bus-specific cleanup ordering.
func covPGPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres coverage tests")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		cancel()
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		pool.Close()
	})
	return pool, ctx
}

// TestPgEntitlementStore_CRUD exercises the Postgres entitlement store:
// schema provisioning, built-in seeding, tier put/get/list/delete (with
// built-in protection), and entitlement put/get/list with grant overrides.
func TestPgEntitlementStore_CRUD(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS tenant_entitlements, tiers CASCADE"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	store, err := NewPgEntitlementStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgEntitlementStore: %v", err)
	}

	// Built-ins seeded.
	if tr, ok := store.GetTier(ctx, "free"); !ok || tr.Plan != PlanFree || !tr.BuiltIn {
		t.Fatalf("free tier = %+v ok=%v, want built-in free", tr, ok)
	}
	if tr, ok := store.GetTier(ctx, "pro"); !ok || tr.Plan != PlanPro {
		t.Fatalf("pro tier = %+v ok=%v, want pro", tr, ok)
	}

	// PutTier validation: empty id rejected.
	if err := store.PutTier(ctx, Tier{}); err == nil {
		t.Fatal("PutTier(empty id) = nil, want error")
	}

	// Add a custom tier; non-pro plan coerced to free.
	allowed := true
	custom := Tier{ID: "team", Name: "Team", Plan: "weird", RunsPerMonth: 100, MaxFlows: 7, PollingAllowed: &allowed}
	if err := store.PutTier(ctx, custom); err != nil {
		t.Fatalf("PutTier custom: %v", err)
	}
	got, ok := store.GetTier(ctx, "team")
	if !ok || got.Plan != PlanFree || got.RunsPerMonth != 100 || got.MaxFlows != 7 {
		t.Fatalf("custom tier = %+v ok=%v", got, ok)
	}
	if got.PollingAllowed == nil || !*got.PollingAllowed {
		t.Fatalf("custom tier polling = %v, want true", got.PollingAllowed)
	}

	// ListTiers includes built-ins + custom.
	tiers, err := store.ListTiers(ctx)
	if err != nil || len(tiers) != 3 {
		t.Fatalf("ListTiers = %d / %v, want 3", len(tiers), err)
	}

	// DeleteTier: built-ins protected, custom deletable.
	if err := store.DeleteTier(ctx, "free"); err == nil {
		t.Fatal("DeleteTier(free) = nil, want built-in protection error")
	}
	if err := store.DeleteTier(ctx, "team"); err != nil {
		t.Fatalf("DeleteTier(team): %v", err)
	}
	if _, ok := store.GetTier(ctx, "team"); ok {
		t.Fatal("team tier still present after delete")
	}

	// Entitlements: empty tenant rejected.
	if err := store.PutEntitlement(ctx, TenantEntitlement{}); err == nil {
		t.Fatal("PutEntitlement(empty tenant) = nil, want error")
	}
	runs := 42
	trial := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	ent := TenantEntitlement{
		Tenant: "acme", TierID: "pro", PlanOverride: "pro", Comped: true,
		TrialEndsAt: &trial, RunsPerMonth: &runs, Notes: "vip",
	}
	if err := store.PutEntitlement(ctx, ent); err != nil {
		t.Fatalf("PutEntitlement: %v", err)
	}
	gotEnt, ok := store.GetEntitlement(ctx, "acme")
	if !ok || gotEnt.TierID != "pro" || !gotEnt.Comped || gotEnt.Notes != "vip" {
		t.Fatalf("GetEntitlement = %+v ok=%v", gotEnt, ok)
	}
	if gotEnt.RunsPerMonth == nil || *gotEnt.RunsPerMonth != 42 {
		t.Fatalf("ent runs = %v, want 42", gotEnt.RunsPerMonth)
	}
	if gotEnt.TrialEndsAt == nil || !gotEnt.TrialEndsAt.Equal(trial) {
		t.Fatalf("ent trial = %v, want %v", gotEnt.TrialEndsAt, trial)
	}

	ents, err := store.ListEntitlements(ctx)
	if err != nil || len(ents) != 1 {
		t.Fatalf("ListEntitlements = %d / %v, want 1", len(ents), err)
	}

	// Unknown tenant: not found, no error.
	if _, ok := store.GetEntitlement(ctx, "ghost"); ok {
		t.Fatal("GetEntitlement(ghost) = ok, want not found")
	}
}

// TestPgWriteDedupeStore exercises the shared write-dedupe store: a miss, a
// recorded result round-tripping back, first-writer-wins on conflict, and a
// stale row reading as absent.
func TestPgWriteDedupeStore(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS write_dedupe"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	store, err := NewPgWriteDedupeStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgWriteDedupeStore: %v", err)
	}

	// Miss on an unknown key.
	if _, ok := store.Get(ctx, "job-1"); ok {
		t.Fatal("Get(unknown) = ok, want miss")
	}

	// Put then Get round-trips the result.
	want := core.Result{JobID: "job-1", Status: core.StatusOK,
		Output: map[string]core.Ref{"sid": {Inline: "SM123"}}}
	store.Put(ctx, "job-1", want)
	got, ok := store.Get(ctx, "job-1")
	if !ok || got.JobID != "job-1" || got.Status != core.StatusOK || got.Output["sid"].Inline != "SM123" {
		t.Fatalf("Get after Put = %+v ok=%v, want %+v", got, ok, want)
	}

	// First-writer-wins: a second Put for the same key must not overwrite.
	store.Put(ctx, "job-1", core.Result{JobID: "job-1", Status: core.StatusOK,
		Output: map[string]core.Ref{"sid": {Inline: "SM999"}}})
	if got, _ := store.Get(ctx, "job-1"); got.Output["sid"].Inline != "SM123" {
		t.Fatalf("second Put overwrote: sid=%q, want SM123", got.Output["sid"].Inline)
	}

	// A stale row reads as absent (and is dropped). Backdate past the TTL.
	if _, err := pool.Exec(ctx,
		`UPDATE write_dedupe SET stored_at = now() - $1::interval WHERE key='job-1'`,
		(pgWriteDedupeTTL + time.Minute).String()); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, ok := store.Get(ctx, "job-1"); ok {
		t.Fatal("Get(stale) = ok, want miss")
	}
}

// TestPgDropSwitchStore_Lifecycle exercises the killswitch store: schema,
// disable/enable, global vs per-tenant precedence, the in-memory Disabled
// fast path, and List.
func TestPgDropSwitchStore_Lifecycle(t *testing.T) {
	pool, ctx := covPGPool(t)
	if err := EnsurePgDropSwitchSchema(ctx, pool); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE drop_switches"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	store, err := NewPgDropSwitchStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgDropSwitchStore: %v", err)
	}

	// Validation: empty drop id.
	if err := store.Disable(ctx, DropSwitch{}); err == nil {
		t.Fatal("Disable(empty drop id) = nil, want error")
	}

	// Nothing disabled initially.
	if store.Disabled("slack.post", "acme") {
		t.Fatal("Disabled before any switch = true")
	}

	// Per-tenant switch only affects that tenant.
	if err := store.Disable(ctx, DropSwitch{DropID: "slack.post", Tenant: "acme", DisabledBy: "op", Reason: "abuse"}); err != nil {
		t.Fatalf("Disable tenant: %v", err)
	}
	if !store.Disabled("slack.post", "acme") {
		t.Fatal("Disabled(acme) = false after per-tenant switch")
	}
	if store.Disabled("slack.post", "other") {
		t.Fatal("Disabled(other) = true, per-tenant switch leaked")
	}

	// Global switch affects everyone.
	if err := store.Disable(ctx, DropSwitch{DropID: "http.request"}); err != nil {
		t.Fatalf("Disable global: %v", err)
	}
	if !store.Disabled("http.request", "anyone") || !store.Disabled("http.request", "") {
		t.Fatal("global switch not applied to all tenants")
	}

	// List returns both.
	list, err := store.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d / %v, want 2", len(list), err)
	}

	// Enable clears the per-tenant switch (idempotent).
	if err := store.Enable(ctx, "slack.post", "acme"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if store.Disabled("slack.post", "acme") {
		t.Fatal("Disabled(acme) = true after enable")
	}
	if err := store.Enable(ctx, "slack.post", "acme"); err != nil {
		t.Fatalf("Enable idempotent: %v", err)
	}
}

// TestPgAuditLog_Operations exercises the Postgres audit log: append,
// list (with tenant scoping, ordering, and limit), anonymize, prune, and
// delete-by-tenant.
func TestPgAuditLog_Operations(t *testing.T) {
	pool, ctx := covPGPool(t)
	log, err := NewPgAuditLog(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgAuditLog: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE audit_events"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	now := time.Now().UTC()
	events := []core.AuditEvent{
		{Time: now.Add(-3 * time.Minute), Tenant: "t1", Actor: "alice", Action: "graph.save", Target: "g1", Detail: "ip=1.2.3.4"},
		{Time: now.Add(-2 * time.Minute), Tenant: "t1", Actor: "bob", Action: "secret.delete", Target: "s1"},
		{Time: now.Add(-1 * time.Minute), Tenant: "t2", Actor: "alice", Action: "login", Target: ""},
	}
	for _, e := range events {
		if err := log.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// List scoped to t1, newest first.
	got, err := log.List(ctx, core.AuditQuery{Tenant: "t1"})
	if err != nil || len(got) != 2 {
		t.Fatalf("List t1 = %d / %v, want 2", len(got), err)
	}
	if got[0].Actor != "bob" {
		t.Fatalf("List order: first = %q, want bob (newest)", got[0].Actor)
	}

	// Limit caps the page; negative offset normalized.
	page, err := log.List(ctx, core.AuditQuery{Tenant: "t1", Limit: 1, Offset: -5})
	if err != nil || len(page) != 1 {
		t.Fatalf("limited list = %d / %v, want 1", len(page), err)
	}

	// AnonymizeActor scrubs alice across tenants.
	n, err := log.AnonymizeActor(ctx, "alice")
	if err != nil || n != 2 {
		t.Fatalf("AnonymizeActor = %d / %v, want 2", n, err)
	}
	t1, _ := log.List(ctx, core.AuditQuery{Tenant: "t1"})
	for _, e := range t1 {
		if e.Actor == "alice" {
			t.Fatal("alice still present after anonymize")
		}
	}

	// Prune with non-positive duration is a no-op.
	if pruned, err := log.Prune(ctx, 0, 0); err != nil || pruned != 0 {
		t.Fatalf("Prune(0) = %d / %v, want 0", pruned, err)
	}
	// Prune everything older than 1ns ago (all rows). batch defaulting path.
	if _, err := log.Prune(ctx, time.Nanosecond, 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	remaining, _ := log.List(ctx, core.AuditQuery{Tenant: "t1"})
	if len(remaining) != 0 {
		t.Fatalf("after prune t1 has %d rows, want 0", len(remaining))
	}

	// DeleteByTenant on already-empty tenant returns 0.
	if d, err := log.DeleteByTenant(ctx, "t2"); err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	} else if d < 0 {
		t.Fatalf("DeleteByTenant = %d", d)
	}
}

// TestPgAuditLog_PruneKeepsApprovals pins the one action retention must not
// reach. Retention is there to stop routine chatter accumulating; an approval
// is the record of who authorised something, and that is asked about long
// after the window closes — at Pro's 90 days a production deploy's
// authorisation is gone within a quarter, on Free within a week.
//
// The old rows here are FAR past any cutoff, so a regression that drops the
// exemption deletes the approval and this fails; it cannot pass by being
// inside the window.
func TestPgAuditLog_PruneKeepsApprovals(t *testing.T) {
	pool, ctx := covPGPool(t)
	log, err := NewPgAuditLog(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgAuditLog: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE audit_events"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	old := time.Now().UTC().Add(-365 * 24 * time.Hour)
	for _, e := range []core.AuditEvent{
		{Time: old, Tenant: "t1", Actor: "alice", Action: "approval", Target: "run1/await_1", Detail: "approve"},
		{Time: old, Tenant: "t1", Actor: "alice", Action: "graph.save", Target: "g1"},
		{Time: old, Tenant: "t1", Actor: "bob", Action: "secret.read", Target: "s1"},
	} {
		if err := log.Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	n, err := log.Prune(ctx, 24*time.Hour, 100)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d rows, want 2 (both non-approval rows, and only those)", n)
	}

	left, err := log.List(ctx, core.AuditQuery{Tenant: "t1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(left) != 1 {
		t.Fatalf("%d rows survived, want 1", len(left))
	}
	if left[0].Action != "approval" {
		t.Errorf("survivor is %q, want the approval", left[0].Action)
	}
	if left[0].Target != "run1/await_1" {
		t.Errorf("survivor target = %q, want run1/await_1 — the run it authorised", left[0].Target)
	}
}

// TestPgDropSwitchStore_DeleteByTenant covers the erasure hook against the real
// DB: per-tenant switches go, the GLOBAL switch stays, and an empty tenant is
// refused rather than matching every global row.
func TestPgDropSwitchStore_DeleteByTenant(t *testing.T) {
	pool, ctx := covPGPool(t)
	if err := EnsurePgDropSwitchSchema(ctx, pool); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE drop_switches"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	store, err := NewPgDropSwitchStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgDropSwitchStore: %v", err)
	}
	for _, sw := range []DropSwitch{
		{DropID: "http", Tenant: "doomed", DisabledBy: "ops@platform.test"},
		{DropID: "slack.post", Tenant: "doomed", DisabledBy: "ops@platform.test"},
		{DropID: "http", Tenant: "keeper", DisabledBy: "ops@platform.test"},
		{DropID: "smtp", Tenant: "", DisabledBy: "ops@platform.test"}, // global
	} {
		if err := store.Disable(ctx, sw); err != nil {
			t.Fatalf("seed %v: %v", sw, err)
		}
	}

	// An empty tenant must be refused: the WHERE clause would match exactly the
	// global switches, silently re-enabling a drop the platform turned off.
	if _, err := store.DeleteByTenant(ctx, ""); err == nil {
		t.Fatal("DeleteByTenant(\"\") = nil, want error")
	}
	if !store.Disabled("smtp", "anyone") {
		t.Fatal("the refused call still cleared the global switch")
	}

	n, err := store.DeleteByTenant(ctx, "doomed")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2", n)
	}
	// Reads go through the in-memory snapshot, so this also proves the reload
	// ran — a stale cache would keep enforcing an erased org's switches.
	if store.Disabled("http", "doomed") || store.Disabled("slack.post", "doomed") {
		t.Error("erased tenant's switches still enforced (cache not reloaded?)")
	}
	if !store.Disabled("http", "keeper") {
		t.Error("other tenant's switch was collateral damage")
	}
	if !store.Disabled("smtp", "anyone") {
		t.Error("global switch was collateral damage")
	}
}

// TestPgRunnerStore_DeleteByTenant covers the two-table transaction: runners and
// unspent registration tokens both go, scoped to one tenant.
func TestPgRunnerStore_DeleteByTenant(t *testing.T) {
	pool, ctx := covPGPool(t)
	store, err := NewPgRunnerStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgRunnerStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_runners, runner_tokens"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	for _, tn := range []string{"doomed", "keeper"} {
		if err := store.MintToken(ctx, tn, "admin@"+tn, "box", []byte("spent-"+tn), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("mint %s: %v", tn, err)
		}
		if _, err := store.RedeemToken(ctx, []byte("spent-"+tn),
			Runner{Tenant: tn, Name: "box"}, []byte("cred-"+tn)); err != nil {
			t.Fatalf("redeem %s: %v", tn, err)
		}
		if err := store.MintToken(ctx, tn, "admin@"+tn, "box2", []byte("live-"+tn), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("mint unspent %s: %v", tn, err)
		}
	}

	n, err := store.DeleteByTenant(ctx, "doomed")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1 runner", n)
	}
	if got, _ := store.List(ctx, "doomed"); len(got) != 0 {
		t.Errorf("runners survived: %v", got)
	}
	// Count tokens directly — nothing in the interface lists them.
	var tokens int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM runner_tokens WHERE tenant=$1`, "doomed").Scan(&tokens); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if tokens != 0 {
		t.Errorf("%d registration tokens survived — live credentials for an erased org", tokens)
	}

	if got, _ := store.List(ctx, "keeper"); len(got) != 1 {
		t.Errorf("other tenant's runners = %d, want 1", len(got))
	}
	if _, err := store.RedeemToken(ctx, []byte("live-keeper"),
		Runner{Tenant: "keeper", Name: "box2"}, []byte("cred-new")); err != nil {
		t.Errorf("other tenant's token stopped working: %v", err)
	}
}
