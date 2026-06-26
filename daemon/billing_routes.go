package daemon

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Billing surface (T3):
//
//	GET  /api/v1/me/billing            → plan + limit + this month's runs
//	POST /api/v1/me/billing/checkout   → Stripe Checkout URL (upgrade)
//	POST /api/v1/me/billing/portal     → Stripe billing-portal URL (manage)
//	POST /api/v1/events/stripe         → webhook (signature is the auth)
//
// The first three are requireAuth'd; the webhook is unauthenticated by
// nature and verified via Stripe's HMAC signature instead, mirroring the
// GitHub/Slack event endpoints.

// maxStripeEventBytes caps incoming webhook payloads — Stripe events are
// a few KB; 1 MiB is generous headroom.
const maxStripeEventBytes = 1 << 20

// BillingHandler holds the Stripe wiring the billing routes need. Nil
// fields degrade gracefully: no Stripe client → checkout/portal return
// 501; no webhook secret → the events route returns 501.
type BillingHandler struct {
	Stripe        *StripeClient
	WebhookSecret string
	logger        *log.Logger
}

func NewBillingHandler(stripe *StripeClient, webhookSecret string) *BillingHandler {
	return &BillingHandler{
		Stripe:        stripe,
		WebhookSecret: webhookSecret,
		logger:        log.New(log.Writer(), "billing: ", log.LstdFlags),
	}
}

// resolveTenantScope applies the shared /me/* scope rule: the
// principal's own tenant unless a platform admin asks about another.
// Used by the usage and billing handlers.
func resolveTenantScope(rw http.ResponseWriter, r *http.Request, p core.Principal) (string, bool) {
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_scope",
			"tenant required (no principal binding)")
		return "", false
	}
	if p.Tenant != "" && tenant != p.Tenant && !isPlatformAdmin(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			"cannot act on another tenant's billing")
		return "", false
	}
	return tenant, true
}

// GET /api/v1/me/billing — everything the Usage page needs to render the
// plan state: plan, whether upgrading is possible on this deployment,
// the free-tier cap (0 = no enforcement), and this month's run count.
func (h *HTTPGateway) billingMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, ok := resolveTenantScope(rw, r, p)
	if !ok {
		return
	}
	plan := TenantPlan{Tenant: tenant, Plan: PlanFree}
	if h.svc.Plans != nil {
		var err error
		if plan, err = h.svc.Plans.GetPlan(r.Context(), tenant); err != nil {
			writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	var runsThisMonth int64
	if h.svc.Usage != nil {
		runsThisMonth, _ = h.svc.runsThisMonth(r.Context(), tenant)
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"plan":                plan.Plan,
		"subscription_status": plan.SubscriptionStatus,
		"free_runs_per_month": h.svc.FreeRunsPerMonth,
		"runs_this_month":     runsThisMonth,
		// polling_allowed tells the Usage page why a free tenant's
		// schedules aren't firing on gated deployments.
		"polling_allowed": !h.svc.FreePollingDisabled || plan.Plan == PlanPro,
		// Upgrade is offered only when Stripe is actually configured;
		// manage (portal) additionally needs an existing customer.
		"can_upgrade": h.Billing != nil && h.Billing.Stripe != nil && plan.Plan != PlanPro,
		"can_manage":  h.Billing != nil && h.Billing.Stripe != nil && plan.StripeCustomerID != "",
	})
}

// planLimits is the display-resolved limit set for one plan on the /me/plans
// comparison. Mirrors EffectiveLimits but normalized for presentation: a
// pro-granting plan reports runs_per_month 0 (= unlimited) because the run gate
// bypasses the cap for any non-free plan (see checkRunQuota), regardless of the
// numeric value the tier happens to inherit.
type planLimits struct {
	RunsPerMonth      int   `json:"runs_per_month"`
	MaxFlows          int   `json:"max_flows"`
	MaxGraphNodes     int   `json:"max_graph_nodes"`
	DiskQuotaBytes    int64 `json:"disk_quota_bytes"`
	MaxTimeoutSeconds int   `json:"max_timeout_seconds"`
	RetentionDays     int   `json:"retention_days"`
	MaxConcurrency    int   `json:"max_concurrency"`
	MaxMembers        int   `json:"max_members"`
	PollingAllowed    bool  `json:"polling_allowed"`
}

