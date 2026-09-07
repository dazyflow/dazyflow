// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

// planStoreContract runs the behavior shared by both backends.
func planStoreContract(t *testing.T, store PlanStore) {
	ctx := context.Background()

	// Unknown tenant: zero-value free plan, never an error.
	p, err := store.GetPlan(ctx, "acme")
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if p.Plan != PlanFree || p.Tenant != "acme" {
		t.Errorf("default plan = %+v, want free/acme", p)
	}

	// Upgrade with full Stripe state round-trips.
	end := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	want := TenantPlan{
		Tenant:               "acme",
		Plan:                 PlanPro,
		StripeCustomerID:     "cus_123",
		StripeSubscriptionID: "sub_456",
		SubscriptionStatus:   "active",
		CurrentPeriodEnd:     end,
	}
	if err := store.SetPlan(ctx, want); err != nil {
		t.Fatalf("SetPlan: %v", err)
	}
	got, err := store.GetPlan(ctx, "acme")
	if err != nil {
		t.Fatalf("GetPlan after set: %v", err)
	}
	if got.Plan != PlanPro || got.StripeCustomerID != "cus_123" ||
		got.StripeSubscriptionID != "sub_456" || got.SubscriptionStatus != "active" ||
		!got.CurrentPeriodEnd.Equal(end) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// Downgrade overwrites the row; empty plan normalizes to free.
	if err := store.SetPlan(ctx, TenantPlan{Tenant: "acme", SubscriptionStatus: "canceled"}); err != nil {
		t.Fatalf("SetPlan downgrade: %v", err)
	}
	got, _ = store.GetPlan(ctx, "acme")
	if got.Plan != PlanFree || got.SubscriptionStatus != "canceled" || !got.CurrentPeriodEnd.IsZero() {
		t.Errorf("after downgrade = %+v, want free/canceled/zero period end", got)
	}

	// Other tenants unaffected.
	other, _ := store.GetPlan(ctx, "globex")
	if other.Plan != PlanFree {
		t.Errorf("other tenant = %+v, want free", other)
	}
}

func TestMemPlanStore(t *testing.T) {
	planStoreContract(t, NewMemPlanStore())
}

// TestStripeEventDedupe_ProcessedReadVsMark covers the read/mark split that
// lets the webhook handler mark an event only AFTER a successful apply:
// StripeEventProcessed must report false until MarkStripeEvent records it.
func TestStripeEventDedupe_ProcessedReadVsMark(t *testing.T) {
	store := NewMemPlanStore()
	ctx := context.Background()
	// Unseen event reads as not-processed (so the handler applies it).
	if seen, _ := store.StripeEventProcessed(ctx, "evt_1"); seen {
		t.Fatal("unseen event reported processed")
	}
	// Mark only after a (hypothetical) successful apply.
	if first, _ := store.MarkStripeEvent(ctx, "evt_1"); !first {
		t.Fatal("first mark should report first=true")
	}
	// Now a replay sees it as processed and is skipped without re-applying.
	if seen, _ := store.StripeEventProcessed(ctx, "evt_1"); !seen {
		t.Fatal("marked event should read as processed")
	}
}

// Gated on DAZYFLOW_TEST_DB (a real Postgres), like the jobstore/auth
// integration tests.
func TestPgPlanStore(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres plan tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPgPlanStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgPlanStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_plans, stripe_webhook_events"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	planStoreContract(t, store)

	// Event dedupe rides the same store: first insert wins, replay is seen.
	if first, err := store.MarkStripeEvent(ctx, "evt_pg"); err != nil || !first {
		t.Errorf("pg first = %v/%v", first, err)
	}
	if again, err := store.MarkStripeEvent(ctx, "evt_pg"); err != nil || again {
		t.Errorf("pg replay = %v/%v", again, err)
	}
}

