// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
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

// liveSubscription reports whether the tenant already has a Stripe
// subscription that is billing (or about to): minting a second Checkout
// session in this state would create a duplicate subscription on a new
// customer and double-bill. "active"/"trialing"/"past_due" all count —
// past_due is still mid-retry, and a cancel-at-period-end subscription is
// still "active" (resume via the portal, not a fresh checkout). A lapsed
// one (canceled/unpaid/incomplete_expired/empty) is fair game to re-checkout.
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

	// CancelAtPeriodEnd is true once the subscription is set to cancel at
	// the period boundary: Stripe keeps status "active" (access continues)
	// but won't renew, so the UI shows "cancels on <CurrentPeriodEnd>"
	// rather than implying an open-ended plan.
	CancelAtPeriodEnd bool `json:"cancel_at_period_end,omitempty"`
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
	// StripeEventProcessed reports whether this event id was already recorded.
	// The webhook handler marks an event ONLY after a successful apply, so a
	// recorded id means the side effect completed — letting a replay skip
	// re-applying without the mark-before-apply hazard (a failed apply that was
	// already marked would never be retried).
	StripeEventProcessed(ctx context.Context, id string) (bool, error)
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
	freeMaxConcurrency  int
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
		freeMaxConcurrency:  s.FreeMaxConcurrency,
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
func (s *Service) reserveRun(ctx context.Context, tenant string) (bool, error) {
	return s.billing().reserveRun(ctx, tenant)
}

// recordSkippedFire writes a terminal "skipped" graph run so a cap-blocked
// scheduled fire is visible in the Runs list, not just the server log. It
// enqueues a bare graph record (no node work, so nothing dispatches) and
// immediately completes it skipped with the reason. Best-effort: a write
// failure is logged, never propagated — the fire already wasn't going to run.
// The scheduler coalesces calls (one per flow per window); the precise count
// lives in the usage counter.
func (s *Service) recordSkippedFire(ctx context.Context, tenant, workspace, graphID string) {
	if s.Jobs == nil {
		return
	}
	id, err := newID()
	if err != nil {
		return
	}
	// A minimal (node-less) graph payload so the run-detail view renders
	// safely rather than choking on an empty payload.
	payload, _ := json.Marshal(core.Graph{ID: graphID, Tenant: tenant, Workspace: workspace})
	rec := core.JobRecord{
		ID:           id,
		Kind:         core.JobKindGraph,
		GraphID:      graphID,
		NodeID:       "*",
		Tenant:       tenant,
		Workspace:    workspace,
		Status:       core.JobStatusRunning,
		GraphPayload: payload,
		Job:          core.Job{ID: id, GraphID: graphID},
	}
	if err := s.Jobs.Enqueue(ctx, rec); err != nil {
		if s.Logger != nil {
			s.Logger.Printf("skipped-run marker [%s/%s/%s]: enqueue: %v", tenant, workspace, graphID, err)
		}
		return
	}
	_ = s.Jobs.Complete(ctx, id, core.JobStatusSkipped, &core.Result{
		JobID:  id,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "plan_run_cap",
			Message: "Scheduled run skipped — over the plan's monthly run limit.",
		},
	})
}

// concurrencyCapped reports the tenant's effective simultaneous-run limit when
// it applies (limit > 0), and false otherwise (no limit → unlimited). The
// effective limit already encodes the plan — Pro defaults to 0 (uncapped) but
// honors an explicit fair-use cap from its tier/override. Shared by the
// submit-time admission decision and the promotion sweep so both agree on who
// is capped and at what number.
func (s *Service) concurrencyCapped(ctx context.Context, tenant string) (limit int, capped bool) {
	b := s.billing()
	limit = b.concurrencyLimit(ctx, tenant)
	if limit <= 0 {
		return 0, false
	}
	return limit, true
}

// runningGraphRuns counts a tenant's currently-running top-level graph runs. It
// stops counting once it reaches limit — the admission decision only needs to
// know whether the cap is hit. limit <= 0 means "no early stop" (count up to a
// generous page).
func (s *Service) runningGraphRuns(ctx context.Context, tenant string, limit int) (int, error) {
	page := limit + 1
	if page <= 1 {
		page = 200
	}
	recs, err := s.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
		Tenant: tenant, Status: core.JobStatusRunning, Limit: page,
	})
	if err != nil {
		return 0, err
	}
	return len(recs), nil
}

