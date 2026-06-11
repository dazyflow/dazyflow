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
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a
// hostile or buggy upstream (reachable via the base_url override) can't
// OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 16 << 20 // 16 MiB

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://api.stripe.com/v1"
)

// SetHTTPBase swaps the Stripe API root (tests point it at httptest).
func SetHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	httpBase = base
}

func baseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return strings.TrimRight(b, "/")
	}
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return httpBase
}

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
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	var rdr io.Reader
	if form != "" {
		rdr = strings.NewReader(form)
	}
	apiKey, err := resolveAPIKey(job)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	// base_url is a tenant-supplied param, so guard the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress
	// allowlist (when set) bounds which public hosts the API key may be
	// sent to.
	if err := hfnet.EgressAllowed(url); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("stripe response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, nil
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

// textInputOr returns the text wired into input port `port` (string or raw
// bytes), or `fallback` when the port is unwired/empty. ok is false only
// when the port carries a NON-text value — a wiring mistake the caller
// rejects. Same pattern as gmail send / the Email drop.
func textInputOr(job core.Job, port, fallback string) (val string, ok bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	}
	return "", false
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

// stripeFailure maps a transport error or a non-2xx Stripe response to
// an error Result. Returns nil when the call succeeded — the shared
// epilogue of every drop's stripeDo call.
func stripeFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		r := params.Err(job, "stripe_http_error", err.Error())
		return &r
	}
	if status < 200 || status >= 300 {
		r := params.Err(job, "stripe_error", fmt.Sprintf("Stripe returned %d: %s", status, extractStripeError(body)))
		return &r
	}
	return nil
}
