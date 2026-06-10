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

// StripeEventDeduper is an optional PlanStore extension: record a Stripe
// webhook event id, reporting whether this is the FIRST time it's seen.
// Stripe retries deliveries, and while the plan upserts are idempotent,
// dedupe keeps replays from re-running side effects (audit noise today,
// anything heavier tomorrow).
type StripeEventDeduper interface {
	MarkStripeEvent(ctx context.Context, id string) (first bool, err error)
}

// MemPlanStore is the in-process PlanStore for dev/tests.
type MemPlanStore struct {
	mu    sync.Mutex
	plans map[string]TenantPlan
	seen  map[string]bool
}

func NewMemPlanStore() *MemPlanStore {
	return &MemPlanStore{plans: map[string]TenantPlan{}, seen: map[string]bool{}}
}

func (m *MemPlanStore) MarkStripeEvent(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen[id] {
		return false, nil
	}
	m.seen[id] = true
	return true, nil
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

// tenantIsFree resolves whether the plan gates apply to tenant. Fails
// OPEN (reports pro) on plan-store errors: a billing-infrastructure
// hiccup must degrade to "no gate" rather than "product down".
func (s *Service) tenantIsFree(ctx context.Context, tenant, gate string) bool {
	plan, err := s.Plans.GetPlan(ctx, tenant)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("%s [%s]: read plan (failing open): %v", gate, tenant, err)
		}
		return false
	}
	return plan.Plan != PlanPro
}

// runsThisMonth reads the tenant's current-month run count from the
// metering buckets. One home for the "current bucket" semantics, shared
// by the run gate and the billing view — when billing-day anchoring
// lands, both change together.
func (s *Service) runsThisMonth(ctx context.Context, tenant string) (int64, error) {
	buckets, err := s.Usage.Usage(ctx, tenant, 1)
	if err != nil {
		return 0, err
	}
	if len(buckets) > 0 && buckets[0].Period == usagePeriod(time.Now()) {
		return buckets[0].GraphRuns, nil
	}
	return 0, nil
}

// checkTriggerQuota is the free-tier scheduling gate, called by the
// scheduler before firing a cron/poll trigger. Same fail-open policy as
// checkRunQuota: a billing-store hiccup must not silence everyone's
// schedules.
func (s *Service) checkTriggerQuota(ctx context.Context, tenant string) error {
	if !s.FreePollingDisabled || s.Plans == nil {
		return nil
	}
	if !s.tenantIsFree(ctx, tenant, "trigger gate") {
		return nil
	}
	return fmt.Errorf("%w: schedules and polling triggers are a Pro feature — manual runs still work", core.ErrPlanLimit)
}

// checkRunQuota is the free-tier run gate, called by SubmitGraphWithSeed
// before any run state is written. Pro tenants and deployments without
// enforcement configured pass through; usage-store errors fail open too.
func (s *Service) checkRunQuota(ctx context.Context, tenant string) error {
	if s.FreeRunsPerMonth <= 0 || s.Plans == nil || s.Usage == nil {
		return nil
	}
	if !s.tenantIsFree(ctx, tenant, "plan gate") {
		return nil
	}
	used, err := s.runsThisMonth(ctx, tenant)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("plan gate [%s]: read usage (failing open): %v", tenant, err)
		}
		return nil
	}
	if used >= int64(s.FreeRunsPerMonth) {
		return fmt.Errorf("%w: %d of %d free runs used this month — upgrade to keep your flows running",
			core.ErrPlanLimit, used, s.FreeRunsPerMonth)
	}
	return nil
}

// CachedPlanStore fronts a PlanStore with a short-TTL read cache. Plans
// sit on the hottest paths — every gated submission and every scheduled
// fire reads one — but change only via the Stripe webhook, whose
// SetPlan writes through this cache so the SAME replica sees the flip
// immediately. Other replicas converge within ttl (default 30s): an
// acceptable upgrade lag, in keeping with the gates' fail-open posture.
type CachedPlanStore struct {
	inner PlanStore
	ttl   time.Duration

	mu      sync.Mutex
	entries map[string]planCacheEntry
}

type planCacheEntry struct {
	plan TenantPlan
	exp  time.Time
}

func NewCachedPlanStore(inner PlanStore, ttl time.Duration) *CachedPlanStore {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &CachedPlanStore{inner: inner, ttl: ttl, entries: map[string]planCacheEntry{}}
}

func (c *CachedPlanStore) GetPlan(ctx context.Context, tenant string) (TenantPlan, error) {
	c.mu.Lock()
	if e, ok := c.entries[tenant]; ok && e.exp.After(nowFunc()) {
		c.mu.Unlock()
		return e.plan, nil
	}
	c.mu.Unlock()
	plan, err := c.inner.GetPlan(ctx, tenant)
	if err != nil {
		return plan, err
	}
	c.store(tenant, plan)
	return plan, nil
}

func (c *CachedPlanStore) SetPlan(ctx context.Context, p TenantPlan) error {
	if err := c.inner.SetPlan(ctx, p); err != nil {
		return err
	}
	if p.Plan == "" {
		p.Plan = PlanFree // mirror the stores' normalization
	}
	c.store(p.Tenant, p)
	return nil
}

func (c *CachedPlanStore) store(tenant string, plan TenantPlan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[tenant] = planCacheEntry{plan: plan, exp: nowFunc().Add(c.ttl)}
}

// MarkStripeEvent passes the webhook dedupe through to the inner store,
// so wrapping a PgPlanStore doesn't silently lose replay protection.
func (c *CachedPlanStore) MarkStripeEvent(ctx context.Context, id string) (bool, error) {
	if dd, ok := c.inner.(StripeEventDeduper); ok {
		return dd.MarkStripeEvent(ctx, id)
	}
	return true, nil
}
