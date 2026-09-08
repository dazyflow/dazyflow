// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPgShareStore_CRUD(t *testing.T) {
	pool, ctx := covPGPool(t)
	store, err := NewPgShareStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgShareStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE workspace_shares"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	if _, err := store.Get(ctx, "acme", "main"); err != core.ErrNotFound {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}

	sh, err := store.Upsert(ctx, "acme", "main", "tok-1", "alice")
	if err != nil || sh.Token != "tok-1" || sh.CreatedBy != "alice" {
		t.Fatalf("Upsert = %+v / %v", sh, err)
	}

	got, err := store.Get(ctx, "acme", "main")
	if err != nil || got.Token != "tok-1" {
		t.Fatalf("Get = %+v / %v", got, err)
	}

	byTok, err := store.Lookup(ctx, "tok-1")
	if err != nil || byTok.Tenant != "acme" || byTok.Workspace != "main" {
		t.Fatalf("Lookup = %+v / %v", byTok, err)
	}
	if _, err := store.Lookup(ctx, "nope"); err != core.ErrNotFound {
		t.Fatalf("Lookup(missing) = %v, want ErrNotFound", err)
	}

	rot, err := store.Upsert(ctx, "acme", "main", "tok-2", "bob")
	if err != nil || rot.Token != "tok-2" || rot.CreatedBy != "bob" {
		t.Fatalf("rotate = %+v / %v", rot, err)
	}
	if _, err := store.Lookup(ctx, "tok-1"); err != core.ErrNotFound {
		t.Fatalf("old token still resolvable after rotate: %v", err)
	}

	_, _ = store.Upsert(ctx, "acme", "other", "tok-3", "carol")
	_, _ = store.Upsert(ctx, "elsewhere", "main", "tok-4", "dave")

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

	if err := store.Delete(ctx, "elsewhere", "main"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "elsewhere", "main"); err != nil {
		t.Fatalf("Delete(idempotent): %v", err)
	}
}