// The trigger gate: free tenants are refused when FreePollingDisabled,
// pro tenants and ungated deployments pass.
func TestCheckTriggerQuota(t *testing.T) {
	plans := NewMemPlanStore()
	svc := &Service{Plans: plans}

	// Default: gate off → free tenant passes.
	if err := svc.checkTriggerQuota(context.Background(), "t"); err != nil {
		t.Errorf("ungated: %v", err)
	}
	svc.FreePollingDisabled = true
	if err := svc.checkTriggerQuota(context.Background(), "t"); !errors.Is(err, core.ErrPlanLimit) {
		t.Errorf("gated free tenant err = %v, want ErrPlanLimit", err)
	}
	_ = plans.SetPlan(context.Background(), TenantPlan{Tenant: "t", Plan: PlanPro})
	if err := svc.checkTriggerQuota(context.Background(), "t"); err != nil {
		t.Errorf("gated pro tenant: %v", err)
	}
	// No plan store at all: fail open.
	bare := &Service{FreePollingDisabled: true}
	if err := bare.checkTriggerQuota(context.Background(), "t"); err != nil {
		t.Errorf("no-plan-store should fail open: %v", err)
	}
}

// TestBillingService_Standalone exercises the extracted BillingService
// directly (no Service), proving the gate logic is self-contained — the point
// of carving it off the god object. It also confirms Service.billing() wires
// the same fields, so the delegation preserves behaviour.
func TestBillingService_Standalone(t *testing.T) {
	plans := NewMemPlanStore()
	b := &BillingService{plans: plans, freePollingDisabled: true}

	if err := b.checkTriggerQuota(context.Background(), "t"); !errors.Is(err, core.ErrPlanLimit) {
		t.Errorf("free tenant should be gated: %v", err)
	}
	_ = plans.SetPlan(context.Background(), TenantPlan{Tenant: "t", Plan: PlanPro})
	if err := b.checkTriggerQuota(context.Background(), "t"); err != nil {
		t.Errorf("pro tenant should pass: %v", err)
	}

	// Service.billing() carries the same config through to the gate.
	svc := &Service{Plans: NewMemPlanStore(), FreePollingDisabled: true}
	if err := svc.billing().checkTriggerQuota(context.Background(), "t"); !errors.Is(err, core.ErrPlanLimit) {
		t.Errorf("Service.billing() should gate a free tenant: %v", err)
	}
}

// Stripe event-id dedupe: first marking wins, replays report seen.
func TestMemPlanStore_MarkStripeEvent(t *testing.T) {
	store := NewMemPlanStore()
	first, err := store.MarkStripeEvent(context.Background(), "evt_1")
	if err != nil || !first {
		t.Fatalf("first = %v/%v, want true", first, err)
	}
	again, err := store.MarkStripeEvent(context.Background(), "evt_1")
	if err != nil || again {
		t.Errorf("replay = %v/%v, want false", again, err)
	}
	other, _ := store.MarkStripeEvent(context.Background(), "evt_2")
	if !other {
		t.Errorf("different event should be first")
	}
}

// The reservation has to precede the write (the cap check and the increment
// must be atomic), which leaves a window: the write fails and the tenant has
// been charged for a run that exists nowhere — no record, no history, nothing
// to retry. It must be given back.
func TestReleaseRun_GivesBackAReservationWhoseWriteFailed(t *testing.T) {
	usage := NewMemUsageStore()
	svc := &Service{Usage: usage}
	ctx := t.Context()

	if _, err := usage.AddRunIfUnder(ctx, "acme", time.Now(), 10); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if used := runsUsed(t, usage, "acme"); used != 1 {
		t.Fatalf("reserved %d runs, want 1", used)
	}

	svc.releaseRun(ctx, "acme")
	if used := runsUsed(t, usage, "acme"); used != 0 {
		t.Errorf("after release the tenant is still charged %d run(s)", used)
	}
}

// A release with nothing reserved must not drive the counter negative and
// make the Usage page nonsense.
func TestReleaseRun_FloorsAtZero(t *testing.T) {
	usage := NewMemUsageStore()
	svc := &Service{Usage: usage}
	svc.releaseRun(t.Context(), "acme")
	if used := runsUsed(t, usage, "acme"); used != 0 {
		t.Errorf("counter went to %d, want 0", used)
	}
}

func runsUsed(t *testing.T, usage UsageStore, tenant string) int64 {
	t.Helper()
	buckets, err := usage.Usage(t.Context(), tenant, 1)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if len(buckets) == 0 {
		return 0
	}
	return buckets[0].GraphRuns
}
