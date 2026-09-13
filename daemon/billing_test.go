// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	"github.com/dazyflow/dazyflow/core"
)

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

	if err := store.SetPlan(ctx, TenantPlan{Tenant: "acme", SubscriptionStatus: "canceled"}); err != nil {
		t.Fatalf("SetPlan downgrade: %v", err)
	}
	got, _ = store.GetPlan(ctx, "acme")
	if got.Plan != PlanFree || got.SubscriptionStatus != "canceled" || !got.CurrentPeriodEnd.IsZero() {
		t.Errorf("after downgrade = %+v, want free/canceled/zero period end", got)
	}

	other, _ := store.GetPlan(ctx, "globex")
	if other.Plan != PlanFree {
		t.Errorf("other tenant = %+v, want free", other)
	}
}

func TestMemPlanStore(t *testing.T) {
	planStoreContract(t, NewMemPlanStore())
}

// Covers the read/mark split that lets the webhook handler mark an event only
// AFTER a successful apply: StripeEventProcessed must report false until
// MarkStripeEvent records it.
func TestStripeEventDedupe_ProcessedReadVsMark(t *testing.T) {
	store := NewMemPlanStore()
	ctx := context.Background()
	if seen, _ := store.StripeEventProcessed(ctx, "evt_1"); seen {
		t.Fatal("unseen event reported processed")
	}
	if first, _ := store.MarkStripeEvent(ctx, "evt_1"); !first {
		t.Fatal("first mark should report first=true")
	}
	if seen, _ := store.StripeEventProcessed(ctx, "evt_1"); !seen {
		t.Fatal("marked event should read as processed")
	}
}

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

	if first, err := store.MarkStripeEvent(ctx, "evt_pg"); err != nil || !first {
		t.Errorf("pg first = %v/%v", first, err)
	}
	if again, err := store.MarkStripeEvent(ctx, "evt_pg"); err != nil || again {
		t.Errorf("pg replay = %v/%v", again, err)
	}
}

func TestCheckTriggerQuota(t *testing.T) {
	plans := NewMemPlanStore()
	svc := &Service{Plans: plans}

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
	bare := &Service{FreePollingDisabled: true}
	if err := bare.checkTriggerQuota(context.Background(), "t"); err != nil {
		t.Errorf("no-plan-store should fail open: %v", err)
	}
}

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

	svc := &Service{Plans: NewMemPlanStore(), FreePollingDisabled: true}
	if err := svc.billing().checkTriggerQuota(context.Background(), "t"); !errors.Is(err, core.ErrPlanLimit) {
		t.Errorf("Service.billing() should gate a free tenant: %v", err)
	}
}

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

// The monthly ceiling the per-minute limiter cannot give: that one bounds a
// burst, this one bounds a bill.
func TestCheckGenerationQuota(t *testing.T) {
	t.Parallel()
	newSvc := func(limit int) (*BillingService, *MemUsageStore) {
		usage := NewMemUsageStore()
		return &BillingService{
			usage: usage,
			effective: func(context.Context, string) EffectiveLimits {
				return EffectiveLimits{GenerationsPerMonth: limit}
			},
		}, usage
	}

	t.Run("counts up to the limit and then refuses", func(t *testing.T) {
		b, usage := newSvc(3)
		ctx := context.Background()
		for i := range 3 {
			if err := b.checkGenerationQuota(ctx, "acme"); err != nil {
				t.Fatalf("generation %d refused early: %v", i+1, err)
			}
			b.recordGeneration(ctx, "acme")
		}
		err := b.checkGenerationQuota(ctx, "acme")
		if err == nil {
			t.Fatal("a fourth generation was allowed past a limit of three")
		}
		if !errors.Is(err, core.ErrPlanLimit) {
			t.Errorf("error %v, want it to wrap core.ErrPlanLimit so the UI can offer an upgrade", err)
		}
		if !strings.Contains(err.Error(), "3 of 3") {
			t.Errorf("message %q should say where the org stands", err)
		}
		// Someone else's month is their own.
		if err := b.checkGenerationQuota(ctx, "other"); err != nil {
			t.Errorf("a different org was refused: %v", err)
		}
		if got, _ := usage.Usage(ctx, "acme", 1); len(got) != 1 || got[0].FlowGenerations != 3 {
			t.Errorf("counted %+v, want 3 generations", got)
		}
	})

	t.Run("zero means no cap", func(t *testing.T) {
		b, _ := newSvc(0)
		for range 50 {
			b.recordGeneration(context.Background(), "acme")
		}
		if err := b.checkGenerationQuota(context.Background(), "acme"); err != nil {
			t.Errorf("an uncapped org was refused: %v", err)
		}
	})

	// A usage table that cannot be read is an operator's problem; refusing
	// everyone's work over it turns a degraded database into an outage.
	t.Run("fails open when usage cannot be read", func(t *testing.T) {
		b := &BillingService{
			usage: brokenUsage{},
			effective: func(context.Context, string) EffectiveLimits {
				return EffectiveLimits{GenerationsPerMonth: 1}
			},
		}
		if err := b.checkGenerationQuota(context.Background(), "acme"); err != nil {
			t.Errorf("a storage error closed the door: %v", err)
		}
	})
}

type brokenUsage struct{ UsageStore }

func (brokenUsage) Usage(context.Context, string, int) ([]UsageCounters, error) {
	return nil, errors.New("usage table unavailable")
}
