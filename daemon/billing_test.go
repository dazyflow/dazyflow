package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// Gated on HAZYFLOW_TEST_DB (a real Postgres), like the jobstore/auth
// integration tests.
func TestPgPlanStore(t *testing.T) {
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres plan tests")
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
	if _, err := pool.Exec(ctx, "TRUNCATE tenant_plans"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	planStoreContract(t, store)
}
