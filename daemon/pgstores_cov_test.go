package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
