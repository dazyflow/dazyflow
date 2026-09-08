// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

const slackOnMentionModuleID = "slack_on_mention"

// Per-tenant: the signing secret is the org's, not the deployment's.
const slackTriggerSecretName = "SLACK_SIGNING_SECRET"

// Capped: this endpoint is unauthenticated until the signature verifies.
const maxSlackBodyBytes = 256 * 1024 // 256 KiB

// Bounds replay: a captured request is only usable inside this window.
const slackSignatureMaxSkew = 5 * time.Minute

// Verifies the signature BEFORE anything else is trusted.
type SlackEventsHandler struct {
	svc           *Service
	signingSecret string
	logger        *log.Logger

	now func() time.Time
}

func NewSlackEventsHandler(svc *Service, signingSecret string) *SlackEventsHandler {
	return &SlackEventsHandler{
		svc:           svc,
		signingSecret: signingSecret,
		logger:        log.New(log.Writer(), "slack-events: ", log.LstdFlags),
		now:           time.Now,
	}
}

func (h *SlackEventsHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tenant := r.PathValue("tenant")
	if tenant == "" {
		http.Error(rw, "expected /api/v1/events/slack/<tenant>", http.StatusBadRequest)
		return
	}

	// The signature covers the raw body, so it must be read first, unparsed.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSlackBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		http.Error(rw, "read body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > maxSlackBodyBytes {
		http.Error(rw, fmt.Sprintf("body exceeds %d bytes", maxSlackBodyBytes), http.StatusRequestEntityTooLarge)
		return
	}

	// Bound to the URL's tenant: resolving it any other way would let one org's
	// signature authenticate a delivery aimed at another.
	secret := h.tenantSecret(r.Context(), tenant)
	if secret == "" {
		secret = h.signingSecret
	}
	if secret == "" {
		h.logger.Printf("reject %s: no signing secret configured", tenant)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}

	if err := h.verifySignature(r.Header, body, secret); err != nil {
		// Generic: a prober must not learn which check failed.
		h.logger.Printf("reject %s: %v", tenant, err)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}

	var env slackEventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(rw, fmt.Sprintf("parse: %v", err), http.StatusBadRequest)
		return
	}

	switch env.Type {
	case "url_verification":
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = rw.Write([]byte(env.Challenge))
		return
	case "event_callback":
		h.dispatchEvent(r.Context(), tenant, env, rw)
		return
	default:
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	}
}

type slackEventEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge,omitempty"`
	TeamID    string          `json:"team_id,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
}

type slackAppMentionEvent struct {
	Type    string `json:"type"`
	User    string `json:"user"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

func (h *SlackEventsHandler) dispatchEvent(_ context.Context, tenant string, env slackEventEnvelope, rw http.ResponseWriter) {
	var ev slackAppMentionEvent
	if err := json.Unmarshal(env.Event, &ev); err != nil {
		http.Error(rw, fmt.Sprintf("parse event: %v", err), http.StatusBadRequest)
		return
	}
	if ev.Type != "app_mention" {
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
		return
	}

	var rawEvent any
	_ = json.Unmarshal(env.Event, &rawEvent) // best-effort; signature was already validated
	seed := core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text":    {MIME: "text/plain", Inline: ev.Text},
			"user":    {MIME: "text/plain", Inline: ev.User},
			"channel": {MIME: "text/plain", Inline: ev.Channel},
			"team":    {MIME: "text/plain", Inline: env.TeamID},
			"ts":      {MIME: "text/plain", Inline: ev.TS},
			"event":   {MIME: "application/json", Inline: rawEvent},
		},
	}

	// Slack retries after 3s, so the run must be dispatched asynchronously.
	go h.fanoutSeed(context.Background(), tenant, ev.Channel, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

func (h *SlackEventsHandler) fanoutSeed(ctx context.Context, tenant, eventChannel string, seed core.Result) {
	// A system principal scoped to the URL's tenant, never a caller-supplied one.
	fanoutSeed(ctx, h.svc, h.logger, "dazyflow-slack-events", tenant, slackOnMentionModuleID, seed,
		func(n core.Node) bool {
			return n.Module == slackOnMentionModuleID &&
				nodeChannelFilterMatches(n.Params, eventChannel)
		})
}

func nodeChannelFilterMatches(params map[string]any, eventChannel string) bool {
	if params == nil {
		return true
	}
	raw, ok := params["channel_filter"]
	if !ok {
		return true
	}
	f, ok := raw.(string)
	if !ok || f == "" {
		return true
	}
	return f == eventChannel
}

func (h *SlackEventsHandler) tenantSecret(ctx context.Context, tenant string) string {
	if h.svc == nil || h.svc.EncryptedSecrets == nil {
		return ""
	}
	secret, err := h.svc.EncryptedSecrets.GetExact(ctx, tenant, slackTriggerSecretName)
	if err != nil {
		return ""
	}
	return secret
}

// v0=HMAC-SHA256 over "v0:timestamp:body", compared in constant time over the
// FULL header value so the version prefix cannot be substituted.
func (h *SlackEventsHandler) verifySignature(header http.Header, body []byte, secret string) error {
	tsStr := header.Get("X-Slack-Request-Timestamp")
	sig := header.Get("X-Slack-Signature")
	if tsStr == "" || sig == "" {
		return fmt.Errorf("missing signature headers")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp: %w", err)
	}
	if skew := h.now().Unix() - ts; skew > int64(slackSignatureMaxSkew.Seconds()) || skew < -int64(slackSignatureMaxSkew.Seconds()) {
		return fmt.Errorf("timestamp skew %ds outside replay window", skew)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:"))
	mac.Write([]byte(tsStr))
	mac.Write([]byte(":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	// Constant-time, over the full header including the version prefix.
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(sig))) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
