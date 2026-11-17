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

	runID, ok := w.submitInbound(rw, r, g, seeds, webhookInbound)
	if !ok {
		return
	}
	w.logger.Printf("fired %s → %s (%d webhook_input seed(s))", flowRef(g), runID, len(seeds))
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(rw).Encode(map[string]string{"job_id": runID})
}

// splitFlowPath parses <prefix><tenant>/<workspace>/<graph-id>, answering 400
// itself when the path doesn't have that shape.
func splitFlowPath(rw http.ResponseWriter, r *http.Request, prefix string) (tenant, workspace, graphID string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected "+prefix+"<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// authPublishedFlow resolves the PUBLISHED revision (never a draft) named by
// the path and checks the caller's key against it. ok is false once it has
// answered the caller itself.
//
// Every pre-authentication failure returns the SAME generic 401, so a prober
// cannot tell a missing flow from a wrong key. Only after the key verifies is
// anything about the flow (such as it being disabled) revealed.
func (w *WebhookListener) authPublishedFlow(
	rw http.ResponseWriter,
	r *http.Request,
	prefix string,
	secrets func(core.Graph) []string,
	public func(core.Graph) bool,
) (core.Graph, bool) {
	tenant, workspace, graphID, ok := splitFlowPath(rw, r, prefix)
	if !ok {
		return core.Graph{}, false
	}
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
	if g.Disabled {
		http.Error(rw, `{"error":{"code":"flow_disabled","message":"flow is currently disabled — re-enable via enable_flow"}}`, http.StatusForbidden)
		return core.Graph{}, false
	}
	return g, true
}

// inputSeeds seeds every enabled node of the given trigger module. inputs
// counts all such nodes, enabled or not, so a caller can tell "no trigger step"
// from "trigger step turned off".
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

// inboundKind is the identity and wording one inbound endpoint uses when it
// submits a run.
type inboundKind struct {
	principal  string // system principal name
	loopMsg    string // trigger_loop refusal
	refusedMsg string // owner-side refusal that could not be kept
	keptMsg    string // owner-side refusal that was kept for the owner
}

var (
	webhookInbound = inboundKind{
		principal:  "dazyflow-webhook",
		loopMsg:    "this delivery came from a run this flow started — the chain is too deep, so a flow is triggering itself",
		refusedMsg: "this flow is not accepting deliveries right now — the owner has been told; please retry",
		keptMsg:    "this flow is not accepting deliveries right now; the delivery has been kept and the owner has been told — do not resend",
	}
	requestInbound = inboundKind{
		principal:  "dazyflow-request",
		loopMsg:    "this call came from a run this flow started — the chain is too deep, so a flow is calling itself",
		refusedMsg: "this flow is not accepting calls right now — the owner has been told; please retry",
		keptMsg:    "this flow is not accepting calls right now; the call has been kept and the owner has been told — do not resend",
	}
)

// submitInbound starts a run for an inbound endpoint under a system principal
// scoped to the graph's own tenant, never the caller's. On failure it answers
// the caller itself and returns ok=false: 429 for a trigger loop, 402/403/503
// for a refusal only the owner can fix (recorded so the owner is told), and
// 500 for anything else.
func (w *WebhookListener) submitInbound(
	rw http.ResponseWriter,
	r *http.Request,
	g core.Graph,
	seeds map[string]core.Result,
	kind inboundKind,
) (string, bool) {
	principal := SystemPrincipal(kind.principal, g.Tenant, g.Workspace)
	// Detached from the request: a caller that hangs up must not cancel the
	// run, nor leave the submit half-written.
	runID, err := w.svc.SubmitGraphOpts(context.WithoutCancel(r.Context()), principal, g, SubmitOpts{
		Seeds:        seeds,
		TriggerDepth: inboundTriggerDepth(r),
	})
	if err == nil {
		return runID, true
	}
	if errors.Is(err, core.ErrTriggerLoop) {
		w.logger.Printf("refused %s: %v", flowRef(g), err)
		writeJSON(rw, http.StatusTooManyRequests, map[string]any{
			"error": map[string]string{"code": "trigger_loop", "message": kind.loopMsg},
		})
		return "", false
	}
	// Distinguishes what the owner must fix from what the caller got wrong.
	if ownerMustFix(err) {
		w.logger.Printf("refused %s: %v", flowRef(g), err)
		refusedRunID, stored := w.recordRefusal(r, g, seeds, err)
		status, msg := http.StatusServiceUnavailable, kind.refusedMsg
		if stored {
			msg = kind.keptMsg
			status = http.StatusPaymentRequired
			if !errors.Is(err, core.ErrPlanLimit) {
				status = http.StatusForbidden
			}
		}
		writeJSON(rw, status, map[string]any{
			"error": map[string]string{"code": refusalCode(err), "message": msg},
			"run":   refusedRunID,
		})
		return "", false
	}
	w.logger.Printf("submit %s: %v", flowRef(g), err)
	http.Error(rw, fmt.Sprintf("submit: %v", err), http.StatusInternalServerError)
	return "", false
}

// recordRefusal keeps an owner-side refusal so the owner is told about it,
// even if the caller has already hung up.
func (w *WebhookListener) recordRefusal(r *http.Request, g core.Graph, seeds map[string]core.Result, err error) (string, bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	return w.svc.recordRefusedDelivery(ctx, g, seeds, refusalCode(err), refusalMessage(err))
}

func flowRef(g core.Graph) string {
	return g.Tenant + "/" + g.Workspace + "/" + g.ID
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