func TestPgRunLogStore_TenantMethods(t *testing.T) {
	pool, ctx := covPGPool(t)

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

	old := time.Now().Add(-72 * time.Hour).UTC()
	now := time.Now().UTC()
	mustEnqueue := func(id, tenant string, finishedAt *time.Time) {
		t.Helper()
		status := core.JobStatusRunning
		if finishedAt != nil {
			status = core.JobStatusSucceeded
		}
		if err := js.Enqueue(ctx, core.JobRecord{
			ID: id, Kind: core.JobKindGraph, Tenant: tenant, Workspace: "ws",
			GraphID: "g", NodeID: "*", Status: status,
			Job: core.Job{ID: id, GraphID: "g"},
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
		if finishedAt != nil {
			if _, err := pool.Exec(ctx,
				`UPDATE jobs SET status = 'succeeded', finished_at = $2 WHERE id = $1`,
				id, *finishedAt); err != nil {
				t.Fatalf("backdate %s: %v", id, err)
			}
		}
	}
	mustEnqueue("run-acme", "acme", &old)  // finished 72h ago
	mustEnqueue("run-parked", "acme", nil) // still running (an approval)
	mustEnqueue("run-other", "elsewhere", &old)

	for _, e := range []RunLogEntry{
		{RunID: "run-acme", TS: old, Kind: "progress", Message: "old-1"},
		{RunID: "run-acme", TS: old, Kind: "progress", Message: "old-2"},
		{RunID: "run-acme", TS: now, Kind: "terminal", Message: "fresh"},
		{RunID: "run-parked", TS: old, Kind: "progress", Message: "parked"},
		{RunID: "run-other", TS: old, Kind: "progress", Message: "other"},
	} {
		if err := store.AppendRunLog(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

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

	if n, err := store.PruneTenant(ctx, "acme", 0, 0); err != nil || n != 0 {
		t.Fatalf("PruneTenant(0 dur) = %d / %v, want 0", n, err)
	}
	if n, err := store.PruneTenant(ctx, "", time.Hour, 0); err != nil || n != 0 {
		t.Fatalf("PruneTenant(empty tenant) = %d / %v, want 0", n, err)
	}
	pruned, err := store.PruneTenant(ctx, "acme", 24*time.Hour, 0)
	if err != nil || pruned != 3 {
		t.Fatalf("PruneTenant = %d / %v, want 3", pruned, err)
	}
	if rem, _ := store.ListRunLogs(ctx, "run-acme", 0, 0); len(rem) != 0 {
		t.Fatalf("after PruneTenant acme's finished run has %+v, want none", rem)
	}
	if parked, _ := store.ListRunLogs(ctx, "run-parked", 0, 0); len(parked) != 1 {
		t.Fatalf("a run that has not finished lost its log: %+v", parked)
	}
	if other, _ := store.ListRunLogs(ctx, "run-other", 0, 0); len(other) != 1 {
		t.Fatalf("elsewhere's logs were pruned: %+v", other)
	}

	d, err := store.DeleteRun(ctx, "run-parked")
	if err != nil || d != 1 {
		t.Fatalf("DeleteRun = %d / %v, want 1", d, err)
	}

	db, err := store.DeleteByTenant(ctx, "elsewhere")
	if err != nil || db != 1 {
		t.Fatalf("DeleteByTenant = %d / %v, want 1", db, err)
	}
}

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

func TestPgEntitlementStore_CRUD(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS tenant_entitlements, tiers CASCADE"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	store, err := NewPgEntitlementStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgEntitlementStore: %v", err)
	}

	if tr, ok := store.GetTier(ctx, "free"); !ok || tr.Plan != PlanFree || !tr.BuiltIn {
		t.Fatalf("free tier = %+v ok=%v, want built-in free", tr, ok)
	}
	if tr, ok := store.GetTier(ctx, "pro"); !ok || tr.Plan != PlanPro {
		t.Fatalf("pro tier = %+v ok=%v, want pro", tr, ok)
	}

	if err := store.PutTier(ctx, Tier{}); err == nil {
		t.Fatal("PutTier(empty id) = nil, want error")
	}

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

	tiers, err := store.ListTiers(ctx)
	if err != nil || len(tiers) != 3 {
		t.Fatalf("ListTiers = %d / %v, want 3", len(tiers), err)
	}

	if err := store.DeleteTier(ctx, "free"); err == nil {
		t.Fatal("DeleteTier(free) = nil, want built-in protection error")
	}
	if err := store.DeleteTier(ctx, "team"); err != nil {
		t.Fatalf("DeleteTier(team): %v", err)
	}
	if _, ok := store.GetTier(ctx, "team"); ok {
		t.Fatal("team tier still present after delete")
	}

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

	if _, ok := store.GetEntitlement(ctx, "ghost"); ok {
		t.Fatal("GetEntitlement(ghost) = ok, want not found")
	}
}

// Exercises the shared write-dedupe store: a miss, a recorded result round-
// tripping back, first-writer-wins on conflict, and a stale row reading as
// absent.
func TestPgWriteDedupeStore(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS write_dedupe"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	store, err := NewPgWriteDedupeStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgWriteDedupeStore: %v", err)
	}

	if _, ok := store.Get(ctx, "job-1"); ok {
		t.Fatal("Get(unknown) = ok, want miss")
	}

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

	if err := store.Disable(ctx, DropSwitch{}); err == nil {
		t.Fatal("Disable(empty drop id) = nil, want error")
	}

	if store.Disabled("slack.post", "acme") {
		t.Fatal("Disabled before any switch = true")
	}

	if err := store.Disable(ctx, DropSwitch{DropID: "slack.post", Tenant: "acme", DisabledBy: "op", Reason: "abuse"}); err != nil {
		t.Fatalf("Disable tenant: %v", err)
	}
	if !store.Disabled("slack.post", "acme") {
		t.Fatal("Disabled(acme) = false after per-tenant switch")
	}
	if store.Disabled("slack.post", "other") {
		t.Fatal("Disabled(other) = true, per-tenant switch leaked")
	}

	if err := store.Disable(ctx, DropSwitch{DropID: "http.request"}); err != nil {
		t.Fatalf("Disable global: %v", err)
	}
	if !store.Disabled("http.request", "anyone") || !store.Disabled("http.request", "") {
		t.Fatal("global switch not applied to all tenants")
	}

	list, err := store.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d / %v, want 2", len(list), err)
	}

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

	got, err := log.List(ctx, core.AuditQuery{Tenant: "t1"})
	if err != nil || len(got) != 2 {
		t.Fatalf("List t1 = %d / %v, want 2", len(got), err)
	}
	if got[0].Actor != "bob" {
		t.Fatalf("List order: first = %q, want bob (newest)", got[0].Actor)
	}

	page, err := log.List(ctx, core.AuditQuery{Tenant: "t1", Limit: 1, Offset: -5})
	if err != nil || len(page) != 1 {
		t.Fatalf("limited list = %d / %v, want 1", len(page), err)
	}

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

	if pruned, err := log.Prune(ctx, 0, 0); err != nil || pruned != 0 {
		t.Fatalf("Prune(0) = %d / %v, want 0", pruned, err)
	}
	if _, err := log.Prune(ctx, time.Nanosecond, 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	remaining, _ := log.List(ctx, core.AuditQuery{Tenant: "t1"})
	if len(remaining) != 0 {
		t.Fatalf("after prune t1 has %d rows, want 0", len(remaining))
	}

	if d, err := log.DeleteByTenant(ctx, "t2"); err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	} else if d < 0 {
		t.Fatalf("DeleteByTenant = %d", d)
	}
}

