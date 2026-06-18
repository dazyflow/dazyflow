package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

const (
	stripeOnPaymentModuleID              = "stripe_on_payment"
	stripeOnPaymentFailedModuleID        = "stripe_on_payment_failed"
	stripeOnSubscriptionCanceledModuleID = "stripe_on_subscription_canceled"
)

// stripeTriggerSecretName is the tenant secret holding the webhook
// endpoint's signing secret (whsec_…). Organization scope (bare name),
// same convention as STRIPE_API_KEY on the action drops.
const stripeTriggerSecretName = "STRIPE_WEBHOOK_SECRET"

// maxStripeTriggerBodyBytes caps incoming webhook payloads. Stripe
// events are small JSON documents; 1 MiB matches the billing webhook's
// ceiling and leaves headroom for expanded objects.
const maxStripeTriggerBodyBytes = 1 * 1024 * 1024

// StripeEventsHandler verifies tenant Stripe webhook deliveries and
// dispatches payment events to subscribed graphs.
//
// Routing layout:
//
//	POST /api/v1/events/stripe/{tenant}
//	Stripe-Signature: t=<unix>,v1=<hmac-sha256-hex>
//	(JSON body)
//
// Auth: per-tenant, unlike GitHub/Slack's single operator secret.
// Stripe generates a distinct signing secret for every webhook
// endpoint — there is no user-chosen value an operator could share
// across tenants — so the tenant saves their endpoint's whsec_… in
// the encrypted secret store under STRIPE_WEBHOOK_SECRET and the
// handler resolves it per request. (The unsuffixed POST
// /api/v1/events/stripe route is the platform's own billing webhook —
// different secret, different handler.)
//
// Event types handled (per the body's `type` field):
//
//   - payment_intent.succeeded      → stripe_on_payment nodes
//   - payment_intent.payment_failed → stripe_on_payment_failed nodes
//   - customer.subscription.deleted → stripe_on_subscription_canceled nodes
//
// Unknown events ack with 200 so Stripe stops retrying — graphs that
// subscribed get nothing, which is the correct outcome.
type StripeEventsHandler struct {
	svc    *Service
	logger *log.Logger
	// now is injectable so tests can sign within the timestamp
	// tolerance window deterministically.
	now func() time.Time
}

// NewStripeEventsHandler wires a handler against the daemon Service.
// The Service's EncryptedSecrets store must be configured — without it
// there is nowhere to read a tenant's signing secret from, and every
// POST returns 501.
func NewStripeEventsHandler(svc *Service) *StripeEventsHandler {
	return &StripeEventsHandler{
		svc:    svc,
		logger: log.New(log.Writer(), "stripe-events: ", log.LstdFlags),
		now:    time.Now,
	}
}

