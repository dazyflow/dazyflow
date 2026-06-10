package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Billing (T3 / Phase 3): tenant plans + the free-tier run gate.
//
// Two plans for now — "free" and "pro". A tenant with no stored plan is
// free; Stripe webhook events (see stripe_events.go) flip the plan when
// a subscription starts or dies. Enforcement is a separate, operator-
// opt-in knob (Service.FreeRunsPerMonth) so self-hosted deployments
// without billing never hit a gate.

const (
	PlanFree = "free"
	PlanPro  = "pro"
)

// TenantPlan is a tenant's billing state. The Stripe fields are empty
// for tenants that never went through Checkout.
type TenantPlan struct {
	Tenant string `json:"tenant"`
	Plan   string `json:"plan"` // PlanFree | PlanPro

	// StripeCustomerID / StripeSubscriptionID let the webhook map
	// subscription lifecycle events back to the tenant, and the
	// billing-portal endpoint mint a session for the right customer.
	StripeCustomerID     string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string `json:"stripe_subscription_id,omitempty"`

	// SubscriptionStatus mirrors Stripe's status string (active,
	// past_due, canceled, …) for the UI; the plan field is the
	// enforcement truth.
	SubscriptionStatus string `json:"subscription_status,omitempty"`

	// CurrentPeriodEnd is when the paid period lapses (informational).
	CurrentPeriodEnd time.Time `json:"current_period_end,omitzero"`
}

// PlanStore persists tenant plans. Implementations must be safe for
// concurrent use. Get returns a zero-value free plan (not an error)
// for tenants with no stored row, so callers never special-case "new
// tenant".
type PlanStore interface {
	GetPlan(ctx context.Context, tenant string) (TenantPlan, error)
	SetPlan(ctx context.Context, p TenantPlan) error
}

// MemPlanStore is the in-process PlanStore for dev/tests.
type MemPlanStore struct {
	mu    sync.Mutex
	plans map[string]TenantPlan
}

func NewMemPlanStore() *MemPlanStore {
	return &MemPlanStore{plans: map[string]TenantPlan{}}
}

func (m *MemPlanStore) GetPlan(_ context.Context, tenant string) (TenantPlan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.plans[tenant]; ok {
		return p, nil
	}
	return TenantPlan{Tenant: tenant, Plan: PlanFree}, nil
}

func (m *MemPlanStore) SetPlan(_ context.Context, p TenantPlan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.Plan == "" {
		p.Plan = PlanFree
	}
	m.plans[p.Tenant] = p
	return nil
}

// checkRunQuota is the free-tier run gate, called by SubmitGraphWithSeed
// before any run state is written. Pro tenants and deployments without
// enforcement configured pass through. Fails OPEN on plan/usage store
// errors: a billing-infrastructure hiccup must degrade to "runs work,
// maybe one too many" rather than "product down" — the counters
// themselves stay correct either way.
func (s *Service) checkRunQuota(ctx context.Context, tenant string) error {
	if s.FreeRunsPerMonth <= 0 || s.Plans == nil || s.Usage == nil {
		return nil
	}
	plan, err := s.Plans.GetPlan(ctx, tenant)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("plan gate [%s]: read plan (failing open): %v", tenant, err)
		}
		return nil
	}
	if plan.Plan == PlanPro {
		return nil
	}
	buckets, err := s.Usage.Usage(ctx, tenant, 1)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("plan gate [%s]: read usage (failing open): %v", tenant, err)
		}
		return nil
	}
	var used int64
	if len(buckets) > 0 && buckets[0].Period == usagePeriod(time.Now()) {
		used = buckets[0].GraphRuns
	}
	if used >= int64(s.FreeRunsPerMonth) {
		return fmt.Errorf("%w: %d of %d free runs used this month — upgrade to keep your flows running",
			core.ErrPlanLimit, used, s.FreeRunsPerMonth)
	}
	return nil
}
