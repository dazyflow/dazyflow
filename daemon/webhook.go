// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

const webhookInputModuleID = "webhook_input"

// WebhookListener exposes an HTTP endpoint per graph that has a webhook
// trigger. Layout:
//
//	POST /trigger/<tenant>/<workspace>/<graph-id>
//	Authorization: Bearer <secret-from-graph-trigger>   (or ?key=<secret>)
//	(body — passed to the graph as a webhook_input record if present)
//
// Responses:
//
//	202 + JSON {job_id: "..."} on accepted fire
//	401 on bad secret
//	404 on unknown graph or graph without webhook trigger
//	400 on malformed paths
//	402/403 + JSON {error, run} when the OWNER has to fix something (over the
//	        run cap, suspended org, a flow that no longer validates) AND we
//	        kept the delivery: it is stored on the named run for the owner to
//	        retry, so the sender must not resend
//	503 for the same refusals when we could NOT keep the delivery — the only
//	        case where re-sending is the right thing to do
//
// The listener authenticates via the per-graph trigger secret rather
// than the daemon's normal API-key chain. That's intentional: webhook
// callers (Stripe, GitHub, your CI provider) typically don't have a
// Dazyflow API key but do hold a per-integration secret.
type WebhookListener struct {
	svc    *Service
	logger *log.Logger

	// idempotency backs the Idempotency-Key contract on /call. Owned by the
	// listener so a standalone one honours the header too; the gateway
	// replaces it with the daemon's shared cache so there is one TTL and one
	// eviction budget.
	idempotency *idempotencyStore

	// MaxBodyBytes caps the inline body included in graph input.
	MaxBodyBytes int64
}

func NewWebhookListener(svc *Service) *WebhookListener {
	return &WebhookListener{
		svc:          svc,
		logger:       log.New(log.Writer(), "webhook: ", log.LstdFlags),
		MaxBodyBytes: 1 * 1024 * 1024, // 1 MiB default
		idempotency:  newIdempotencyStore(),
	}
}

