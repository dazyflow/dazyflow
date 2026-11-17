// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

const webhookInputModuleID = "webhook_input"

// One public endpoint per flow carrying a webhook step. Unauthenticated in the
// session sense: the key in the request IS the credential.
type WebhookListener struct {
	svc    *Service
	logger *log.Logger

	// Backs the Idempotency-Key contract on /call.
	idempotency *idempotencyStore

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
	g, ok := w.authPublishedFlow(rw, r, "/trigger/", core.GraphWebhookSecrets, core.GraphWebhookPublic)
	if !ok {
		return
	}

	// Capped, or an anonymous caller can OOM the daemon with one POST.
	rawBody, ok := w.readCallBody(rw, r)
	if !ok {
		return
	}

	seeds, inputs := inputSeeds(g, webhookInputModuleID, buildWebhookSeed(rawBody, r))
	// Firing anyway would start a run whose trigger step is skipped.
	if inputs > 0 && len(seeds) == 0 {
		http.Error(rw, `{"error":{"code":"trigger_disabled","message":"this flow's webhook step is turned off — re-enable the step to accept deliveries"}}`, http.StatusForbidden)
		return
	}

	runID, _, ok := w.submitInbound(rw, r, g, "dazyflow-webhook", seeds,
		`{"error":{"code":"trigger_loop","message":"this delivery came from a run this flow started — the chain is too deep, so a flow is triggering itself"}}`)
	if !ok {
		return
	}
	w.logger.Printf("fired %s/%s/%s → %s (%d webhook_input seed(s))",
		g.Tenant, g.Workspace, g.ID, runID, len(seeds))
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(map[string]string{"job_id": runID})
}

// flowPath splits <prefix><tenant>/<workspace>/<graph-id>, answering 400 itself
// when the path has any other shape.
func flowPath(rw http.ResponseWriter, r *http.Request, prefix string) (tenant, workspace, graphID string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected "+prefix+"<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// authPublishedFlow resolves the path to a PUBLISHED revision (never a draft)
// and checks the caller's key against it. It answers the caller itself and
// returns ok=false on any failure.
func (w *WebhookListener) authPublishedFlow(
	rw http.ResponseWriter,
	r *http.Request,
	prefix string,
	secrets func(core.Graph) []string,
	public func(core.Graph) bool,
) (core.Graph, bool) {
	tenant, workspace, graphID, ok := flowPath(rw, r, prefix)
	if !ok {
		return core.Graph{}, false
	}

	// Every pre-authentication failure returns the SAME generic 401, so a prober
	// cannot tell a missing flow from a wrong key.
	const unauthorized = "unknown endpoint or invalid secret"

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return core.Graph{}, false
	}
	g, err := store.LoadPublished(graphID)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return core.Graph{}, false
	}

	keys := secrets(g)
	switch {
	case len(keys) > 0:
		if !anyKeyMatches(keys, webhookKey(r)) {
			http.Error(rw, unauthorized, http.StatusUnauthorized)
			return core.Graph{}, false
		}
	case public(g):
		// Marked open by the author: possession of the URL is the capability.
	default:
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return core.Graph{}, false
	}

	// Only AFTER the key verifies is anything about the flow revealed.
	if g.Disabled {
		http.Error(rw, `{"error":{"code":"flow_disabled","message":"flow is currently disabled — re-enable via enable_flow"}}`, http.StatusForbidden)
		return core.Graph{}, false
	}
	return g, true
}

// inputSeeds seeds every enabled step of the given input module with seed.
// inputs counts all such steps, enabled or not, so a caller can tell "the
// flow has no such step" from "every such step is turned off".
func inputSeeds(g core.Graph, module string, seed core.Result) (seeds map[string]core.Result, inputs int) {
	seeds = map[string]core.Result{}
	for _, n := range g.Nodes {
		if n.Module != module {
			continue
		}
		inputs++
		if triggerNodeDisabled(n) {
			continue
		}
		seeds[n.ID] = seed
	}
	return seeds, inputs
}

