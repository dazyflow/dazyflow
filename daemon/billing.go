package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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

// BillingService owns the free-tier plan gates — the cohesive billing
// concern extracted off the Service god object. It holds only the deps the
// gates need (plans, usage metering, the free-tier limits, a logger), so the
// logic is independently testable and Service is left a thin facade that
// delegates to it via Service.billing(). OAuth and secrets are likewise their
// own services (OAuthRegistry, EncryptedSecrets); this carves out the last
// in-Service cluster.
type BillingService struct {
	plans               PlanStore
	usage               UsageStore
	freeRunsPerMonth    int
	freePollingDisabled bool
	logger              *log.Logger
	// effective, when set, resolves per-org tier/override limits + the
	// effective plan. The gates prefer it over the raw plan-store + global
	// knobs; left nil (e.g. in unit tests that build a BillingService
	// directly) the gates fall back to the pre-entitlement behaviour.
	effective func(ctx context.Context, tenant string) EffectiveLimits
}

// billing builds the BillingService view over the Service's billing fields.
// Cheap (a struct copy), so callers construct one per use rather than caching.
func (s *Service) billing() *BillingService {
	b := &BillingService{
		plans:               s.Plans,
		usage:               s.Usage,
		freeRunsPerMonth:    s.FreeRunsPerMonth,
		freePollingDisabled: s.FreePollingDisabled,
		logger:              s.Logger,
	}
	if s.Entitlements != nil {
		b.effective = s.effectiveLimits
	}
	return b
}

// Service-level delegations keep every existing caller (and test) working
// while the logic lives on BillingService.
func (s *Service) runsThisMonth(ctx context.Context, tenant string) (int64, error) {
	return s.billing().runsThisMonth(ctx, tenant)
}
func (s *Service) checkTriggerQuota(ctx context.Context, tenant string) error {
	return s.billing().checkTriggerQuota(ctx, tenant)
}
func (s *Service) checkRunQuota(ctx context.Context, tenant string) error {
	return s.billing().checkRunQuota(ctx, tenant)
}

// tenantIsFree resolves whether the plan gates apply to tenant. Fails
// OPEN (reports pro) on plan-store errors: a billing-infrastructure
// hiccup must degrade to "no gate" rather than "product down".
func (b *BillingService) tenantIsFree(ctx context.Context, tenant, gate string) bool {
	// Prefer the effective plan (it layers tier/comp/trial/force over
	// Stripe) when the entitlement resolver is wired.
	if b.effective != nil {
		return b.effective(ctx, tenant).Plan != PlanPro
	}
	plan, err := b.plans.GetPlan(ctx, tenant)
	if err != nil {
		if b.logger != nil {
			b.logger.Printf("%s [%s]: read plan (failing open): %v", gate, tenant, err)
		}
		return false
	}
	return plan.Plan != PlanPro
}

// runLimit is the tenant's effective monthly run cap: the per-org/tier
// value when entitlements are wired, else the global free-tier default.
func (b *BillingService) runLimit(ctx context.Context, tenant string) int {
	if b.effective != nil {
		return b.effective(ctx, tenant).RunsPerMonth
	}
	return b.freeRunsPerMonth
}

// pollingAllowed reports whether tenant may run scheduled/poll triggers.
func (b *BillingService) pollingAllowed(ctx context.Context, tenant string) bool {
	if b.effective != nil {
		return b.effective(ctx, tenant).PollingAllowed
	}
	// Pre-entitlement: allowed unless the global gate is on and the tenant
	// is free.
	if !b.freePollingDisabled || b.plans == nil {
		return true
	}
	return !b.tenantIsFree(ctx, tenant, "trigger gate")
}

// runsThisMonth reads the tenant's current-month run count from the
// metering buckets. One home for the "current bucket" semantics, shared
// by the run gate and the billing view — when billing-day anchoring
// lands, both change together.
func (b *BillingService) runsThisMonth(ctx context.Context, tenant string) (int64, error) {
	buckets, err := b.usage.Usage(ctx, tenant, 1)
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
func (b *BillingService) checkTriggerQuota(ctx context.Context, tenant string) error {
	if b.pollingAllowed(ctx, tenant) {
		return nil
	}
	return fmt.Errorf("%w: schedules and polling triggers are a Pro feature — manual runs still work", core.ErrPlanLimit)
}

// checkRunQuota is the free-tier run gate, called by SubmitGraphWithSeed
// before any run state is written. Pro tenants and deployments without
// enforcement configured pass through; usage-store errors fail open too.
func (b *BillingService) checkRunQuota(ctx context.Context, tenant string) error {
	if b.usage == nil {
		return nil
	}
	limit := b.runLimit(ctx, tenant)
	if limit <= 0 {
		return nil // 0 = no cap
	}
	if !b.tenantIsFree(ctx, tenant, "plan gate") {
		return nil // pro / comped / trial: uncapped
	}
	used, err := b.runsThisMonth(ctx, tenant)
	if err != nil {
		if b.logger != nil {
			b.logger.Printf("plan gate [%s]: read usage (failing open): %v", tenant, err)
		}
		return nil
	}
	if used >= int64(limit) {
		return fmt.Errorf("%w: %d of %d runs used this month — upgrade to keep your flows running",
			core.ErrPlanLimit, used, limit)
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
	cache *ttlCache[TenantPlan]
}

func NewCachedPlanStore(inner PlanStore, ttl time.Duration) *CachedPlanStore {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &CachedPlanStore{inner: inner, cache: newTTLCache[TenantPlan](ttl)}
}

func (c *CachedPlanStore) GetPlan(ctx context.Context, tenant string) (TenantPlan, error) {
	if p, ok := c.cache.get(tenant); ok {
		return p, nil
	}
	plan, err := c.inner.GetPlan(ctx, tenant)
	if err != nil {
		return plan, err
	}
	c.cache.put(tenant, plan)
	return plan, nil
}

func (c *CachedPlanStore) SetPlan(ctx context.Context, p TenantPlan) error {
	if err := c.inner.SetPlan(ctx, p); err != nil {
		return err
	}
	if p.Plan == "" {
		p.Plan = PlanFree // mirror the stores' normalization
	}
	c.cache.put(p.Tenant, p)
	return nil
}

// MarkStripeEvent passes the webhook dedupe through to the inner store,
// so wrapping a PgPlanStore doesn't silently lose replay protection.
func (c *CachedPlanStore) MarkStripeEvent(ctx context.Context, id string) (bool, error) {
	if dd, ok := c.inner.(StripeEventDeduper); ok {
		return dd.MarkStripeEvent(ctx, id)
	}
	return true, nil
}
