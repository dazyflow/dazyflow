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

// Per-tenant: the signing secret is the org's, not the deployment's.
const githubTriggerSecretName = "GITHUB_WEBHOOK_SECRET"

const maxGitHubBodyBytes = 1 * 1024 * 1024

// Verifies the signature BEFORE anything else is trusted.
type GitHubEventsHandler struct {
	svc           *Service
	webhookSecret string
	logger        *log.Logger
	// Tests use it to await an asynchronous dispatch.
	fanoutDone func()
}

func NewGitHubEventsHandler(svc *Service, webhookSecret string) *GitHubEventsHandler {
	return &GitHubEventsHandler{
		svc:           svc,
		webhookSecret: webhookSecret,
		logger:        log.New(log.Writer(), "github-events: ", log.LstdFlags),
	}
}

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

	// Bound to the URL's tenant: resolving it any other way would let one org's
	// signature authenticate a delivery aimed at another.
	secret := h.tenantSecret(r.Context(), tenant)
	if secret == "" {
		secret = h.webhookSecret
	}
	if secret == "" {
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
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("pong"))
		return
	case "push":
		h.dispatchPush(tenant, body, rw)
	case "pull_request":
		h.dispatchPullRequest(tenant, body, rw)
	default:
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte("ok"))
	}
}

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

// sha256=HMAC over the raw body, compared in constant time.
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
