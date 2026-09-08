// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

	"github.com/dazyflow/dazyflow/core"
)

const (
	stripeOnPaymentModuleID              = "stripe_on_payment"
	stripeOnPaymentFailedModuleID        = "stripe_on_payment_failed"
	stripeOnSubscriptionCanceledModuleID = "stripe_on_subscription_canceled"
)

// Per-tenant: the signing secret is the org's, not the deployment's.
const stripeTriggerSecretName = "STRIPE_WEBHOOK_SECRET"

// Capped: this endpoint is unauthenticated until the signature verifies.
const maxStripeTriggerBodyBytes = 1 * 1024 * 1024

// Verifies the signature BEFORE anything else is trusted, against the secret
// BOUND to the URL's tenant — resolving it any other way would let one org's
// signature authenticate a delivery aimed at another.
type StripeEventsHandler struct {
	svc    *Service
	logger *log.Logger
	now    func() time.Time
	// Tests use it to await an asynchronous dispatch.
	fanoutDone func()
}

func NewStripeEventsHandler(svc *Service) *StripeEventsHandler {
	return &StripeEventsHandler{
		svc:    svc,
		logger: log.New(log.Writer(), "stripe-events: ", log.LstdFlags),
		now:    time.Now,
	}
}

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

	// Both come back the same, so a prober learns nothing from which failed.
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
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	}
}

type stripeTriggerEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type stripePaymentIntent struct {
	ID               string `json:"id"`
	Amount           int64  `json:"amount"`
	AmountReceived   int64  `json:"amount_received"`
	Currency         string `json:"currency"`
	ReceiptEmail     string `json:"receipt_email"`
	Description      string `json:"description"`
	LastPaymentError *struct {
		Message string `json:"message"`
	} `json:"last_payment_error"`
}

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
	go h.runFanout(context.Background(), tenant, stripeOnPaymentModuleID, seed)

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
	go h.runFanout(context.Background(), tenant, stripeOnPaymentFailedModuleID, seed)

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
	go h.runFanout(context.Background(), tenant, stripeOnSubscriptionCanceledModuleID, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

var stripeZeroDecimalCurrencies = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true,
	"kmf": true, "krw": true, "mga": true, "pyg": true, "rwf": true,
	"ugx": true, "vnd": true, "vuv": true, "xaf": true, "xof": true,
	"xpf": true,
}

func formatStripeAmount(minor int64, currency string) string {
	code := strings.ToUpper(currency)
	if stripeZeroDecimalCurrencies[strings.ToLower(currency)] {
		return fmt.Sprintf("%d %s", minor, code)
	}
	return fmt.Sprintf("%d.%02d %s", minor/100, minor%100, code)
}

func (h *StripeEventsHandler) runFanout(ctx context.Context, tenant, moduleID string, seed core.Result) {
	defer func() {
		if h.fanoutDone != nil {
			h.fanoutDone()
		}
	}()
	h.fanoutSeed(ctx, tenant, moduleID, seed)
}

func (h *StripeEventsHandler) fanoutSeed(ctx context.Context, tenant, moduleID string, seed core.Result) {
	fanoutSeed(ctx, h.svc, h.logger, "dazyflow-stripe-events", tenant, moduleID, seed,
		func(n core.Node) bool { return n.Module == moduleID })
}
