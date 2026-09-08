// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// The billing surface.

type billingAPI struct {
	auditor
	svc     *Service
	Billing *BillingHandler
}

func (h *HTTPGateway) billingAPI() *billingAPI {
	return &billingAPI{auditor: h.auditor(), svc: h.svc, Billing: h.Billing}
}

const maxStripeEventBytes = 1 << 20

// Nil when the deployment runs no paid billing.
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

func (h *billingAPI) billingMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	// The source of truth: a stored plan can lag a lapsed subscription.
	effPlan := h.svc.effectiveLimits(r.Context(), tenant).Plan
	var runsThisMonth int64
	if h.svc.Usage != nil {
		runsThisMonth, _ = h.svc.runsThisMonth(r.Context(), tenant)
	}
	// Whether this deployment runs paid billing at all.
	billingEnabled := h.Billing != nil && h.Billing.Stripe != nil
	resp := map[string]any{
		"plan":                 effPlan,
		"subscription_status":  plan.SubscriptionStatus,
		"cancel_at_period_end": plan.CancelAtPeriodEnd,
		"free_runs_per_month":  h.svc.FreeRunsPerMonth,
		"runs_this_month":      runsThisMonth,
		"billing_enabled":      billingEnabled,
		"polling_allowed":      !h.svc.FreePollingDisabled || effPlan == PlanPro,
		"can_upgrade":          billingEnabled && effPlan != PlanPro,
		"can_manage":           billingEnabled && plan.StripeCustomerID != "",
	}
	if !plan.CurrentPeriodEnd.IsZero() {
		resp["current_period_end"] = plan.CurrentPeriodEnd.UTC().Format(time.RFC3339)
	}
	writeJSON(rw, http.StatusOK, resp)
}

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

// Limits resolved server-side, so the page cannot disagree with the gate.
type planOption struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Plan      string     `json:"plan"`
	IsCurrent bool       `json:"is_current"`
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

func planLimitsFrom(e EffectiveLimits) planLimits {
	return planLimits{
		RunsPerMonth:      e.RunsPerMonth,
		MaxFlows:          e.MaxFlows,
		MaxGraphNodes:     e.MaxGraphNodes,
		DiskQuotaBytes:    e.DiskQuotaBytes,
		MaxTimeoutSeconds: e.MaxTimeoutSeconds,
		RetentionDays:     e.RetentionDays,
		MaxConcurrency:    e.MaxConcurrency,
		MaxMembers:        e.MaxMembers,
		PollingAllowed:    e.PollingAllowed,
	}
}

func isBuiltinTierID(id string) bool { return id == "free" || id == PlanPro }

func (h *billingAPI) plansMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, ok := resolveTenantScope(rw, r, p)
	if !ok {
		return
	}
	ctx := r.Context()
	cur := h.svc.effectiveLimits(ctx, tenant)

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
		if cur.TierID != "" && !isBuiltinTierID(cur.TierID) {
			if t, ok := h.svc.Entitlements.GetTier(ctx, cur.TierID); ok {
				plans = append(plans, resolveTier(t))
			}
		}
	} else {
		free := ResolveEffective(nil, nil, def, PlanFree, now)
		pro := ResolveEffective(nil, nil, def, PlanPro, now)
		plans = append(plans,
			planOption{ID: "free", Name: "Free", Plan: free.Plan, Limits: planLimitsFrom(free)},
			planOption{ID: PlanPro, Name: "Pro", Plan: pro.Plan, Limits: planLimitsFrom(pro)},
		)
	}
	if cur.TierID == "" || isBuiltinTierID(cur.TierID) {
		plans = append(plans, planOption{
			ID:        "enterprise",
			Name:      "Enterprise",
			Plan:      PlanPro,
			IsContact: true,
			Limits:    planLimits{PollingAllowed: true},
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
		CanUpgrade:    h.Billing != nil && h.Billing.Stripe != nil && cur.Plan != PlanPro,
		CanManage:     h.Billing != nil && h.Billing.Stripe != nil && customerID != "",
		Plans:         plans,
	})
}

