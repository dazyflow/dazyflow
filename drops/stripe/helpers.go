// Package stripe hosts the native Stripe connectors: customers (create,
// search), payments (payment link, refund, send invoice), subscriptions
// (list, cancel), webhook triggers (on payment / payment failed /
// subscription canceled, fed by the daemon's Stripe events handler) and
// the raw event feed (list events) for polling everything else.
// Auth is an API key — there's no Stripe OAuth app — resolved from the
// `api_key` param, which defaults to ${secret.STRIPE_API_KEY} so a fresh
// node works as soon as that secret exists (built-in store or a BYO
// manager via ${vault./aws./gcp.…}).
//
// "Fire on new payment" has a real trigger (stripe_on_payment, fed by the
// daemon's Stripe events webhook). Other event reactions ("failed invoice",
// "new subscription") compose via poll_trigger → stripe_list_events (cursor
// in `after_id`, next cursor out of `last_id`, persisted with secret_set) →
// for_each — the same pattern the Gmail and Notion connectors document.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/apibase"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 16 << 20 // 16 MiB

var httpBase = apibase.New("https://api.stripe.com/v1")

// SetHTTPBase swaps the Stripe API root (tests point it at httptest).
func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string { return httpBase.For(job) }

// resolveAPIKey reads the `api_key` param. The schema defaults it to
// ${secret.STRIPE_API_KEY}, which the engine resolves before Execute —
// so an empty value here means the secret isn't set (or the author
// blanked the param), and the error says exactly that.
func resolveAPIKey(job core.Job) (string, error) {
	key, _ := params.StringOpt(job.Params, "api_key")
	if key == "" {
		return "", fmt.Errorf("no Stripe API key: add a STRIPE_API_KEY secret (the api_key param resolves ${secret.STRIPE_API_KEY} by default) or set api_key on the step")
	}
	return key, nil
}

// stripeDo runs one authenticated Stripe API call. POSTs are form-encoded
// (Stripe's protocol) and carry the job's Idempotency-Key so a retried
// node can't double-create or double-refund — Stripe honors the header
// natively. Returns status + body; the caller maps non-2xx via
// extractStripeError.
func stripeDo(ctx context.Context, job core.Job, method, url string, form string) (int, []byte, error) {
	return stripeDoIdem(ctx, job, method, url, form, job.IdempotencyKey())
}

// stripeDoIdem is stripeDo with an explicit Idempotency-Key — for drops
// that make SEVERAL calls per execution (send invoice). Each step gets a
// distinct key derived from the job's (e.g. key+":finalize"), so a retry
// after a partial failure replays completed steps as Stripe-side no-ops
// instead of duplicating them.
func stripeDoIdem(ctx context.Context, job core.Job, method, url, form, idemKey string) (int, []byte, error) {
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	apiKey, err := resolveAPIKey(job)
	if err != nil {
		return 0, nil, err
	}
	var b []byte
	if form != "" {
		b = []byte(form)
	}
	headers := map[string]string{"Authorization": "Bearer " + apiKey}
	if form != "" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	if method == http.MethodPost {
		headers["Idempotency-Key"] = idemKey
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress
	// allowlist (when set) bounds which public hosts the API key may be sent to.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, b, timeoutMS, maxResponseBytes)
	return status, raw, err
}

// extractStripeError pulls error.message (plus code when set) out of a
// Stripe error body, so "No such payment_intent: pi_123" reaches the
// user instead of a bare HTTP status.
func extractStripeError(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		if e.Error.Code != "" {
			return e.Error.Code + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
}

// numberInputOr returns the whole number wired into input port `port` (a
// JSON number or numeric text), or `fallback` when the port is unwired or
// empty. ok is false when the port carries anything else — including a
// fractional number, which is a wiring mistake (quantities and minor-unit
// amounts are integers), not something to silently truncate.
func numberInputOr(job core.Job, port string, fallback int) (int, bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	fromText := func(s string) (int, bool) {
		s = strings.TrimSpace(s)
		if s == "" {
			return fallback, true
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	switch v := in.Inline.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case string:
		return fromText(v)
	case []byte:
		return fromText(string(v))
	}
	return 0, false
}

// paymentTriggerOutputs is the output-port set shared by the
// payment_intent.* triggers (stripe_on_payment / stripe_on_payment_failed):
// the scalar fields pulled out of the event, plus the raw payment intent and
// the whole webhook event as wireable JSON pins (so compositions can template
// across fields the scalar pins don't surface). The failed trigger prepends
// its own 'Failure reason' pin. Returns a fresh slice per call so a caller
// can safely append to it.
func paymentTriggerOutputs() []core.Port {
	return []core.Port{
		{Port: "amount_display", Label: "Amount (display)", MIME: []string{"text/plain"}},
		{Port: "amount", Label: "Amount (minor units)", MIME: []string{"text/plain"}},
		{Port: "currency", Label: "Currency", MIME: []string{"text/plain"}},
		{Port: "customer_email", Label: "Customer email", MIME: []string{"text/plain"}},
		{Port: "description", Label: "Description", MIME: []string{"text/plain"}},
		{Port: "payment_id", Label: "Payment ID", MIME: []string{"text/plain"}},
		{Port: "payment", Label: "Payment intent", MIME: []string{"application/json"}},
		{Port: "event", Label: "Raw event", MIME: []string{"application/json"}},
	}
}

// noPaymentTriggerData is the standalone-run result shared by the
// payment_intent.* triggers. They're pre-completed by the daemon's Stripe
// events handler when a real webhook arrives, so a manual run has no event to
// emit — `message`/`details` explain the specific trigger and how to test it.
func noPaymentTriggerData(job core.Job, message, details string) (core.Result, error) {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error: &core.JobError{
			Code:    "no_trigger_data",
			Message: message,
			Details: details,
		},
	}, nil
}

// stripeFailure maps a transport error or a non-2xx Stripe response to
// an error Result. Returns nil when the call succeeded — the shared
// epilogue of every drop's stripeDo call. Delegates to params.HTTPFailure
// (the shared transport-error/non-2xx epilogue), keeping Stripe's exact
// error codes ("stripe_http_error"/"stripe_error") and message format.
func stripeFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "stripe", "Stripe", status, body, err, extractStripeError)
}
