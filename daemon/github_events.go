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
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

const (
	githubOnPushModuleID  = "github_on_push"
	githubOnNewPRModuleID = "github_on_new_pr"
)

// githubTriggerSecretName is the tenant secret holding the webhook's
// signing secret (the "Secret" field in the repo's webhook settings).
// Organization scope (bare name), same convention as Stripe's
// STRIPE_WEBHOOK_SECRET — resolved per request and bound to the URL
// tenant so one tenant's secret can't validate another's deliveries.
const githubTriggerSecretName = "GITHUB_WEBHOOK_SECRET"

// maxGitHubBodyBytes caps incoming webhook payloads. GitHub push
// events on large monorepos can be a few hundred KB; 1 MiB leaves
// headroom without letting a malformed sender exhaust memory.
const maxGitHubBodyBytes = 1 * 1024 * 1024

// GitHubEventsHandler verifies GitHub webhook signatures and
// dispatches push / pull_request events to subscribed graphs.
//
// Routing layout:
//
//	POST /api/v1/events/github/{tenant}
//	X-Hub-Signature-256: sha256=<hmac-sha256-hex>
//	X-GitHub-Event: <event-type>
//	X-GitHub-Delivery: <uuid>
//	(JSON body)
//
// Auth: the signature header is the only auth. GitHub's HMAC scheme
// is `sha256=<hex(hmac-sha256(secret, body))>` — see
// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries.
//
// Event types handled (per `X-GitHub-Event`):
//
//   - ping            → 200 OK ack so GitHub's "test delivery" works
//   - push            → fans out to graphs with github_on_push nodes
//   - pull_request    → fans out to github_on_new_pr nodes when
//     action == "opened" (other actions ack silently)
//
// Unknown events ack with 200 so GitHub stops retrying — graphs that
// subscribed get nothing, which is the correct outcome.
type GitHubEventsHandler struct {
	svc           *Service
	webhookSecret string
	logger        *log.Logger
	// fanoutDone, when set, is called once a dispatched fanout has finished.
	// Nil in production. Same contract as the Stripe handler's: the endpoint
	// answers GitHub before the fanout completes, so a test has nothing to
	// synchronise on and otherwise has to race the clock. See
	// StripeEventsHandler.fanoutDone for why that is a coin flip under -race.
	fanoutDone func()
}

// NewGitHubEventsHandler wires a handler against the daemon Service.
// webhookSecret is the Secret value the user enters in the repo's
// webhook settings. Empty means the endpoint returns 501 on every
// POST — keeps misconfiguration explicit instead of silently
// rejecting signatures.
func NewGitHubEventsHandler(svc *Service, webhookSecret string) *GitHubEventsHandler {
	return &GitHubEventsHandler{
		svc:           svc,
		webhookSecret: webhookSecret,
		logger:        log.New(log.Writer(), "github-events: ", log.LstdFlags),
	}
}