// planOption is one selectable plan in the comparison. Limits are resolved
// server-side so the client never re-implements ResolveEffective; the client
// only formats and diffs the numbers.
type planOption struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Plan      string     `json:"plan"`
	IsCurrent bool       `json:"is_current"`
	// IsContact marks a sales-led plan (Enterprise) that isn't self-serve:
	// the client shows a "Contact sales" CTA instead of an upgrade button.
	// Its limits are all-unlimited (0) — the real numbers are set per-customer
	// via a comp/custom tier.
	IsContact bool       `json:"is_contact,omitempty"`
	Limits    planLimits `json:"limits"`
}

type plansResponse struct {
	CurrentPlan   string       `json:"current_plan"`
	CurrentTierID string       `json:"current_tier_id"`
	RunsThisMonth int64        `json:"runs_this_month"`
	CanUpgrade    bool         `json:"can_upgrade"`
	CanManage     bool         `json:"can_manage"`
	Plans         []planOption `json:"plans"`
}

// planLimitsFrom projects resolved EffectiveLimits onto the display shape,
// applying the "pro = unlimited runs" normalization.
func planLimitsFrom(e EffectiveLimits) planLimits {
	runs, concurrency, members, retention := e.RunsPerMonth, e.MaxConcurrency, e.MaxMembers, e.RetentionDays
	if e.Plan == PlanPro {
		// Pro/comped/trial bypass the free-only gates, so report the dims they
		// don't enforce as 0 (= unlimited), matching the enforcement reality:
		// runs (checkRunQuota), concurrency (checkConcurrencyQuota), seats
		// (seatQuotaExceeded), and retention (RunLogRetentionDays → global cap).
		runs, concurrency, members, retention = 0, 0, 0, 0
	}
	return planLimits{
		RunsPerMonth:      runs,
		MaxFlows:          e.MaxFlows,
		MaxGraphNodes:     e.MaxGraphNodes,
		DiskQuotaBytes:    e.DiskQuotaBytes,
		MaxTimeoutSeconds: e.MaxTimeoutSeconds,
		RetentionDays:     retention,
		MaxConcurrency:    concurrency,
		MaxMembers:        members,
		PollingAllowed:    e.PollingAllowed,
	}
}

// isBuiltinTierID reports the two seeded tier ids the comparison offers as
// self-serve options. Anything else is an admin-assigned custom (comp) tier.
func isBuiltinTierID(id string) bool { return id == "free" || id == PlanPro }

// GET /api/v1/me/plans — the data-driven plan comparison the Plans page
// renders. Returns the resolved limits for each self-serve plan (built-in free
// + pro, plus the org's own tier when it's a custom comp) so the client can
// show what differs from the current plan without any per-tier copy. Limits are
// resolved through the same ResolveEffective the enforcement paths use, so the
// catalog and reality agree.
func (h *HTTPGateway) plansMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, ok := resolveTenantScope(rw, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	cur := h.svc.effectiveLimits(ctx, tenant)

	// Stripe customer id drives the "manage billing" affordance; the plan
	// store also gives the customer record. Best-effort — absent store leaves
	// can_manage false.
	var customerID string
	if h.svc.Plans != nil {
		if tp, err := h.svc.Plans.GetPlan(ctx, tenant); err == nil {
			customerID = tp.StripeCustomerID
		}
	}
	var runsThisMonth int64
	if h.svc.Usage != nil {
		runsThisMonth, _ = h.svc.runsThisMonth(ctx, tenant)
	}

	// Which built-in/custom option represents the org's current standing. The
	// effective PLAN is the source of truth (a Stripe-pro org keeps the default
	// "free" tier id but is really on pro); a custom comp tier is keyed by its
	// own id so it, not the generic pro card, reads as current.
	currentKey := cur.Plan // "free" | "pro"
	if cur.TierID != "" && !isBuiltinTierID(cur.TierID) {
		currentKey = cur.TierID
	}

	def := h.svc.limitDefaults()
	now := time.Now()
	resolveTier := func(t Tier) planOption {
		e := ResolveEffective(nil, &t, def, t.Plan, now)
		return planOption{ID: t.ID, Name: t.Name, Plan: e.Plan, Limits: planLimitsFrom(e)}
	}

	var plans []planOption
	if h.svc.Entitlements != nil {
		for _, id := range []string{"free", PlanPro} {
			if t, ok := h.svc.Entitlements.GetTier(ctx, id); ok {
				plans = append(plans, resolveTier(t))
			}
		}
		// Surface a custom comp tier as the org's own option so it reads as
		// "your plan" rather than silently mapping onto free/pro.
		if cur.TierID != "" && !isBuiltinTierID(cur.TierID) {
			if t, ok := h.svc.Entitlements.GetTier(ctx, cur.TierID); ok {
				plans = append(plans, resolveTier(t))
			}
		}
	} else {
		// Pre-entitlement deploys have no tier store: synthesize the two plans
		// straight from the global defaults + plan semantics.
		free := ResolveEffective(nil, nil, def, PlanFree, now)
		pro := ResolveEffective(nil, nil, def, PlanPro, now)
		plans = append(plans,
			planOption{ID: "free", Name: "Free", Plan: free.Plan, Limits: planLimitsFrom(free)},
			planOption{ID: PlanPro, Name: "Pro", Plan: pro.Plan, Limits: planLimitsFrom(pro)},
		)
	}
	// Enterprise is sales-led, not a self-serve Stripe plan: a hosted tier for
	// larger orgs whose real limits are provisioned per-customer via a comp/
	// custom tier. Surface it as a contact-only card (all limits unlimited) so
	// the comparison renders three columns. Skip it when the org is already on
	// a custom (non-built-in) tier — that tier IS their enterprise plan and is
	// already listed above as current.
	if cur.TierID == "" || isBuiltinTierID(cur.TierID) {
		plans = append(plans, planOption{
			ID:        "enterprise",
			Name:      "Enterprise",
			Plan:      PlanPro,
			IsContact: true,
			// All numeric limits 0 = unlimited; polling included.
			Limits: planLimits{PollingAllowed: true},
		})
	}

	for i := range plans {
		if plans[i].ID == currentKey {
			plans[i].IsCurrent = true
		}
	}

	writeJSON(rw, http.StatusOK, plansResponse{
		CurrentPlan:   cur.Plan,
		CurrentTierID: cur.TierID,
		RunsThisMonth: runsThisMonth,
		// Offer upgrade only when Stripe is configured and the org isn't
		// already effectively pro (comp/trial included, via cur.Plan).
		CanUpgrade: h.Billing != nil && h.Billing.Stripe != nil && cur.Plan != PlanPro,
		CanManage:  h.Billing != nil && h.Billing.Stripe != nil && customerID != "",
		Plans:      plans,
	})
}