func (w *WebhookListener) handleTrigger(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/trigger/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected /trigger/<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return
	}
	tenant, workspace, graphID := parts[0], parts[1], parts[2]

	// Every pre-authentication failure below returns the SAME generic 401.
	// Distinct codes/bodies (unknown workspace vs unknown graph vs no
	// webhook trigger vs bad secret) would let an unauthenticated caller
	// enumerate which tenants/workspaces/graphs exist and are webhook-enabled
	// — a discovery oracle. The webhook secret is per-graph, so we must load
	// the graph before we can check it; we just refuse to reveal *why* a
	// pre-auth request failed. (A residual timing difference between
	// existing/missing resources remains, far weaker than the status/body
	// signal this closes.)
	const unauthorized = "unknown endpoint or invalid secret"

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}
	// Fire the PUBLISHED revision — never a draft. An unpublished flow does
	// not receive: same rule the scheduler enforces, so "published" means one
	// thing whatever the trigger. ErrNotPublished takes the same generic 401
	// as every other pre-auth failure below, so the endpoint still doesn't
	// reveal whether a given flow exists.
	g, err := store.LoadPublished(graphID)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}

	keys := core.GraphWebhookSecrets(g)
	switch {
	case len(keys) > 0:
		if !anyKeyMatches(keys, webhookKey(r)) {
			http.Error(rw, unauthorized, http.StatusUnauthorized)
			return
		}
	case core.GraphWebhookPublic(g):
		// The author marked this address open. Possession of the URL is the
		// only credential, exactly like the hosted form — and, like the form,
		// it sits behind the per-IP throttle on the route.
	default:
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}

	// Only AFTER proving possession of the secret do we reveal the flow's
	// disabled state — the caller (e.g. Stripe's webhook UI) holds the
	// secret, so a distinct 403 here is safe and more useful than a retry
	// loop. Paused flows reject inbound webhooks.
	if g.Disabled {
		http.Error(rw, `{"error":{"code":"flow_disabled","message":"flow is currently disabled — re-enable via enable_flow"}}`, http.StatusForbidden)
		return
	}

	// Read the body with a cap so an attacker can't OOM us by posting
	// 100 GB. We *do* read it now (the previous implementation
	// discarded it) so we can feed it to webhook_input nodes.
	var rawBody []byte
	if r.Body != nil {
		limited := io.LimitReader(r.Body, w.MaxBodyBytes+1)
		data, err := io.ReadAll(limited)
		_ = r.Body.Close()
		if err != nil {
			http.Error(rw, "read body", http.StatusBadRequest)
			return
		}
		if int64(len(data)) > w.MaxBodyBytes {
			http.Error(rw, fmt.Sprintf("body exceeds %d bytes", w.MaxBodyBytes), http.StatusRequestEntityTooLarge)
			return
		}
		rawBody = data
	}

	// Build the seed result and apply it to every webhook_input node
	// the graph declares. Multiple webhook_input nodes is unusual but
	// not forbidden — they all receive the same payload.
	seed := buildWebhookSeed(rawBody, r)
	seeds := map[string]core.Result{}
	inputs := 0
	for _, n := range g.Nodes {
		if n.Module != webhookInputModuleID {
			continue
		}
		inputs++
		if triggerNodeDisabled(n) {
			continue
		}
		seeds[n.ID] = seed
	}
	// Every webhook step in this flow is paused. Firing anyway would start a
	// run whose only trigger node the worker immediately skips — a run that
	// does nothing, which is a worse answer than a refusal. A flow with NO
	// webhook step at all is left alone: posting here to kick such a flow is
	// a legitimate use of this endpoint and stays permitted.
	if inputs > 0 && len(seeds) == 0 {
		http.Error(rw, `{"error":{"code":"trigger_disabled","message":"this flow's webhook step is turned off — re-enable the step to accept deliveries"}}`, http.StatusForbidden)
		return
	}

	// Fire the graph as a system principal scoped to the graph's
	// tenant. Trigger-driven runs bypass per-flow visibility because
	// possession of the per-graph webhook secret already proves
	// authorization — graph:admin lets the principal fire private
	// flows without owning them.
	principal := SystemPrincipal("dazyflow-webhook", g.Tenant, g.Workspace)
	// Detached from the request, like /call: a sender that hangs up — a proxy
	// timeout, a flaky mobile link, Stripe giving up at 20s — must not abandon
	// the submit half-written. Cancelling mid-submit could leave a graph
	// record whose node work never queued AND whose failure write also ran on
	// the dead context, i.e. a run stuck `running` with nothing in it. The
	// sender will retry a delivery it never got an answer for, so the cost of
	// finishing the write is a duplicate at worst; the cost of not finishing
	// it is a zombie.
	runID, err := w.svc.SubmitGraphOpts(context.WithoutCancel(r.Context()), principal, g, SubmitOpts{
		Seeds: seeds,
		// Carried by a step of the run that called us (see
		// core.TriggerDepthHeader); 0 for a delivery from anywhere else.
		TriggerDepth: inboundTriggerDepth(r),
	})
	if errors.Is(err, core.ErrTriggerLoop) {
		w.logger.Printf("refused %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, `{"error":{"code":"trigger_loop","message":"this delivery came from a run this flow started — the chain is too deep, so a flow is triggering itself"}}`,
			http.StatusTooManyRequests)
		return
	}
	// A refusal the OWNER has to fix (over the run cap, suspended org, a flow
	// that no longer validates) will refuse the next delivery too, so a 500 was
	// the worst of both worlds: the sender retried a while, gave up, and the
	// event was gone with nothing on our side to show it ever arrived. Keep the
	// delivery as a failed run the owner can Retry, and let whether we KEPT it
	// pick the status — 4xx to stop a sender re-sending what we already hold,
	// 503 to ask for the retry when we don't hold it.
	if err != nil && ownerMustFix(err) {
		w.logger.Printf("refused %s/%s/%s: %v", tenant, workspace, graphID, err)
		capCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		refusedRunID, stored := w.svc.recordRefusedDelivery(capCtx, g, seeds,
			refusalCode(err), refusalMessage(err))
		cancel()
		code, status := refusalCode(err), http.StatusServiceUnavailable
		msg := "this flow is not accepting deliveries right now — the owner has been told; please retry"
		if stored {
			msg = "this flow is not accepting deliveries right now; the delivery has been kept and the owner has been told — do not resend"
			status = http.StatusPaymentRequired
			if !errors.Is(err, core.ErrPlanLimit) {
				status = http.StatusForbidden
			}
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"error": map[string]string{"code": code, "message": msg},
			"run":   refusedRunID,
		})
		return
	}
	if err != nil {
		w.logger.Printf("submit %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, fmt.Sprintf("submit: %v", err), http.StatusInternalServerError)
		return
	}
	w.logger.Printf("fired %s/%s/%s → %s (%d webhook_input seed(s))",
		tenant, workspace, graphID, runID, len(seeds))
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(map[string]string{"job_id": runID})
}

