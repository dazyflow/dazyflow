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

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/trigger/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected /trigger/<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return
	}
	tenant, workspace, graphID := parts[0], parts[1], parts[2]

	// Every pre-authentication failure returns the SAME generic 401, so a prober
	// cannot tell a missing flow from a wrong key.
	const unauthorized = "unknown endpoint or invalid secret"

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}
	// The PUBLISHED revision, never a draft.
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
		// Marked open by the author: possession of the URL is the capability.
	default:
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}

	// Only AFTER the key verifies is anything about the flow revealed.
	if g.Disabled {
		http.Error(rw, `{"error":{"code":"flow_disabled","message":"flow is currently disabled — re-enable via enable_flow"}}`, http.StatusForbidden)
		return
	}

	// Capped, or an anonymous caller can OOM the daemon with one POST.
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
	// Firing anyway would start a run whose trigger step is skipped.
	if inputs > 0 && len(seeds) == 0 {
		http.Error(rw, `{"error":{"code":"trigger_disabled","message":"this flow's webhook step is turned off — re-enable the step to accept deliveries"}}`, http.StatusForbidden)
		return
	}

	// A system principal scoped to the graph's own tenant, never the caller's.
	principal := SystemPrincipal("dazyflow-webhook", g.Tenant, g.Workspace)
	// Detached from the request: a sender that hangs up must not cancel the run.
	runID, err := w.svc.SubmitGraphOpts(context.WithoutCancel(r.Context()), principal, g, SubmitOpts{
		Seeds:        seeds,
		TriggerDepth: inboundTriggerDepth(r),
	})
	if errors.Is(err, core.ErrTriggerLoop) {
		w.logger.Printf("refused %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, `{"error":{"code":"trigger_loop","message":"this delivery came from a run this flow started — the chain is too deep, so a flow is triggering itself"}}`,
			http.StatusTooManyRequests)
		return
	}
	// Distinguishes what the owner must fix from what the sender got wrong.
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