// POST /api/v1/me/billing/checkout — mint a Stripe Checkout session for
// the pro plan and return its hosted URL; the web client redirects there.
func (h *HTTPGateway) billingCheckout(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Billing == nil || h.Billing.Stripe == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"billing is not enabled on this deployment")
		return
	}
	tenant, ok := resolveTenantScope(rw, r, p)
	if !ok {
		return
	}
	base := h.svc.PublicBaseURL
	if base == "" {
		writeAPIError(rw, http.StatusInternalServerError, "not_configured",
			"DAZYFLOW_PUBLIC_BASE_URL must be set for Checkout redirects")
		return
	}
	u, err := h.Billing.Stripe.CreateCheckoutSession(r.Context(), tenant,
		base+"/usage?checkout=success", base+"/usage?checkout=cancelled")
	if err != nil {
		writeAPIError(rw, http.StatusBadGateway, "stripe_error", err.Error())
		return
	}
	h.audit(r.Context(), p, "billing.checkout", tenant, "")
	writeJSON(rw, http.StatusOK, map[string]string{"url": u})
}

// POST /api/v1/me/billing/portal — mint a billing-portal session for an
// already-subscribed tenant (manage payment method, cancel, invoices).
func (h *HTTPGateway) billingPortal(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Billing == nil || h.Billing.Stripe == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"billing is not enabled on this deployment")
		return
	}
	tenant, ok := resolveTenantScope(rw, r, p)
	if !ok {
		return
	}
	if h.svc.Plans == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"no plan store on this deployment")
		return
	}
	plan, err := h.svc.Plans.GetPlan(r.Context(), tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if plan.StripeCustomerID == "" {
		writeAPIError(rw, http.StatusConflict, "no_subscription",
			"this organization has no billing account yet — upgrade first")
		return
	}
	u, err := h.Billing.Stripe.CreatePortalSession(r.Context(), plan.StripeCustomerID,
		h.svc.PublicBaseURL+"/usage")
	if err != nil {
		writeAPIError(rw, http.StatusBadGateway, "stripe_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]string{"url": u})
}

// stripeEvent is the slice of Stripe's event envelope the plan-sync
// cares about.
type stripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID                string            `json:"id"`
			Customer          string            `json:"customer"`
			Subscription      string            `json:"subscription"`
			ClientReferenceID string            `json:"client_reference_id"`
			Status            string            `json:"status"`
			Metadata          map[string]string `json:"metadata"`
			CurrentPeriodEnd  int64             `json:"current_period_end"`
		} `json:"object"`
	} `json:"data"`
}