// anyKeyMatches reports whether provided equals any active key. Every
// candidate is compared (no early break) so the work — and thus the timing —
// doesn't depend on which key matched or how many there are. Multi-key
// acceptance is what enables zero-downtime rotation: add a new key, migrate
// callers, revoke the old one.
func anyKeyMatches(keys []string, provided string) bool {
	matched := 0
	for _, k := range keys {
		matched |= subtle.ConstantTimeCompare([]byte(k), []byte(provided))
	}
	return matched == 1
}

// buildWebhookSeed constructs the Result that the webhook handler
// pre-completes webhook_input nodes with. The result has two output
// ports — body and headers — matching the webhook_input manifest.
//
// Body parsing follows Content-Type:
//   - application/json                  → map[string]any (parsed object) or whatever JSON.Unmarshal produces
//   - application/x-www-form-urlencoded → map[string]any (one entry per field, first value)
//   - text/* or no body                 → string
//   - everything else                   → []byte
//
// The form-urlencoded case reuses collectFormValues so a real HTML
// form (or a sender like Twilio/Slack that posts urlencoded) lands the
// same {key: value} object as the hosted form and a JSON webhook —
// ${trigger.body.email} works identically across all three paths.
//
// Headers are flattened to a string map (first value per name) so
// downstream nodes can read them with branch's field-path access.
func buildWebhookSeed(rawBody []byte, r *http.Request) core.Result {
	contentType := r.Header.Get("Content-Type")
	mediaType := contentType
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	// HTTP media types are case-insensitive (RFC 9110 §8.3.1), so a
	// sender using "Application/JSON" or "TEXT/PLAIN" must parse the same
	// as the lowercase form — otherwise it falls through to raw []byte
	// and ${trigger.body.field} silently breaks. Lowercase only this
	// comparison key; bodyMIME below keeps the original header verbatim.
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	var bodyValue any
	switch {
	case len(rawBody) == 0:
		bodyValue = ""
	case mediaType == "application/json":
		var parsed any
		if err := json.Unmarshal(rawBody, &parsed); err == nil {
			bodyValue = parsed
		} else {
			// Fall back to string when JSON is malformed — better to
			// let the graph see the raw text than fail the trigger.
			bodyValue = string(rawBody)
		}
	case mediaType == "application/x-www-form-urlencoded":
		if parsed, err := url.ParseQuery(string(rawBody)); err == nil {
			bodyValue = collectFormValues(nil, parsed)
		} else {
			// Malformed query string — hand the raw text to the graph
			// rather than fail the trigger, mirroring the JSON path.
			bodyValue = string(rawBody)
		}
	case strings.HasPrefix(mediaType, "text/"):
		bodyValue = string(rawBody)
	default:
		bodyValue = rawBody
	}

	headers := make(map[string]any, len(r.Header))
	for k, vs := range r.Header {
		// Never expose credential headers on the body's sibling port: the
		// Authorization header carries this graph's own webhook bearer
		// secret, and Cookie can carry session creds. A downstream node
		// that forwards ${trigger.headers} to an external service would
		// otherwise leak them. Drop both (canonicalized, case-insensitive).
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "Cookie":
			continue
		}
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}

	bodyMIME := contentType
	if bodyMIME == "" {
		bodyMIME = "text/plain"
	}
	return core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"body":    {MIME: bodyMIME, Inline: bodyValue},
			"headers": {MIME: "application/json", Inline: headers},
		},
	}
}