// ServeHTTP routes a single GitHub webhook POST. Mounted at
// `/api/v1/events/github/{tenant}` by HTTPGateway.
func (h *GitHubEventsHandler) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tenant := r.PathValue("tenant")
	if tenant == "" {
		http.Error(rw, "expected /api/v1/events/github/<tenant>", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxGitHubBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		http.Error(rw, "read body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > maxGitHubBodyBytes {
		http.Error(rw, fmt.Sprintf("body exceeds %d bytes", maxGitHubBodyBytes), http.StatusRequestEntityTooLarge)
		return
	}

	// Resolve the signing secret BOUND to the URL tenant. Policy
	// (per-tenant preferred, global fallback):
	//
	//   - If this tenant configured its own GITHUB_WEBHOOK_SECRET, verify
	//     against that ONLY. A tenant that set its own secret is thereby
	//     protected from the shared-secret cross-tenant injection — an
	//     attacker who knows the global secret can't forge a delivery to
	//     a tenant whose secret it doesn't know.
	//   - Otherwise fall back to the global env-configured secret. This
	//     preserves single-tenant deploys that use DAZYFLOW_GITHUB_WEBHOOK_SECRET
	//     and haven't moved their secret into the per-tenant store.
	//   - If neither exists, reject (fail closed).
	secret := h.tenantSecret(r.Context(), tenant)
	if secret == "" {
		secret = h.webhookSecret
	}
	if secret == "" {
		// Same 401 + body as a bad signature: an unauthenticated caller
		// probing tenant names learns nothing about which tenants exist
		// or have GitHub configured.
		h.logger.Printf("reject %s: no webhook secret configured", tenant)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}

	if err := verifyGitHubSignature(r.Header, body, secret); err != nil {
		h.logger.Printf("reject %s: %v", tenant, err)
		http.Error(rw, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	switch event {
	case "ping":
		// GitHub sends a ping on first webhook setup so the user can
		// confirm the endpoint is reachable. Just ack.
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("pong"))
		return
	case "push":
		h.dispatchPush(tenant, body, rw)
	case "pull_request":
		h.dispatchPullRequest(tenant, body, rw)
	default:
		// Unsubscribed event types — ack so GitHub doesn't retry,
		// but nothing to dispatch.
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	}
}

// pushEvent is the decoded shape of a GitHub push event. We extract
// the named ports up front; the raw event also goes through to the
// `event` output for graphs that need fields we didn't pull out.
type pushEvent struct {
	Ref        string            `json:"ref"`
	Before     string            `json:"before"`
	After      string            `json:"after"`
	Commits    []json.RawMessage `json:"commits"`
	Repository json.RawMessage   `json:"repository"`
	Pusher     json.RawMessage   `json:"pusher"`
}

func (h *GitHubEventsHandler) dispatchPush(tenant string, body []byte, rw http.ResponseWriter) {
	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(rw, fmt.Sprintf("parse push: %v", err), http.StatusBadRequest)
		return
	}
	var commits, repo, pusher, raw any
	_ = json.Unmarshal(ev.Repository, &repo)
	_ = json.Unmarshal(ev.Pusher, &pusher)
	if len(ev.Commits) > 0 {
		commitsList := make([]any, 0, len(ev.Commits))
		for _, c := range ev.Commits {
			var v any
			_ = json.Unmarshal(c, &v)
			commitsList = append(commitsList, v)
		}
		commits = commitsList
	}
	_ = json.Unmarshal(body, &raw)

	seed := core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"ref":        {MIME: "text/plain", Inline: ev.Ref},
			"before":     {MIME: "text/plain", Inline: ev.Before},
			"after":      {MIME: "text/plain", Inline: ev.After},
			"commits":    {MIME: "application/json", Inline: commits},
			"repository": {MIME: "application/json", Inline: repo},
			"pusher":     {MIME: "application/json", Inline: pusher},
			"event":      {MIME: "application/json", Inline: raw},
		},
	}
	go h.runFanout(context.Background(), tenant, githubOnPushModuleID, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

// pullRequestEvent. We only fan out the "opened" action to the
// github_on_new_pr trigger — the trigger drop's name says "new PR",
// so reopens / synchronizes / closes don't fire it. Future drops
// like github_on_pr_merged can subscribe to different actions
// against the same handler.
type pullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number  int             `json:"number"`
		Title   string          `json:"title"`
		Body    string          `json:"body"`
		HTMLURL string          `json:"html_url"`
		User    json.RawMessage `json:"user"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_request"`
	Repository json.RawMessage `json:"repository"`
}

