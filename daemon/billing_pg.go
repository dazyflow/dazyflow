package daemon

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgPlanStore is the durable PlanStore — one row per tenant, upserted
// whole on every change (plan flips are rare; no need for per-field
// updates).
type PgPlanStore struct {
	pool *pgxpool.Pool
}

const pgPlanSchema = `
CREATE TABLE IF NOT EXISTS tenant_plans (
    tenant                 TEXT PRIMARY KEY,
    plan                   TEXT NOT NULL DEFAULT 'free',
    stripe_customer_id     TEXT NOT NULL DEFAULT '',
    stripe_subscription_id TEXT NOT NULL DEFAULT '',
    subscription_status    TEXT NOT NULL DEFAULT '',
    current_period_end     TIMESTAMPTZ,
    cancel_at_period_end   BOOLEAN NOT NULL DEFAULT false,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Backfill the column on tables created before it existed.
ALTER TABLE tenant_plans
    ADD COLUMN IF NOT EXISTS cancel_at_period_end BOOLEAN NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS stripe_webhook_events (
    id          TEXT PRIMARY KEY,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

func NewPgPlanStore(ctx context.Context, pool *pgxpool.Pool) (*PgPlanStore, error) {
	if err := applyPgSchema(ctx, pool, pgPlanSchema); err != nil {
		return nil, err
	}
	return &PgPlanStore{pool: pool}, nil
}

func (s *PgPlanStore) GetPlan(ctx context.Context, tenant string) (TenantPlan, error) {
	var p TenantPlan
	var periodEnd *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT tenant, plan, stripe_customer_id, stripe_subscription_id,
		       subscription_status, current_period_end, cancel_at_period_end
		FROM tenant_plans WHERE tenant = $1`,
		tenant).Scan(&p.Tenant, &p.Plan, &p.StripeCustomerID,
		&p.StripeSubscriptionID, &p.SubscriptionStatus, &periodEnd,
		&p.CancelAtPeriodEnd)
	if err != nil {
		// No row = free plan, same contract as the memory store.
		if isPgNoRows(err) {
			return TenantPlan{Tenant: tenant, Plan: PlanFree}, nil
		}
		return TenantPlan{}, err
	}
	if periodEnd != nil {
		p.CurrentPeriodEnd = *periodEnd
	}
	return p, nil
}

// MarkStripeEvent records a webhook event id; the INSERT's conflict
// outcome is the atomic first-time test, so concurrent replicas
// processing the same retry agree on exactly one "first".
func (s *PgPlanStore) MarkStripeEvent(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO stripe_webhook_events (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PgPlanStore) SetPlan(ctx context.Context, p TenantPlan) error {
	if p.Plan == "" {
		p.Plan = PlanFree
	}
	var periodEnd *time.Time
	if !p.CurrentPeriodEnd.IsZero() {
		periodEnd = &p.CurrentPeriodEnd
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_plans (tenant, plan, stripe_customer_id,
			stripe_subscription_id, subscription_status, current_period_end,
			cancel_at_period_end)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant) DO UPDATE SET
			plan                   = EXCLUDED.plan,
			stripe_customer_id     = EXCLUDED.stripe_customer_id,
			stripe_subscription_id = EXCLUDED.stripe_subscription_id,
			subscription_status    = EXCLUDED.subscription_status,
			current_period_end     = EXCLUDED.current_period_end,
			cancel_at_period_end   = EXCLUDED.cancel_at_period_end,
			updated_at             = now()`,
		p.Tenant, p.Plan, p.StripeCustomerID, p.StripeSubscriptionID,
		p.SubscriptionStatus, periodEnd, p.CancelAtPeriodEnd)
	return err
}