// POST /api/v1/events/stripe — Stripe webhook. The signature header is
// the only auth (same model as the GitHub events endpoint). Handled
// events flip the tenant's plan; everything else acks 200 so Stripe
// stops retrying.
func (h *HTTPGateway) stripeEvents(rw http.ResponseWriter, r *http.Request) {
	if h.Billing == nil || h.Billing.WebhookSecret == "" {
		http.Error(rw, "Stripe events endpoint not configured (set DAZYFLOW_STRIPE_WEBHOOK_SECRET)",
			http.StatusNotImplemented)
		return
	}
	if h.svc.Plans == nil {
		http.Error(rw, "no plan store configured", http.StatusNotImplemented)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxStripeEventBytes+1))
	_ = r.Body.Close()
	if err != nil || int64(len(body)) > maxStripeEventBytes {
		http.Error(rw, "bad body", http.StatusBadRequest)
		return
	}
	if err := VerifyStripeSignature(r.Header.Get("Stripe-Signature"), body,
		h.Billing.WebhookSecret, time.Now()); err != nil {
		http.Error(rw, "signature verification failed", http.StatusUnauthorized)
		return
	}
	var ev stripeEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(rw, "bad event payload", http.StatusBadRequest)
		return
	}
	// Stripe retries deliveries; the event id dedupes them so a replay
	// acks without re-applying. Fails OPEN on store errors — the plan
	// upserts are idempotent, so processing twice beats dropping one.
	if dd, ok := h.svc.Plans.(StripeEventDeduper); ok && ev.ID != "" {
		if first, err := dd.MarkStripeEvent(r.Context(), ev.ID); err == nil && !first {
			h.Billing.logger.Printf("replayed event %s (%s) — already processed, acking", ev.ID, ev.Type)
			rw.WriteHeader(http.StatusOK)
			return
		}
	}
	if err := h.applyStripeEvent(r, ev); err != nil {
		// 500 so Stripe retries — plan flips must not be lost to a
		// transient store error.
		h.Billing.logger.Printf("apply %s: %v", ev.Type, err)
		http.Error(rw, "apply failed", http.StatusInternalServerError)
		return
	}
	rw.WriteHeader(http.StatusOK)
}

// applyStripeEvent maps subscription lifecycle events onto the plan
// store. The tenant arrives via client_reference_id (checkout) or the
// subscription metadata stamped at Checkout time (lifecycle events).
func (h *HTTPGateway) applyStripeEvent(r *http.Request, ev stripeEvent) error {
	obj := ev.Data.Object
	switch ev.Type {
	case "checkout.session.completed":
		tenant := obj.ClientReferenceID
		if tenant == "" {
			h.Billing.logger.Printf("checkout.session.completed without client_reference_id (session %s) — ignoring", obj.ID)
			return nil
		}
		return h.svc.Plans.SetPlan(r.Context(), TenantPlan{
			Tenant:               tenant,
			Plan:                 PlanPro,
			StripeCustomerID:     obj.Customer,
			StripeSubscriptionID: obj.Subscription,
			SubscriptionStatus:   "active",
		})
	case "customer.subscription.updated", "customer.subscription.deleted":
		tenant := obj.Metadata["tenant"]
		if tenant == "" {
			h.Billing.logger.Printf("%s without tenant metadata (sub %s) — ignoring", ev.Type, obj.ID)
			return nil
		}
		plan := PlanPro
		// Deleted, or updated into a dead state, drops to free. past_due
		// stays pro — Stripe is still retrying payment; cutting access on
		// the first failed charge is hostile.
		if ev.Type == "customer.subscription.deleted" ||
			obj.Status == "canceled" || obj.Status == "unpaid" ||
			obj.Status == "incomplete_expired" {
			plan = PlanFree
		}
		tp := TenantPlan{
			Tenant:               tenant,
			Plan:                 plan,
			StripeCustomerID:     obj.Customer,
			StripeSubscriptionID: obj.ID,
			SubscriptionStatus:   obj.Status,
		}
		if obj.CurrentPeriodEnd > 0 {
			tp.CurrentPeriodEnd = time.Unix(obj.CurrentPeriodEnd, 0).UTC()
		}
		return h.svc.Plans.SetPlan(r.Context(), tp)
	default:
		// Unhandled event types ack silently — Stripe sends many.
		return nil
	}
}