func (h *GitHubEventsHandler) dispatchPullRequest(tenant string, body []byte, rw http.ResponseWriter) {
	var ev pullRequestEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(rw, fmt.Sprintf("parse pull_request: %v", err), http.StatusBadRequest)
		return
	}
	if ev.Action != "opened" {
		// Non-opened actions ack without dispatch — github_on_new_pr
		// specifically subscribes to the "this PR is new" moment.
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
		return
	}

	var author map[string]any
	_ = json.Unmarshal(ev.PullRequest.User, &author)
	authorLogin := ""
	if author != nil {
		if l, ok := author["login"].(string); ok {
			authorLogin = l
		}
	}
	var repo, raw any
	_ = json.Unmarshal(ev.Repository, &repo)
	_ = json.Unmarshal(body, &raw)

	seed := core.Result{
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"number":     {MIME: "text/plain", Inline: fmt.Sprintf("%d", ev.PullRequest.Number)},
			"title":      {MIME: "text/plain", Inline: ev.PullRequest.Title},
			"body":       {MIME: "text/plain", Inline: ev.PullRequest.Body},
			"author":     {MIME: "text/plain", Inline: authorLogin},
			"head_ref":   {MIME: "text/plain", Inline: ev.PullRequest.Head.Ref},
			"base_ref":   {MIME: "text/plain", Inline: ev.PullRequest.Base.Ref},
			"html_url":   {MIME: "text/plain", Inline: ev.PullRequest.HTMLURL},
			"repository": {MIME: "application/json", Inline: repo},
			"event":      {MIME: "application/json", Inline: raw},
		},
	}
	go h.runFanout(context.Background(), tenant, githubOnNewPRModuleID, seed)

	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte("ok"))
}

// fanoutSeed walks every workspace under the tenant, loads each
// graph, and submits a run for any that declares a node with the
// matching trigger module. Mirrors slack_events.fanoutSeed.
// runFanout is fanoutSeed plus the completion signal. Every dispatch goes
// through it so no call site can forget to fire the hook.
func (h *GitHubEventsHandler) runFanout(ctx context.Context, tenant, moduleID string, seed core.Result) {
	defer func() {
		if h.fanoutDone != nil {
			h.fanoutDone()
		}
	}()
	h.fanoutSeed(ctx, tenant, moduleID, seed)
}

func (h *GitHubEventsHandler) fanoutSeed(ctx context.Context, tenant, moduleID string, seed core.Result) {
	fanoutSeed(ctx, h.svc, h.logger, "dazyflow-github-events", tenant, moduleID, seed,
		func(n core.Node) bool { return n.Module == moduleID })
}

// tenantSecret reads this tenant's own GITHUB_WEBHOOK_SECRET from the
// encrypted secret store, bound to the URL tenant. Empty (no store, not
// configured, or any lookup error) means "no per-tenant secret" — the
// caller then falls back to the global env secret.
func (h *GitHubEventsHandler) tenantSecret(ctx context.Context, tenant string) string {
	if h.svc == nil || h.svc.EncryptedSecrets == nil {
		return ""
	}
	secret, err := h.svc.EncryptedSecrets.GetExact(ctx, tenant, githubTriggerSecretName)
	if err != nil {
		return ""
	}
	return secret
}

// verifyGitHubSignature implements GitHub's webhook signing scheme:
//
//	sig = "sha256=" + hex(hmac-sha256(secret, body))
//	header X-Hub-Signature-256 must equal sig (constant-time)
//
// The secret is resolved per request (per-tenant preferred, global
// fallback) and passed in, so the signature is verified against the
// secret bound to the URL tenant.
//
// Unlike Slack's scheme there's no timestamp in the signature, so
// no replay window — GitHub relies on TLS + per-delivery UUIDs
// (X-GitHub-Delivery) for that. The lack of a window means an
// attacker who somehow captures one valid signature could replay
// it; mitigation is keeping the secret confidential and using
// the per-delivery UUID for idempotency at the receiving side
// (out of scope for V1).
func verifyGitHubSignature(header http.Header, body []byte, secret string) error {
	sig := header.Get("X-Hub-Signature-256")
	if sig == "" {
		return fmt.Errorf("missing X-Hub-Signature-256 header")
	}
	if !strings.HasPrefix(sig, "sha256=") {
		return fmt.Errorf("signature header missing sha256= prefix")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(sig))) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