// submitInbound starts a run for an inbound delivery. On failure it answers
// the caller itself and returns ok=false; stored then reports whether an
// owner-side refusal was kept as a refused run (so the caller must not resend).
func (w *WebhookListener) submitInbound(
	rw http.ResponseWriter,
	r *http.Request,
	g core.Graph,
	principalName string,
	seeds map[string]core.Result,
	loopBody string,
) (runID string, stored bool, ok bool) {
	// A system principal scoped to the graph's own tenant, never the caller's.
	principal := SystemPrincipal(principalName, g.Tenant, g.Workspace)
	// Detached from the request: a sender that hangs up must not cancel the run.
	runID, err := w.svc.SubmitGraphOpts(context.WithoutCancel(r.Context()), principal, g, SubmitOpts{
		Seeds:        seeds,
		TriggerDepth: inboundTriggerDepth(r),
	})
	if err == nil {
		return runID, false, true
	}
	if errors.Is(err, core.ErrTriggerLoop) {
		w.logger.Printf("refused %s/%s/%s: %v", g.Tenant, g.Workspace, g.ID, err)
		http.Error(rw, loopBody, http.StatusTooManyRequests)
		return "", false, false
	}
	// Distinguishes what the owner must fix from what the sender got wrong.
	if ownerMustFix(err) {
		w.logger.Printf("refused %s/%s/%s: %v", g.Tenant, g.Workspace, g.ID, err)
		refusedRunID, stored := w.recordRefusal(r, g, seeds, err)
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
		return "", stored, false
	}
	w.logger.Printf("submit %s/%s/%s: %v", g.Tenant, g.Workspace, g.ID, err)
	http.Error(rw, fmt.Sprintf("submit: %v", err), http.StatusInternalServerError)
	return "", false, false
}

// recordRefusal keeps a delivery the owner's side refused as a refused run,
// so it can be retried once the owner has fixed the cause.
func (w *WebhookListener) recordRefusal(r *http.Request, g core.Graph, seeds map[string]core.Result, err error) (string, bool) {
	capCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	return w.svc.recordRefusedDelivery(capCtx, g, seeds, refusalCode(err), refusalMessage(err))
}

func anyKeyMatches(keys []string, provided string) bool {
	matched := 0
	for _, k := range keys {
		matched |= subtle.ConstantTimeCompare([]byte(k), []byte(provided))
	}
	return matched == 1
}

func buildWebhookSeed(rawBody []byte, r *http.Request) core.Result {
	contentType := r.Header.Get("Content-Type")
	mediaType := contentType
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	// Media types are case-insensitive per RFC 9110 §8.3.1.
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
			bodyValue = string(rawBody)
		}
	case mediaType == "application/x-www-form-urlencoded":
		if parsed, err := url.ParseQuery(string(rawBody)); err == nil {
			bodyValue = collectFormValues(nil, parsed)
		} else {
			bodyValue = string(rawBody)
		}
	case strings.HasPrefix(mediaType, "text/"):
		bodyValue = string(rawBody)
	default:
		bodyValue = rawBody
	}

	headers := make(map[string]any, len(r.Header))
	for k, vs := range r.Header {
		// Credential headers must never reach the headers port, or a flow can echo them.
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

// Either place a sender can put one: the Authorization header, or a `key` query
// parameter for services that can only be given a URL.
func webhookKey(r *http.Request) string {
	if h := stripBearer(r.Header.Get("Authorization")); h != "" {
		return h
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

func stripBearer(h string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return strings.TrimSpace(h)
}

func ServeWebhookForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleTrigger(rw, r)
}

func ServeFormForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleForm(rw, r)
}

func ParseFormBodyForTest(r *http.Request) (url.Values, error) {
	return parseFormBody(r)
}

func HoneypotFieldNameForTest() string { return honeypotName }

func CollectFormValuesForTest(declared []string, posted url.Values) map[string]any {
	return collectFormValues(declared, posted)
}

func BuildFormSeedForTest(declared []string, values map[string]any) core.Result {
	return buildFormSeed(declared, values)
}

func BuildWebhookSeedForTest(rawBody []byte, r *http.Request) core.Result {
	return buildWebhookSeed(rawBody, r)
}

func inboundTriggerDepth(r *http.Request) int {
	n, err := strconv.Atoi(r.Header.Get(core.TriggerDepthHeader))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