// admitGraphRun reports whether a new top-level run for tenant may START now
// (true) or must wait as pending/queued (false). Free-tier only; pro/comped/
// trial and a 0 limit always admit. Fails OPEN (admit) on a job-store hiccup —
// a counting error must never strand a run in the pending queue.
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

// tenantIsFree resolves whether the plan gates apply to tenant. Fails
// OPEN (reports pro) on plan-store errors: a billing-infrastructure
// hiccup must degrade to "no gate" rather than "product down".
func (b *BillingService) tenantIsFree(ctx context.Context, tenant, gate string) bool {
	// Prefer the effective plan (it layers tier/comp/trial/force over
	// Stripe) when the entitlement resolver is wired.
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

// runLimit is the tenant's effective monthly run cap: the per-org/tier
// value when entitlements are wired, else the global free-tier default.
func (b *BillingService) runLimit(ctx context.Context, tenant string) int {
	if b.effective != nil {
		return b.effective(ctx, tenant).RunsPerMonth
	}
	// Pre-entitlements: the free default caps free tenants only; pro/comped/
	// trial are uncapped (the free env value isn't a global ceiling).
	if !b.tenantIsFree(ctx, tenant, "plan gate") {
		return 0
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
	// The effective limit already encodes the plan: 0 = uncapped (Pro's
	// default, and any plan with no cap), N > 0 = enforce — so a Pro tier with
	// an explicit fair-use cap is honored, not bypassed.
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

// reserveRun is the AUTHORITATIVE run-cap gate: it atomically counts one run
// iff the tenant is under its monthly cap, closing the check-then-increment
// race that checkRunQuota (a read) leaves open. Returns admitted=false (without
// counting) when at the cap, admitted=true (having counted) otherwise. A
// store/limit error fails OPEN (admitted=true, counted best-effort) — same
// posture as checkRunQuota: a billing hiccup must not halt runs. The caller
// must NOT separately AddRun — reserveRun already metered the accepted run.
func (b *BillingService) reserveRun(ctx context.Context, tenant string) (admitted bool, err error) {
	if b.usage == nil {
		return true, nil
	}
	limit := b.runLimit(ctx, tenant)
	if limit <= 0 {
		// Uncapped: meter the run, always admit.
		return true, b.usage.AddRun(ctx, tenant, time.Now())
	}
	if rr, ok := b.usage.(runReserver); ok {
		admitted, rerr := rr.AddRunIfUnder(ctx, tenant, time.Now(), limit)
		if rerr != nil {
			// Fail open, matching the documented contract: a store error must
			// not block runs, and admitted is only meaningful when err == nil.
			return true, rerr
		}
		return admitted, nil
	}
	// Fallback for a store without atomic reserve (no shipping store): the
	// pre-fix racy read-then-add.
	used, err := b.runsThisMonth(ctx, tenant)
	if err != nil {
		return true, err
	}
	if used >= int64(limit) {
		return false, nil
	}
	return true, b.usage.AddRun(ctx, tenant, time.Now())
}

// concurrencyLimit is the tenant's effective cap on simultaneously in-flight
// runs: the per-org/tier value when entitlements are wired, else the global
// free-tier default. 0 = no cap.
func (b *BillingService) concurrencyLimit(ctx context.Context, tenant string) int {
	if b.effective != nil {
		return b.effective(ctx, tenant).MaxConcurrency
	}
	// Pre-entitlements: the free default caps free tenants only; pro/comped/
	// trial are uncapped.
	if !b.tenantIsFree(ctx, tenant, "concurrency gate") {
		return 0
	}
	return b.freeMaxConcurrency
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

// StripeEventProcessed passes the replay read through to the inner store.
func (c *CachedPlanStore) StripeEventProcessed(ctx context.Context, id string) (bool, error) {
	if dd, ok := c.inner.(StripeEventDeduper); ok {
		return dd.StripeEventProcessed(ctx, id)
	}
	return false, nil
}