// webhookKey reads the caller's key from either place a sender can put one:
// the Authorization header, or a `key` query parameter.
//
// The query parameter is not a convenience. A large share of the services that
// post webhooks give you one field — the URL — and no way to set a header at
// all, so a header-only endpoint is one those senders simply cannot call. With
// the key in the URL the endpoint is still authenticated: the address itself
// is the credential, which is why the flow's own address is never enough on
// its own.
//
// The header wins when both are present, so a sender that can set one is never
// downgraded by a stale URL someone left lying around.
func webhookKey(r *http.Request) string {
	if h := stripBearer(r.Header.Get("Authorization")); h != "" {
		return h
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

// stripBearer takes the token out of an Authorization header value, tolerating
// a bare token without the "Bearer " prefix.
func stripBearer(h string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return strings.TrimSpace(h)
}

// ServeWebhookForTest exposes the listener's per-request handler without
// binding a real port — used by tests that want to assert HTTP-level
// behaviour via httptest. Production code should use Serve.
func ServeWebhookForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleTrigger(rw, r)
}

// ServeFormForTest is the hosted-form counterpart to
// ServeWebhookForTest — dispatches a request to the /form handler
// without binding a real port.
func ServeFormForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleForm(rw, r)
}

// ParseFormBodyForTest exposes the hosted form's body decoder to the external
// _test package, so the per-encoding behaviour (urlencoded, multipart, flat
// JSON, and the 415 refusal for anything else) is unit-testable without
// standing up a graph run.
func ParseFormBodyForTest(r *http.Request) (url.Values, error) {
	return parseFormBody(r)
}

// HoneypotFieldNameForTest exposes the hidden anti-bot field's name so tests
// can assert it is rendered and that filling it drops the submission.
func HoneypotFieldNameForTest() string { return honeypotName }

// CollectFormValuesForTest exposes the field-collection helper to the
// external _test package so the "extra fields aren't silently dropped"
// guarantee is unit-testable without standing up an HTTP server +
// graph run round trip.
func CollectFormValuesForTest(declared []string, posted url.Values) map[string]any {
	return collectFormValues(declared, posted)
}

// BuildFormSeedForTest exposes the hosted form's seed builder so the
// column order a submission carries downstream is unit-testable without
// standing up an HTTP server + graph run round trip.
func BuildFormSeedForTest(declared []string, values map[string]any) core.Result {
	return buildFormSeed(declared, values)
}

// BuildWebhookSeedForTest exposes the Content-Type-driven body parser
// to the external _test package so the per-encoding decoding (JSON,
// form-urlencoded, text, raw) is unit-testable without a graph run.
func BuildWebhookSeedForTest(rawBody []byte, r *http.Request) core.Result {
	return buildWebhookSeed(rawBody, r)
}

// inboundTriggerDepth reads the trigger-chain depth a run's own HTTP step
// stamped on this delivery. Absent, negative or unparseable reads as 0 —
// the header is only ever set by this instance for requests to itself, so
// anything else is an ordinary delivery.
func inboundTriggerDepth(r *http.Request) int {
	n, err := strconv.Atoi(r.Header.Get(core.TriggerDepthHeader))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