func (h *billingAPI) billingCheckout(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Billing == nil || h.Billing.Stripe == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"billing is not enabled on this deployment")
		return
	}
	tenant, ok := resolveTenantScope(rw, r, p)
	if !ok {
		return
	}
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		writeAPIError(rw, http.StatusInternalServerError, "not_configured",
			"DAZYFLOW_PUBLIC_BASE_URL must be set for Checkout redirects")
		return
	}
	// The UI hides Upgrade, but a stale tab could still post it.
	var customerID string
	if h.svc.Plans != nil {
		plan, err := h.svc.Plans.GetPlan(r.Context(), tenant)
		if err != nil {
			writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if liveSubscription(plan) {
			writeAPIError(rw, http.StatusConflict, "already_subscribed",
				"this organization already has an active subscription — manage it from billing")
			return
		}
		customerID = plan.StripeCustomerID
	}
	// Pinned to the tenant just billed, or the user lands in another org.
	u, err := h.Billing.Stripe.CreateCheckoutSession(r.Context(), tenant, customerID,
		withOrg(base+"/usage?checkout=success", tenant),
		withOrg(base+"/usage?checkout=cancelled", tenant))
	if err != nil {
		writeAPIError(rw, http.StatusBadGateway, "stripe_error", err.Error())
		return
	}
	h.audit(r.Context(), p, "billing.checkout", tenant, "")
	writeJSON(rw, http.StatusOK, map[string]string{"url": u})
}

func (h *billingAPI) billingPortal(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
		withOrg(strings.TrimRight(h.svc.PublicBaseURL, "/")+"/usage", tenant))
	if err != nil {
		writeAPIError(rw, http.StatusBadGateway, "stripe_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]string{"url": u})
}

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
			CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
			CurrentPeriodEnd  int64             `json:"current_period_end"`
			Items             struct {
				Data []struct {
					CurrentPeriodEnd int64 `json:"current_period_end"`
				} `json:"data"`
			} `json:"items"`
		} `json:"object"`
	} `json:"data"`
}

func (h *billingAPI) stripeEvents(rw http.ResponseWriter, r *http.Request) {
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
	// Stripe retries, so the event id dedupes; order is not guaranteed either.
	dd, dedupeOK := h.svc.Plans.(StripeEventDeduper)
	dedupeOK = dedupeOK && ev.ID != ""
	if dedupeOK {
		if processed, err := dd.StripeEventProcessed(r.Context(), ev.ID); err == nil && processed {
			h.Billing.logger.Printf("replayed event %s (%s) — already processed, acking", ev.ID, ev.Type)
			rw.WriteHeader(http.StatusOK)
			return
		}
	}
	if err := h.applyStripeEvent(r, ev); err != nil {
		// 500 so Stripe retries: a plan flip must not be lost to a transient failure.
		h.Billing.logger.Printf("apply %s: %v", ev.Type, err)
		http.Error(rw, "apply failed", http.StatusInternalServerError)
		return
	}
	if dedupeOK {
		if _, err := dd.MarkStripeEvent(r.Context(), ev.ID); err != nil {
			h.Billing.logger.Printf("mark event %s: %v", ev.ID, err)
		}
	}
	rw.WriteHeader(http.StatusOK)
}

func (h *billingAPI) applyStripeEvent(r *http.Request, ev stripeEvent) error {
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
		// past_due is deliberately NOT a drop: a card retry is still in flight.
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
			CancelAtPeriodEnd:    obj.CancelAtPeriodEnd,
		}
		// Top-level pre-2025-03-31, per line item after, so both are read.
		periodEnd := obj.CurrentPeriodEnd
		for _, it := range obj.Items.Data {
			if it.CurrentPeriodEnd > periodEnd {
				periodEnd = it.CurrentPeriodEnd
			}
		}
		if periodEnd > 0 {
			tp.CurrentPeriodEnd = time.Unix(periodEnd, 0).UTC()
		}
		return h.svc.Plans.SetPlan(r.Context(), tp)
	default:
		return nil
	}
}
