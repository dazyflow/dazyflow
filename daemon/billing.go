// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// Tenant plans and the free-tier run gate.

const (
	PlanFree = "free"
	PlanPro  = "pro"
)

// A live subscription is what distinguishes a paid tenant from a lapsed one.
func liveSubscription(p TenantPlan) bool {
	if p.StripeSubscriptionID == "" {
		return false
	}
	switch p.SubscriptionStatus {
	case "active", "trialing", "past_due":
		return true
	default:
		return false
	}
}

type TenantPlan struct {
	Tenant string `json:"tenant"`
	Plan   string `json:"plan"` // PlanFree | PlanPro

	StripeCustomerID     string `json:"stripe_customer_id,omitempty"`
	StripeSubscriptionID string `json:"stripe_subscription_id,omitempty"`

	SubscriptionStatus string `json:"subscription_status,omitempty"`

	CurrentPeriodEnd time.Time `json:"current_period_end,omitzero"`

	CancelAtPeriodEnd bool `json:"cancel_at_period_end,omitempty"`
}

type PlanStore interface {
	GetPlan(ctx context.Context, tenant string) (TenantPlan, error)
	SetPlan(ctx context.Context, p TenantPlan) error
}

type StripeEventDeduper interface {
	MarkStripeEvent(ctx context.Context, id string) (first bool, err error)
	StripeEventProcessed(ctx context.Context, id string) (bool, error)
}

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

func (m *MemPlanStore) StripeEventProcessed(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[id], nil
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

type BillingService struct {
	plans               PlanStore
	usage               UsageStore
	freeRunsPerMonth    int
	freePollingDisabled bool
	freeMaxConcurrency  int
	logger              *log.Logger
	effective           func(ctx context.Context, tenant string) EffectiveLimits
}

func (s *Service) billing() *BillingService {
	b := &BillingService{
		plans:               s.Plans,
		usage:               s.Usage,
		freeRunsPerMonth:    s.FreeRunsPerMonth,
		freePollingDisabled: s.FreePollingDisabled,
		freeMaxConcurrency:  s.FreeMaxConcurrency,
		logger:              s.Logger,
	}
	if s.Entitlements != nil {
		b.effective = s.effectiveLimits
	}
	return b
}

func (s *Service) runsThisMonth(ctx context.Context, tenant string) (int64, error) {
	return s.billing().runsThisMonth(ctx, tenant)
}
func (s *Service) checkTriggerQuota(ctx context.Context, tenant string) error {
	return s.billing().checkTriggerQuota(ctx, tenant)
}
func (s *Service) checkRunQuota(ctx context.Context, tenant string) error {
	return s.billing().checkRunQuota(ctx, tenant)
}
func (s *Service) reserveRun(ctx context.Context, tenant string) (bool, error) {
	return s.billing().reserveRun(ctx, tenant)
}

func (s *Service) releaseRun(ctx context.Context, tenant string) {
	if s.Usage == nil {
		return
	}
	rl, ok := s.Usage.(runReleaser)
	if !ok {
		return
	}
	if err := rl.ReleaseRun(ctx, tenant, time.Now()); err != nil && s.Logger != nil {
		s.Logger.Printf("usage metering [%s]: release reserved run: %v", tenant, err)
	}
}

func (s *Service) concurrencyCapped(ctx context.Context, tenant string) (limit int, capped bool) {
	b := s.billing()
	limit = b.concurrencyLimit(ctx, tenant)
	if limit <= 0 {
		return 0, false
	}
	return limit, true
}

func (s *Service) runningGraphRuns(ctx context.Context, tenant string, limit int) (int, error) {
	page := limit + 1
	if page <= 1 {
		page = 200
	}
	return core.CountRuns(ctx, s.Jobs, core.ListGraphRunsOpts{
		Tenant: tenant, Status: core.JobStatusRunning, Limit: page,
	})
}

func (s *Service) admitGraphRun(ctx context.Context, tenant string) bool {
	if s.Jobs == nil {
		return true
	}
	limit, capped := s.concurrencyCapped(ctx, tenant)
	if !capped {
		return true
	}
	running, err := s.runningGraphRuns(ctx, tenant, limit)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Printf("concurrency admission [%s]: count running (admitting): %v", tenant, err)
		}
		return true
	}
	return running < limit
}

func (b *BillingService) tenantIsFree(ctx context.Context, tenant, gate string) bool {
	if b.effective != nil {
		return b.effective(ctx, tenant).Plan != PlanPro
	}
	if b.plans == nil {
		return false // no plan store → no pro signal; fail open (no gate)
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

func (b *BillingService) runLimit(ctx context.Context, tenant string) int {
	if b.effective != nil {
		return b.effective(ctx, tenant).RunsPerMonth
	}
	if !b.tenantIsFree(ctx, tenant, "plan gate") {
		return 0
	}
	return b.freeRunsPerMonth
}

func (b *BillingService) pollingAllowed(ctx context.Context, tenant string) bool {
	if b.effective != nil {
		return b.effective(ctx, tenant).PollingAllowed
	}
	if !b.freePollingDisabled || b.plans == nil {
		return true
	}
	return !b.tenantIsFree(ctx, tenant, "trigger gate")
}

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

func (b *BillingService) checkTriggerQuota(ctx context.Context, tenant string) error {
	if b.pollingAllowed(ctx, tenant) {
		return nil
	}
	return fmt.Errorf("%w: schedules and polling triggers are a Pro feature — manual runs still work", core.ErrPlanLimit)
}

func (b *BillingService) checkRunQuota(ctx context.Context, tenant string) error {
	if b.usage == nil {
		return nil
	}
	limit := b.runLimit(ctx, tenant)
	if limit <= 0 {
		return nil // 0 = no cap
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

func (b *BillingService) reserveRun(ctx context.Context, tenant string) (admitted bool, err error) {
	if b.usage == nil {
		return true, nil
	}
	limit := b.runLimit(ctx, tenant)
	if limit <= 0 {
		return true, b.usage.AddRun(ctx, tenant, time.Now())
	}
	if rr, ok := b.usage.(runReserver); ok {
		admitted, rerr := rr.AddRunIfUnder(ctx, tenant, time.Now(), limit)
		if rerr != nil {
			return true, rerr
		}
		return admitted, nil
	}
	used, err := b.runsThisMonth(ctx, tenant)
	if err != nil {
		return true, err
	}
	if used >= int64(limit) {
		return false, nil
	}
	return true, b.usage.AddRun(ctx, tenant, time.Now())
}

func (b *BillingService) concurrencyLimit(ctx context.Context, tenant string) int {
	if b.effective != nil {
		return b.effective(ctx, tenant).MaxConcurrency
	}
	if !b.tenantIsFree(ctx, tenant, "concurrency gate") {
		return 0
	}
	return b.freeMaxConcurrency
}

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

func (c *CachedPlanStore) MarkStripeEvent(ctx context.Context, id string) (bool, error) {
	if dd, ok := c.inner.(StripeEventDeduper); ok {
		return dd.MarkStripeEvent(ctx, id)
	}
	return true, nil
}

func (c *CachedPlanStore) StripeEventProcessed(ctx context.Context, id string) (bool, error) {
	if dd, ok := c.inner.(StripeEventDeduper); ok {
		return dd.StripeEventProcessed(ctx, id)
	}
	return false, nil
}