// Pins the one action retention must not
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

func TestPgScheduleStore_Projection(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS flow_schedules CASCADE"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	store, err := NewPgScheduleStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgScheduleStore: %v", err)
	}

	spec := func(tenant, graphID, entrySuffix, specKey string) ScheduleSpec {
		return ScheduleSpec{
			Tenant: tenant, Workspace: "ws", GraphID: graphID,
			EntryKey: tenant + "/ws/" + graphID + entrySuffix,
			SpecKey:  specKey, Cron: "*/5 * * * *", TZ: "UTC",
		}
	}

	if err := store.ReplaceFlowSchedules(ctx, "t1", "ws", "f1", []ScheduleSpec{
		spec("t1", "f1", "#a", "cron:a"), spec("t1", "f1", "#b", "cron:b"),
	}); err != nil {
		t.Fatalf("replace f1: %v", err)
	}
	if err := store.ReplaceFlowSchedules(ctx, "t2", "ws", "f2", []ScheduleSpec{
		spec("t2", "f2", "#a", "cron:a"),
	}); err != nil {
		t.Fatalf("replace f2: %v", err)
	}
	all, err := store.ListSchedules(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListSchedules = %d / %v, want 3", len(all), err)
	}

	poll := ScheduleSpec{Tenant: "t1", Workspace: "ws", GraphID: "f3",
		EntryKey: "t1/ws/f3@n1", SpecKey: "poll:300", IntervalSeconds: 300}
	if err := store.ReplaceFlowSchedules(ctx, "t1", "ws", "f3", []ScheduleSpec{poll}); err != nil {
		t.Fatalf("replace f3: %v", err)
	}
	all, _ = store.ListSchedules(ctx)
	var gotPoll bool
	for _, s := range all {
		if s.EntryKey == "t1/ws/f3@n1" {
			gotPoll = true
			if s.IntervalSeconds != 300 || !s.IsPoll() || s.Cron != "" {
				t.Fatalf("poll spec round-trip = %+v", s)
			}
		}
	}
	if !gotPoll {
		t.Fatal("poll spec missing after replace")
	}

	if err := store.ReplaceFlowSchedules(ctx, "t1", "ws", "f1", []ScheduleSpec{
		spec("t1", "f1", "#b", "cron:b-edited"),
	}); err != nil {
		t.Fatalf("re-replace f1: %v", err)
	}
	all, _ = store.ListSchedules(ctx)
	if len(all) != 3 { // f1#b, f3@n1, t2/f2#a
		t.Fatalf("after replace = %d entries, want 3", len(all))
	}
	for _, s := range all {
		if s.EntryKey == "t1/ws/f1#a" {
			t.Fatal("replace left the dropped entry behind")
		}
		if s.EntryKey == "t1/ws/f1#b" && s.SpecKey != "cron:b-edited" {
			t.Fatalf("replace did not update the spec key: %+v", s)
		}
	}

	if err := store.ReplaceFlowSchedules(ctx, "t1", "ws", "f1", nil); err != nil {
		t.Fatalf("clear f1: %v", err)
	}
	all, _ = store.ListSchedules(ctx)
	if len(all) != 2 {
		t.Fatalf("after clear = %d entries, want 2", len(all))
	}

	err = store.ReplaceFlowSchedules(ctx, "t1", "ws", "f3", []ScheduleSpec{spec("t1", "other", "#x", "cron:x")})
	if err == nil {
		t.Fatal("ReplaceFlowSchedules accepted a spec for another flow")
	}
	all, _ = store.ListSchedules(ctx)
	if len(all) != 2 {
		t.Fatalf("failed replace changed the table: %d entries, want 2", len(all))
	}

	n, err := store.PruneMissingFlows(ctx, map[string]struct{}{"t2/ws/f2": {}}, nil)
	if err != nil || n != 1 {
		t.Fatalf("PruneMissingFlows = %d / %v, want 1", n, err)
	}
	all, _ = store.ListSchedules(ctx)
	if len(all) != 1 || all[0].Tenant != "t2" {
		t.Fatalf("after prune = %+v, want only t2's row", all)
	}

	d, err := store.DeleteByTenant(ctx, "t2")
	if err != nil || d != 1 {
		t.Fatalf("DeleteByTenant = %d / %v, want 1", d, err)
	}
	if all, _ = store.ListSchedules(ctx); len(all) != 0 {
		t.Fatalf("after erasure = %d entries, want 0", len(all))
	}
}