// ServeHTTP routes a single Stripe webhook POST. Mounted at
// `/api/v1/events/stripe/{tenant}` by HTTPGateway.
func (h *StripeEventsHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.svc.EncryptedSecrets == nil {
		http.Error(rw, "Stripe events endpoint not configured (encrypted secret store required)", http.StatusNotImplemented)
		return
	}
	tenant := r.PathValue("tenant")
	if tenant == "" {
		http.Error(rw, "expected /api/v1/events/stripe/<tenant>", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxStripeTriggerBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		http.Error(rw, "read body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > maxStripeTriggerBodyBytes {
		http.Error(rw, fmt.Sprintf("body exceeds %d bytes", maxStripeTriggerBodyBytes), http.StatusRequestEntityTooLarge)
		return
	}

	// A missing secret and a bad signature both come back 401 with the
	// same body — an unauthenticated caller probing tenant names learns
	// nothing about which tenants exist or have Stripe configured.
	secret, err := h.svc.EncryptedSecrets.GetExact(r.Context(), tenant, stripeTriggerSecretName)
	if err != nil || secret == "" {
		h.logger.Printf("reject %s: no %s secret", tenant, stripeTriggerSecretName)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}
	if err := VerifyStripeSignature(r.Header.Get("Stripe-Signature"), body, secret, h.now()); err != nil {
		h.logger.Printf("reject %s: %v", tenant, err)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}

	var ev stripeTriggerEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(rw, fmt.Sprintf("parse event: %v", err), http.StatusBadRequest)
		return
	}
	switch ev.Type {
	case "payment_intent.succeeded":
		h.dispatchPayment(tenant, ev, body, rw)
	case "payment_intent.payment_failed":
		h.dispatchPaymentFailed(tenant, ev, body, rw)
	case "customer.subscription.deleted":
		h.dispatchSubscriptionCanceled(tenant, ev, body, rw)
	default:
		// Unsubscribed event types — ack so Stripe doesn't retry,
		// but nothing to dispatch.
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	}
}

// stripeTriggerEvent is the decoded envelope of a Stripe event. The
// object stays raw here — each dispatcher decodes the shape its event
// type carries (PaymentIntent, Subscription, …). The named ports are
// extracted up front; the raw event also goes through to the `event`
// output for graphs that need fields we didn't pull out.
type stripeTriggerEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type stripePaymentIntent struct {
	ID string `json:"id"`
	// AmountReceived is what was actually captured; Amount is what was
	// requested. They differ on partial captures, so prefer received.
	Amount           int64  `json:"amount"`
	AmountReceived   int64  `json:"amount_received"`
	Currency         string `json:"currency"`
	ReceiptEmail     string `json:"receipt_email"`
	Description      string `json:"description"`
	LastPaymentError *struct {
		Message string `json:"message"`
	} `json:"last_payment_error"`
}

// paymentPorts builds the output set the two payment triggers share.
// The `payment` port carries the FULL PaymentIntent object (untyped),
// not the typed subset; `event` carries the whole webhook envelope.
func paymentPorts(ev stripeTriggerEvent, body []byte) (stripePaymentIntent, map[string]core.Ref) {
	var pi stripePaymentIntent
	_ = json.Unmarshal(ev.Data.Object, &pi)
	amount := pi.AmountReceived
	if amount == 0 {
		amount = pi.Amount
	}
	var payment, raw any
	_ = json.Unmarshal(ev.Data.Object, &payment)
	_ = json.Unmarshal(body, &raw)
	return pi, map[string]core.Ref{
		"amount_display": {MIME: "text/plain", Inline: formatStripeAmount(amount, pi.Currency)},
		"amount":         {MIME: "text/plain", Inline: fmt.Sprintf("%d", amount)},
		"currency":       {MIME: "text/plain", Inline: strings.ToUpper(pi.Currency)},
		"customer_email": {MIME: "text/plain", Inline: pi.ReceiptEmail},
		"description":    {MIME: "text/plain", Inline: pi.Description},
		"payment_id":     {MIME: "text/plain", Inline: pi.ID},
		"payment":        {MIME: "application/json", Inline: payment},
		"event":          {MIME: "application/json", Inline: raw},
	}
}

func (h *StripeEventsHandler) dispatchPayment(tenant string, ev stripeTriggerEvent, body []byte, rw http.ResponseWriter) {
	_, ports := paymentPorts(ev, body)
	seed := core.Result{Status: core.StatusOK, Output: ports}
	go h.fanoutSeed(context.Background(), tenant, stripeOnPaymentModuleID, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

func (h *StripeEventsHandler) dispatchPaymentFailed(tenant string, ev stripeTriggerEvent, body []byte, rw http.ResponseWriter) {
	pi, ports := paymentPorts(ev, body)
	msg := ""
	if pi.LastPaymentError != nil {
		msg = pi.LastPaymentError.Message
	}
	ports["failure_message"] = core.Ref{MIME: "text/plain", Inline: msg}
	seed := core.Result{Status: core.StatusOK, Output: ports}
	go h.fanoutSeed(context.Background(), tenant, stripeOnPaymentFailedModuleID, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

func (h *StripeEventsHandler) dispatchSubscriptionCanceled(tenant string, ev stripeTriggerEvent, body []byte, rw http.ResponseWriter) {
	var sub struct {
		ID         string `json:"id"`
		Customer   string `json:"customer"`
		EndedAt    int64  `json:"ended_at"`
		CanceledAt int64  `json:"canceled_at"`
		Items      struct {
			Data []struct {
				Price struct {
					ID       string `json:"id"`
					Nickname string `json:"nickname"`
				} `json:"price"`
			} `json:"data"`
		} `json:"items"`
	}
	_ = json.Unmarshal(ev.Data.Object, &sub)
	// The friendliest plan label available without an extra API call:
	// the price nickname when set, the price id otherwise.
	plan := ""
	if len(sub.Items.Data) > 0 {
		plan = sub.Items.Data[0].Price.Nickname
		if plan == "" {
			plan = sub.Items.Data[0].Price.ID
		}
	}
	endedUnix := sub.EndedAt
	if endedUnix == 0 {
		endedUnix = sub.CanceledAt
	}
	endedAt := ""
	if endedUnix != 0 {
		endedAt = time.Unix(endedUnix, 0).UTC().Format(time.RFC3339)
	}
	var subscription, raw any
	_ = json.Unmarshal(ev.Data.Object, &subscription)
	_ = json.Unmarshal(body, &raw)

	seed := core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"subscription_id": {MIME: "text/plain", Inline: sub.ID},
			"customer":        {MIME: "text/plain", Inline: sub.Customer},
			"plan":            {MIME: "text/plain", Inline: plan},
			"ended_at":        {MIME: "text/plain", Inline: endedAt},
			"subscription":    {MIME: "application/json", Inline: subscription},
			"event":           {MIME: "application/json", Inline: raw},
		},
	}
	go h.fanoutSeed(context.Background(), tenant, stripeOnSubscriptionCanceledModuleID, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

// stripeZeroDecimalCurrencies are the currencies Stripe represents in
// whole units rather than hundredths — per
// https://docs.stripe.com/currencies#zero-decimal.
var stripeZeroDecimalCurrencies = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true,
	"kmf": true, "krw": true, "mga": true, "pyg": true, "rwf": true,
	"ugx": true, "vnd": true, "vuv": true, "xaf": true, "xof": true,
	"xpf": true,
}

// formatStripeAmount renders a minor-unit amount as the human form a
// notification wants: "49.99 USD", "5000 JPY".
func formatStripeAmount(minor int64, currency string) string {
	code := strings.ToUpper(currency)
	if stripeZeroDecimalCurrencies[strings.ToLower(currency)] {
		return fmt.Sprintf("%d %s", minor, code)
	}
	return fmt.Sprintf("%d.%02d %s", minor/100, minor%100, code)
}

// fanoutSeed walks every workspace under the tenant, loads each
// graph, and submits a run for any that declares a node with the
// matching trigger module. Mirrors github_events.fanoutSeed.
func (h *StripeEventsHandler) fanoutSeed(ctx context.Context, tenant, moduleID string, seed core.Result) {
	workspaces, err := h.svc.Workspaces.List(tenant)
	if err != nil {
		h.logger.Printf("list workspaces for %s: %v", tenant, err)
		return
	}
	principal := core.Principal{
		Subject: "hazyflow-stripe-events",
		Tenant:  tenant,
		Roles: []core.Role{{
			Name:        "stripe-events",
			Permissions: []core.Permission{core.PermGraphRun, core.PermGraphAdmin},
		}},
	}
	for _, ws := range workspaces {
		store, err := h.svc.Workspaces.Open(tenant, ws)
		if err != nil {
			h.logger.Printf("open %s/%s: %v", tenant, ws, err)
			continue
		}
		ids, err := store.ListGraphs()
		if err != nil {
			h.logger.Printf("list graphs %s/%s: %v", tenant, ws, err)
			continue
		}
		principal.Workspace = ws
		for _, id := range ids {
			// Match + run the published revision (HEAD fallback for
			// never-published flows): an external event fires the version
			// that was deliberately published, not a draft.
			g, err := store.LoadPublishedOrHead(id)
			if err != nil {
				h.logger.Printf("load %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			// A paused flow must not fire on inbound events — same rule the
			// /trigger webhook enforces. Skip disabled graphs entirely.
			if g.Disabled {
				continue
			}
			seeds := map[string]core.Result{}
			for _, n := range g.Nodes {
				if n.Module == moduleID {
					seeds[n.ID] = seed
				}
			}
			if len(seeds) == 0 {
				continue
			}
			runID, err := h.svc.SubmitGraphWithSeed(ctx, principal, g, seeds)
			if err != nil {
				h.logger.Printf("submit %s/%s/%s: %v", tenant, ws, id, err)
				continue
			}
			h.logger.Printf("fired %s/%s/%s → %s (%d %s seed(s))",
				tenant, ws, id, runID, len(seeds), moduleID)
		}
	}
}