// The duplicate-EntryKey bug lived in the gap between these two: the derivation
// was tested with its own output, and the durable store was tested with
// hand-written specs. Nothing pushed REAL derived specs into real Postgres,
// where entry_key is a primary key — so a flow carrying the same trigger twice
// rolled back its whole insert and silently lost every schedule it had.
func TestPgScheduleStore_AcceptsEveryDerivedSpecSet(t *testing.T) {
	pool, ctx := covPGPool(t)
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS flow_schedules CASCADE"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	store, err := NewPgScheduleStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgScheduleStore: %v", err)
	}
	ws, err := workspace.OpenFS("")
	if err != nil {
		t.Fatal(err)
	}
	richWorkspace(t, ws)

	ids, err := ws.ListGraphs()
	if err != nil {
		t.Fatal(err)
	}
	stored := 0
	for _, id := range ids {
		g, err := ws.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		specs := DeriveScheduleSpecs(scheduleCronParser, "t", "ws", g, nil)
		if err := store.ReplaceFlowSchedules(ctx, "t", "ws", id, specs); err != nil {
			t.Fatalf("flow %q derived %d specs the store refused: %v", id, len(specs), err)
		}
		stored += len(specs)
	}

	back, err := store.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != stored {
		t.Fatalf("stored %d specs, read back %d", stored, len(back))
	}
	byKey := make(map[string]ScheduleSpec, len(back))
	for _, s := range back {
		byKey[s.EntryKey] = s
	}
	for _, id := range ids {
		g, _ := ws.Load(id)
		for _, want := range DeriveScheduleSpecs(scheduleCronParser, "t", "ws", g, nil) {
			got, ok := byKey[want.EntryKey]
			if !ok {
				t.Errorf("%s: spec %q missing after round-trip", id, want.EntryKey)
				continue
			}
			if got != want {
				t.Errorf("%s: spec %q changed in the round-trip:\n got  %+v\n want %+v", id, want.EntryKey, got, want)
			}
		}
	}
	if _, err := store.DeleteByTenant(ctx, "t"); err != nil {
		t.Fatal(err)
	}
}
