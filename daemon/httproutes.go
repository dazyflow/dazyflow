// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The gateway's route table. This is the authoritative surface — every
// HTTP endpoint the daemon exposes is registered here, and
// route_sweep_test.go scrapes these registrations so a newly-mounted route
// is covered the moment it's added, with no test edit. The handlers
// themselves live in the httpgateway_*/http*.go files this table points at.

import (
	"context"
	"net/http"
	"time"
)

func (h *HTTPGateway) mountRoutes(mux *http.ServeMux) {
	// Liveness: the process is up and serving. Never touches deps.
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		if _, err := rw.Write([]byte("ok")); err != nil {
			h.logger.Printf("healthz: write response: %v", err)
		}
	})
	// Readiness: can the process actually serve requests (deps reachable)?
	// ReadyCheck is nil for the dev/in-memory deployment — then ready ==
	// alive. With Postgres, cmd/dzd wires a pool ping so orchestration
	// holds traffic until the DB is reachable.
	mux.HandleFunc("GET /readyz", func(rw http.ResponseWriter, r *http.Request) {
		if h.ReadyCheck != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := h.ReadyCheck(ctx); err != nil {
				writeJSONError(rw, http.StatusServiceUnavailable, "not ready: "+err.Error())
				return
			}
		}
		rw.WriteHeader(http.StatusOK)
		if _, err := rw.Write([]byte("ready")); err != nil {
			h.logger.Printf("readyz: write response: %v", err)
		}
	})
	// Prometheus scrape endpoint, opt-in (exposes tenant names + usage).
	if h.EnableMetrics {
		mux.HandleFunc("GET /metrics", h.metrics)
	}
	mux.HandleFunc("POST /api/v1/auth/signin", h.rateLimitAuth(h.signIn))
	mux.HandleFunc("POST /api/v1/auth/signup", h.rateLimitAuth(h.signUp))
	mux.HandleFunc("POST /api/v1/auth/verify-email", h.rateLimitAuth(h.verifyEmail))
	// Password reset (forgot → email link → set new password). Both
	// unauthenticated and rate-limited; the token in reset-password is
	// the credential. See password_reset.go.
	mux.HandleFunc("POST /api/v1/auth/forgot-password", h.rateLimitAuth(h.requestPasswordReset))
	mux.HandleFunc("POST /api/v1/auth/reset-password", h.rateLimitAuth(h.resetPassword))
	// Rate-limited like the other auth routes: resend mints+sends a fresh
	// token, so an authenticated client must not be able to hammer it to spam
	// email or churn tokens. IP limiter wraps the auth check.
	mux.HandleFunc("POST /api/v1/me/verification/resend", h.rateLimitAuth(h.requireAuth(h.resendVerification)))
	mux.HandleFunc("POST /api/v1/auth/signout", h.signOut)
	// Leg 2 of sign-in for TOTP-enrolled users. Unauthenticated (the
	// challenge token is the principal) and rate-limited like the rest
	// of the auth surface.
	mux.HandleFunc("POST /api/v1/auth/totp", h.rateLimitAuth(h.totpVerify))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(h.uploadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/list", h.requireAuth(h.listWorkspaceFiles))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/download", h.requireAuth(h.downloadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/usage", h.requireAuth(h.workspaceFileUsage))
	mux.HandleFunc("DELETE /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(h.deleteWorkspaceFile))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files/mkdir", h.requireAuth(h.mkdirWorkspaceDir))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files/rename", h.requireAuth(h.renameWorkspaceFile))
	mux.HandleFunc("GET /api/v1/secrets", h.requireAuth(h.listSecrets))
	mux.HandleFunc("PUT /api/v1/secrets/{name}", h.requireAuth(h.putSecret))
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", h.requireAuth(h.deleteSecret))
	mux.HandleFunc("GET /api/v1/resources", h.requireAuth(h.listResources))
	mux.HandleFunc("PUT /api/v1/resources/{name}", h.requireAuth(h.putResource))
	mux.HandleFunc("DELETE /api/v1/resources/{name}", h.requireAuth(h.deleteResource))
	mux.HandleFunc("GET /api/v1/email-templates", h.requireAuth(h.listEmailTemplates))
	mux.HandleFunc("PUT /api/v1/email-templates/{name}", h.requireAuth(h.putEmailTemplate))
	mux.HandleFunc("DELETE /api/v1/email-templates/{name}", h.requireAuth(h.deleteEmailTemplate))
	mux.HandleFunc("POST /api/v1/email-templates/preview", h.requireAuth(h.previewEmailTemplate))
	mux.HandleFunc("POST /api/v1/email-templates/send-test", h.requireAuth(h.sendTestEmail))

	// Bring-your-own secret manager (OpenBao/Vault): per-tenant connection
	// config behind the same secret-permission gate. Flows then resolve
	// ${vault.PATH#FIELD} against the tenant's own manager.
	mux.HandleFunc("GET /api/v1/secret-manager", h.requireAuth(h.getSecretManager))
	mux.HandleFunc("PUT /api/v1/secret-manager", h.requireAuth(h.putSecretManager))
	mux.HandleFunc("DELETE /api/v1/secret-manager", h.requireAuth(h.deleteSecretManager))
	mux.HandleFunc("GET /api/v1/secret-manager/aws", h.requireAuth(h.getSecretManagerAws))
	mux.HandleFunc("PUT /api/v1/secret-manager/aws", h.requireAuth(h.putSecretManagerAws))
	mux.HandleFunc("DELETE /api/v1/secret-manager/aws", h.requireAuth(h.deleteSecretManagerAws))
	mux.HandleFunc("GET /api/v1/secret-manager/gcp", h.requireAuth(h.getSecretManagerGcp))
	mux.HandleFunc("PUT /api/v1/secret-manager/gcp", h.requireAuth(h.putSecretManagerGcp))
	mux.HandleFunc("DELETE /api/v1/secret-manager/gcp", h.requireAuth(h.deleteSecretManagerGcp))
	mux.HandleFunc("GET /api/v1/oauth/providers", h.requireAuth(h.oauthListProviders))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/accounts", h.requireAuth(h.oauthListAccounts))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/resources", h.requireAuth(h.listAccountResources))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/authorize", h.requireAuth(h.oauthAuthorize))
	// Callback is UNAUTHENTICATED — the OAuth provider redirects the
	// user's browser back here without a Bearer token. State-token
	// validation in the handler is what binds the callback to the
	// original principal.
	mux.HandleFunc("GET /api/v1/oauth/{provider}/callback", h.oauthCallback)
	mux.HandleFunc("GET /api/v1/drops", h.requireAuth(h.listModules))
	// Legacy alias — dzctl and older proxies still hit /modules. Keep
	// it pointing at the same handler so we can deprecate at our pace.
	mux.HandleFunc("GET /api/v1/modules", h.requireAuth(h.listModules))

	// Self-describing catalog surface (see daemon/catalog.go +
	// daemon/openapi.yaml). These are additive — they coexist with
	// /api/v1/drops above and will become the canonical surface once
	// the rename PR lands. Discovery + openapi are public so an LLM
	// client can read the spec before it has a token.
	mux.HandleFunc("GET /api/v1", h.serviceDescriptor)
	mux.HandleFunc("GET /api/v1/openapi.json", h.openAPISpec)
	mux.HandleFunc("GET /api/v1/catalog", h.requireAuth(h.catalogSummary))
	mux.HandleFunc("GET /api/v1/catalog/integrations", h.requireAuth(h.listIntegrationsHandler))
	mux.HandleFunc("GET /api/v1/catalog/integrations/{id}", h.requireAuth(h.getIntegrationHandler))
	// Connection verify-before-save (PUT) + re-test of a stored connection
	// (POST verify). secret:write-gated inside the handlers.
	mux.HandleFunc("PUT /api/v1/catalog/integrations/{id}/connection", h.requireAuth(h.putIntegrationConnection))
	mux.HandleFunc("POST /api/v1/catalog/integrations/{id}/verify", h.requireAuth(h.verifyIntegrationConnection))
	mux.HandleFunc("GET /api/v1/catalog/drops", h.requireAuth(h.listDropsHandler))
	mux.HandleFunc("GET /api/v1/catalog/drops/{id}", h.requireAuth(h.getDropHandler))
	mux.HandleFunc("GET /api/v1/catalog/trigger-kinds", h.requireAuth(h.triggerKindsHandler))

	// /me surface. /me is the new alias for /whoami; /me/api-keys is
	// new (self-issue + own-key list/revoke). Unlike /admin/api-keys
	// these don't require organization:admin — any authenticated principal
	// can derive sub-scopes of their own permissions.
	mux.HandleFunc("GET /api/v1/me", h.requireAuth(h.meHandler))
	// /me/totp — the caller manages their own 2FA. Status is readable
	// by anyone signed in; the mutating routes 503 when the install
	// hasn't configured a TOTP key.
	mux.HandleFunc("GET /api/v1/me/totp", h.requireAuth(h.totpStatus))
	mux.HandleFunc("POST /api/v1/me/totp/setup", h.requireAuth(h.totpSetup))
	mux.HandleFunc("POST /api/v1/me/totp/confirm", h.requireAuth(h.totpConfirm))
	mux.HandleFunc("POST /api/v1/me/totp/disable", h.requireAuth(h.totpDisable))
	mux.HandleFunc("POST /api/v1/me/totp/recovery-codes", h.requireAuth(h.totpRegenerate))
	// /me/preferences — the caller's own operational notification
	// settings (e.g. flow-failure email). Always available when password
	// auth is configured; unknown (API-key) principals read defaults.
	mux.HandleFunc("GET /api/v1/me/preferences", h.requireAuth(h.getPreferences))
	mux.HandleFunc("PUT /api/v1/me/preferences", h.requireAuth(h.putPreferences))
	mux.HandleFunc("GET /api/v1/me/api-keys", h.requireAuth(h.listMyAPIKeysHandler))
	mux.HandleFunc("POST /api/v1/me/api-keys",
		h.requireAuth(h.idempotencyMiddleware("/me/api-keys", h.issueMyAPIKeyHandler)))
	mux.HandleFunc("DELETE /api/v1/me/api-keys/{id}", h.requireAuth(h.revokeMyAPIKeyHandler))

	// GDPR data-subject rights. Export (Art. 15/20) downloads a complete
	// machine-readable copy; account deletion (Art. 17) erases the caller's
	// own account (confirmation-guarded). Handlers in gdpr_export.go /
	// gdpr_http.go.
	mux.HandleFunc("GET /api/v1/me/export", h.requireAuth(h.exportHandler))
	mux.HandleFunc("DELETE /api/v1/me/account", h.requireAuth(h.deleteMyAccountHandler))
	// Support feature: a support agent
	// requests a scoped, read-only grant; an org admin approves/denies/revokes;
	// the agent reads the redacted bundle. All gated + audited into the org's log.
	mux.HandleFunc("POST /api/v1/support/grants", h.requireAuth(h.requestGrant))
	mux.HandleFunc("GET /api/v1/support/grants", h.requireAuth(h.listGrants))
	mux.HandleFunc("GET /api/v1/support/grants/mine", h.requireAuth(h.listMyGrants))
	mux.HandleFunc("POST /api/v1/support/grants/{id}/decide", h.requireAuth(h.decideGrant))
	mux.HandleFunc("POST /api/v1/support/grants/{id}/revoke", h.requireAuth(h.revokeGrant))
	mux.HandleFunc("GET /api/v1/support/flows/{tenant}/{workspace}/{flow_id}", h.requireAuth(h.supportView))
	// Support tickets + chat (Phase 2). End-user surface under /me; the
	// cross-tenant agent queue under /support.
	mux.HandleFunc("POST /api/v1/me/support/tickets", h.requireAuth(h.createTicket))
	mux.HandleFunc("GET /api/v1/me/support/tickets", h.requireAuth(h.listMyTickets))
	mux.HandleFunc("GET /api/v1/me/support/tickets/{id}", h.requireAuth(h.getMyTicket))
	mux.HandleFunc("GET /api/v1/me/support/tickets/{id}/bundle", h.requireAuth(h.getMyTicketBundle))
	mux.HandleFunc("POST /api/v1/me/support/tickets/{id}/messages", h.requireAuth(h.postMyTicketMessage))
	mux.HandleFunc("POST /api/v1/me/support/tickets/{id}/status", h.requireAuth(h.setMyTicketStatus))
	mux.HandleFunc("POST /api/v1/me/support/tickets/{id}/read", h.requireAuth(h.markMyTicketRead))
	mux.HandleFunc("GET /api/v1/support/tickets", h.requireAuth(h.listTicketQueue))
	// /summary before /{id}: a literal segment outranks a wildcard in ServeMux's
	// precedence rules, so the dashboard's counts can't be read as a ticket id.
	mux.HandleFunc("GET /api/v1/support/tickets/summary", h.requireAuth(h.ticketQueueSummary))
	mux.HandleFunc("GET /api/v1/support/tickets/{id}", h.requireAuth(h.getSupportTicket))
	mux.HandleFunc("GET /api/v1/support/tickets/{id}/bundle", h.requireAuth(h.getSupportTicketBundle))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/messages", h.requireAuth(h.postSupportTicketMessage))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/status", h.requireAuth(h.setSupportTicketStatus))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/read", h.requireAuth(h.markSupportTicketRead))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/assign", h.requireAuth(h.assignSupportTicket))
	// Self-service rectification (Art. 16): change own password / email.
	mux.HandleFunc("POST /api/v1/me/password", h.requireAuth(h.changePasswordHandler))
	mux.HandleFunc("POST /api/v1/me/email", h.requireAuth(h.changeEmailHandler))

	// /me/flows and /me/runs — the new spec-aligned routes. flow_id is
	// a percent-encoded composite of tenant/workspace/id; run_id is
	// the existing jobID verbatim. Handlers in daemon/me_routes.go
	// translate to the legacy graph + job service methods. Mutating
	// routes honor Idempotency-Key for replay-safe retries.
	mux.HandleFunc("GET /api/v1/me/usage", h.requireAuth(h.usageMe))
	mux.HandleFunc("GET /api/v1/me/billing", h.requireAuth(h.billingMe))
	mux.HandleFunc("GET /api/v1/me/plans", h.requireAuth(h.plansMe))
	mux.HandleFunc("POST /api/v1/me/billing/checkout", h.requireAuth(h.billingCheckout))
	mux.HandleFunc("POST /api/v1/me/billing/portal", h.requireAuth(h.billingPortal))
	mux.HandleFunc("GET /api/v1/me/flows", h.requireAuth(h.listFlowsMe))
	// Literal "suggestions" segment outranks the {flow_id} wildcard below
	// in Go's ServeMux precedence, so this need not be ordered carefully.
	mux.HandleFunc("GET /api/v1/me/flows/suggestions", h.requireAuth(h.suggestionsMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}", h.requireAuth(h.loadFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/history", h.requireAuth(h.historyFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/published", h.requireAuth(h.publishedFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/publish",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/publish", h.publishFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/unpublish",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/unpublish", h.unpublishFlowMe)))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/watch", h.requireAuth(h.watchFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/references", h.requireAuth(h.listReferences))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/input-fields", h.requireAuth(h.listInputFields))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/restore",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/restore", h.restoreFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/duplicate",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/duplicate", h.duplicateFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/label",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/label", h.labelRevisionMe)))
	mux.HandleFunc("PUT /api/v1/me/flows/{flow_id}",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}", h.saveFlowMe)))
	mux.HandleFunc("PATCH /api/v1/me/flows/{flow_id}",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}", h.patchFlowMe)))
	mux.HandleFunc("DELETE /api/v1/me/flows/{flow_id}", h.requireAuth(h.deleteFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/enable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/enable", h.enableFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/disable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/disable", h.disableFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/run",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/run", h.runFlowMe)))
	// Clear one node's persisted per-node state (dedupe cursor / watermark /
	// cache) — the editor's "Reset state" action. See resetNodeStateMe.
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/nodes/{node_id}/reset-state",
		h.requireAuth(h.resetNodeStateMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/validate", h.requireAuth(h.validateFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/test-trigger",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/test-trigger", h.testTriggerFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/nodes/{node_id}/sample",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/nodes/{node_id}/sample", h.sampleFlowNodeMe)))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/runs", h.requireAuth(h.listFlowRunsMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/triggers/{node_id}/enable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/triggers/{node_id}/enable", h.enableTriggerMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/triggers/{node_id}/disable",
		h.requireAuth(h.idempotencyMiddleware("/me/flows/{flow_id}/triggers/{node_id}/disable", h.disableTriggerMe)))

	mux.HandleFunc("GET /api/v1/me/schedules", h.requireAuth(h.listSchedulesMe))

	// /me/boards — the Results surface. A "board" is a user table in the
	// workspace's Collections store; these read it back (read-only) and clear
	// it. Scoped to (tenant, workspace) in the handler, mirroring /me/flows.
	mux.HandleFunc("GET /api/v1/me/boards", h.requireAuth(h.listBoardsMe))
	mux.HandleFunc("GET /api/v1/me/boards/{name}", h.requireAuth(h.getBoardMe))
	mux.HandleFunc("DELETE /api/v1/me/boards/{name}", h.requireAuth(h.clearBoardMe))
	// Delete a single row by its rowid (the handle getBoardMe returns per row).
	mux.HandleFunc("DELETE /api/v1/me/boards/{name}/rows/{rowid}", h.requireAuth(h.deleteBoardRowMe))

	// Git credentials: named, per-org auth bundles (SSH key and/or HTTPS
	// PAT) a git_checkout node picks by `account` — the OAuth-account
	// pattern, for raw git credentials.
	mux.HandleFunc("GET /api/v1/git/credentials", h.requireAuth(h.listGitCredsMe))
	mux.HandleFunc("PUT /api/v1/git/credentials/{account}", h.requireAuth(h.putGitCredMe))
	mux.HandleFunc("DELETE /api/v1/git/credentials/{account}", h.requireAuth(h.deleteGitCredMe))

	// /git/mirror — push this workspace's flow repository to a git remote
	// the customer owns (SSH only; the key comes from one of the
	// credentials above). /push is the synchronous "push now / test this
	// remote" action.
	mux.HandleFunc("GET /api/v1/git/mirror", h.requireAuth(h.getGitMirrorMe))
	mux.HandleFunc("PUT /api/v1/git/mirror", h.requireAuth(h.putGitMirrorMe))
	mux.HandleFunc("DELETE /api/v1/git/mirror", h.requireAuth(h.deleteGitMirrorMe))
	mux.HandleFunc("POST /api/v1/git/mirror/push", h.requireAuth(h.pushGitMirrorMe))

	mux.HandleFunc("GET /api/v1/me/runs", h.requireAuth(h.listRunsMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}", h.requireAuth(h.getRunMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/nodes", h.requireAuth(h.listRunNodesMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/logs", h.requireAuth(h.listRunLogsMe))
	mux.HandleFunc("DELETE /api/v1/me/runs/{run_id}/logs", h.requireAuth(h.deleteRunLogsMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/nodes/{node_id}", h.requireAuth(h.getRunNodeMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/events", h.requireAuth(h.runEventsMe))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/cancel",
		h.requireAuth(h.idempotencyMiddleware("/me/runs/{run_id}/cancel", h.cancelRunMe)))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/resume", h.requireAuth(h.resumeRunMe))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/retry",
		h.requireAuth(h.idempotencyMiddleware("/me/runs/{run_id}/retry", h.retryRunMe)))

	// /me/share — the per-workspace public overview (TV-dashboard) link.
	// GET reads the current link, POST mints/rotates it, DELETE revokes it.
	// The public read of the snapshot is the unauthenticated route below.
	mux.HandleFunc("GET /api/v1/me/share", h.requireAuth(h.getShareMe))
	mux.HandleFunc("POST /api/v1/me/share", h.requireAuth(h.createShareMe))
	mux.HandleFunc("DELETE /api/v1/me/share", h.requireAuth(h.deleteShareMe))

	// /me/connections — LLM-friendly OAuth surface. List returns
	// provider catalog + which accounts the caller has linked. Authorize
	// returns JSON {authorize_url} (no 302) so an MCP/CLI client can
	// hand the URL to the user. Callback path stays the same.
	mux.HandleFunc("GET /api/v1/me/connections", h.requireAuth(h.listConnectionsMe))
	mux.HandleFunc("POST /api/v1/me/connections/{provider}/authorize",
		h.requireAuth(h.idempotencyMiddleware("/me/connections/{provider}/authorize", h.startConnectionMe)))
	mux.HandleFunc("DELETE /api/v1/me/connections/{provider}", h.requireAuth(h.disconnectConnectionMe))
	mux.HandleFunc("POST /api/v1/validate/cron", h.requireAuth(h.validateCron))
	// Live preview for the render_template step's editor — renders {template,
	// data} through the same engine the drop uses. See render_preview.go.
	mux.HandleFunc("POST /api/v1/tools/render-template/preview", h.requireAuth(h.renderTemplatePreview))
	// Live preview for the render_text step's editor — renders sample rows
	// through the same engine the drop uses. See render_text_preview.go.
	mux.HandleFunc("POST /api/v1/tools/render-text/preview", h.requireAuth(h.renderTextPreview))
	// The Expression drop's formula linter — compiles a CEL expression and
	// returns any problem inline. See validateExpression.
	mux.HandleFunc("POST /api/v1/tools/expression/validate", h.requireAuth(h.validateExpression))
	// AI assist: plain-English description → HTML email template, using the
	// tenant's connected Claude/ChatGPT key. See render_assist.go.
	mux.HandleFunc("POST /api/v1/tools/render-template/assist", h.requireAuth(h.renderTemplateAssist))
	mux.HandleFunc("GET /api/v1/tools/llm-providers", h.requireAuth(h.renderTemplateLLMProviders))
	// Flagship: describe a flow in plain English → a DRAFT flow graph
	// (grounded on the catalog, structured output, validate-and-repair).
	// Never saves or runs — the editor opens it for review. See flowgen.go.
	mux.HandleFunc("POST /api/v1/tools/flow/generate", h.requireAuth(h.renderFlowGenerate))
	mux.HandleFunc("POST /api/v1/tools/flow/generate/stream", h.requireAuth(h.renderFlowGenerateStream))
	// validate/graph lints a Graph JSON literal without saving — for
	// LLMs that compose a graph in chat and want a dry-run before
	// committing. Distinct from /me/flows/{id}/validate which lints
	// the saved HEAD.
	mux.HandleFunc("POST /api/v1/validate/graph", h.requireAuth(h.validateGraphLiteral))
	// Slack Events API endpoint. NOT under requireAuth — Slack POSTs
	// as a stranger; the HMAC signature is the auth.
	mux.HandleFunc("POST /api/v1/events/slack/{tenant}", h.rateLimitWebhook(h.slackEvents))
	mux.HandleFunc("POST /api/v1/events/github/{tenant}", h.rateLimitWebhook(h.githubEvents))
	mux.HandleFunc("POST /api/v1/events/stripe", h.rateLimitWebhook(h.stripeEvents))
	mux.HandleFunc("POST /api/v1/events/stripe/{tenant}", h.rateLimitWebhook(h.stripeTenantEvents))
	mux.HandleFunc("GET /api/v1/approvals/pending", h.requireAuth(h.listPendingApprovals))
	mux.HandleFunc("POST /api/v1/approvals/{runID}/{nodeID}", h.requireAuth(h.approveAuthed))
	mux.HandleFunc("GET /api/v1/admin/api-keys", h.requireAuth(h.listAPIKeys))
	mux.HandleFunc("POST /api/v1/admin/api-keys", h.requireAuth(h.issueAPIKey))
	mux.HandleFunc("DELETE /api/v1/admin/api-keys/{id}", h.requireAuth(h.revokeAPIKey))
	mux.HandleFunc("GET /api/v1/admin/users", h.requireAuth(h.listUsers))
	mux.HandleFunc("GET /api/v1/admin/tenants", h.requireAuth(h.listTenants))
	mux.HandleFunc("GET /api/v1/admin/audit", h.requireAuth(h.listAudit))
	mux.HandleFunc("GET /api/v1/admin/limits", h.requireAuth(h.workspaceLimits))
	// Version self-check for the System section of the admin page: compares
	// the running build against the newest upstream release tag. Platform
	// admins only (the answer is instance-wide, not per-org).
	mux.HandleFunc("GET /api/v1/admin/version", h.requireAuth(h.adminVersion))
	// Live tail of the daemon's own log stream as SSE — the System-section
	// "System log" viewer. platform:admin only; see admin_systemlog.go.
	mux.HandleFunc("GET /api/v1/admin/system/log", h.requireAuth(h.systemLogTail))
	// OAuth provider configuration: paste client_id + client_secret in
	// the admin UI instead of DAZYFLOW_OAUTH_*_CLIENT_ID env vars + a
	// restart. Persisted creds win over env on the next boot.
	// Tenant MCP servers: someone else's tool catalog, reachable as steps in
	// this org's flows. Same gate as runners — both add a SOURCE of steps —
	// see requireStepSourceAdmin.
	//
	// POST creates, PUT edits under the name in the path. Both reconnect, so
	// the response says whether the server actually answered.
	mux.HandleFunc("GET /api/v1/admin/mcp-servers", h.requireAuth(h.listMCPServers))
	mux.HandleFunc("POST /api/v1/admin/mcp-servers", h.requireAuth(h.saveMCPServer))
	mux.HandleFunc("PUT /api/v1/admin/mcp-servers/{name}", h.requireAuth(h.saveMCPServer))
	mux.HandleFunc("GET /api/v1/admin/mcp-servers/{name}/usage", h.requireAuth(h.mcpServerUsage))
	mux.HandleFunc("POST /api/v1/admin/mcp-servers/{name}/refresh", h.requireAuth(h.refreshMCPServer))
	mux.HandleFunc("DELETE /api/v1/admin/mcp-servers/{name}", h.requireAuth(h.deleteMCPServer))

	// Admin → Web APIs: the org's own described HTTP APIs. No refresh route,
	// unlike MCP servers: there is nothing to re-handshake with, and the
	// operations a catalog holds are the ones it was saved with. Re-importing
	// from a spec is a save, and belongs to the spec importer when it exists.
	mux.HandleFunc("GET /api/v1/admin/web-apis", h.requireAuth(h.listWebAPIs))
	mux.HandleFunc("POST /api/v1/admin/web-apis", h.requireAuth(h.saveWebAPI))
	// Reads a spec and reports what it offers. Stores nothing: the import is
	// the ordinary save that follows, carrying only the operations the admin
	// picked.
	mux.HandleFunc("POST /api/v1/admin/web-apis/spec", h.requireAuth(h.parseWebAPISpec))
	mux.HandleFunc("PUT /api/v1/admin/web-apis/{name}", h.requireAuth(h.saveWebAPI))
	mux.HandleFunc("GET /api/v1/admin/web-apis/{name}/usage", h.requireAuth(h.webAPIUsage))
	mux.HandleFunc("DELETE /api/v1/admin/web-apis/{name}", h.requireAuth(h.deleteWebAPI))

	// Tenant runners: an org's own code, reachable as a step in its flows.
	// Gated on organization:admin or module:register — see requireStepSourceAdmin
	// for why that is deliberately not graph:edit.
	mux.HandleFunc("GET /api/v1/admin/runners", h.requireAuth(h.listRunners))
	mux.HandleFunc("POST /api/v1/admin/runners/token", h.requireAuth(h.mintRunnerToken))
	// Retagging a registered machine — which pools it belongs to — without a
	// visit to it. Admin-gated with the rest: a label decides where work goes.
	mux.HandleFunc("PUT /api/v1/admin/runners/{name}/labels", h.requireAuth(h.setRunnerLabels))
	mux.HandleFunc("DELETE /api/v1/admin/runners/{name}", h.requireAuth(h.deleteRunner))
	// The same fleet, seen by someone building a flow rather than administering
	// one: names, labels and whether each machine is there, for the step's
	// "Machine" dropdown. graph:edit, because that is already all it takes to
	// target a runner — see listRunnerTargets.
	mux.HandleFunc("GET /api/v1/runners", h.requireAuth(h.listRunnerTargets))

	// The agent's own endpoints. Outside requireAuth on purpose: an agent holds
	// a runner credential, not a session or an API key, and it authorises
	// nothing beyond claiming that runner's work. Each handler authenticates
	// itself via authRunner.
	// The agent and runner.sh, unauthenticated: neither is a secret, and requiring
	// auth would break the one-line install for no gain.
	mux.HandleFunc("GET /dzrunner.py", h.serveRunnerAgent)
	mux.HandleFunc("GET /runner.sh", h.serveRunnerScript)

	// Throttled per IP like every other unauthenticated DB-touching route
	// here: each of these runs a token or credential lookup — /claim a locking
	// UPDATE as well — before it can decide the caller is a stranger. Register
	// takes the tighter webhook allowance because it is rare and opens a
	// transaction; the polled endpoints take the runner allowance, which has
	// to fit a whole office of agents behind one NAT.
	mux.HandleFunc("POST /api/v1/runner/register", h.rateLimitWebhook(h.registerRunner))
	mux.HandleFunc("POST /api/v1/runner/claim", h.rateLimitRunner(h.claimRunnerTask))
	mux.HandleFunc("POST /api/v1/runner/tasks/{id}/progress", h.rateLimitRunner(h.runnerTaskProgress))
	mux.HandleFunc("POST /api/v1/runner/tasks/{id}/result", h.rateLimitRunner(h.runnerTaskResult))

	mux.HandleFunc("GET /api/v1/admin/oauth-providers", h.requireAuth(h.listAdminOAuthProviders))
	mux.HandleFunc("PUT /api/v1/admin/oauth-providers/{name}", h.requireAuth(h.upsertAdminOAuthProvider))
	mux.HandleFunc("DELETE /api/v1/admin/oauth-providers/{name}", h.requireAuth(h.deleteAdminOAuthProvider))
	// Multi-org membership: a user can belong to many tenants (the
	// "home" tenant minted at signup + any they've been invited to).
	// switch-org re-issues the session against a different tenant the
	// caller has membership in. Memberships listing falls out of
	// whoami; no separate GET is needed.
	mux.HandleFunc("POST /api/v1/auth/switch-org", h.requireAuth(h.switchOrg))
	// Self-serve organization creation: any verified user can mint a new org
	// (they become its admin). Distinct from invitations, which add people to
	// an existing org.
	mux.HandleFunc("POST /api/v1/me/orgs", h.requireAuth(h.createOrg))

	// Invitations: admin creates / lists / revokes pending invites.
	// The accept side is split: GET /api/v1/invitations/{token} is
	// the public detail endpoint (no auth — the token IS the
	// credential at that step), and POST .../accept requires the
	// caller to be signed in so we know which user to bind the new
	// membership to.
	mux.HandleFunc("POST /api/v1/admin/invitations", h.requireAuth(h.createInvitation))
	mux.HandleFunc("GET /api/v1/admin/invitations", h.requireAuth(h.listInvitations))
	mux.HandleFunc("DELETE /api/v1/admin/invitations/{token}", h.requireAuth(h.revokeInvitation))
	// Platform signup-invites: a platform owner invites a specific email
	// to create its own account on a signup-disabled deployment. Distinct
	// from org invitations above — the recipient gets their own tenant,
	// not a membership. The token is consumed by the signUp gate, not an
	// accept endpoint. See signup_invite.go.
	mux.HandleFunc("POST /api/v1/admin/signup-invites", h.requireAuth(h.createSignupInvite))
	mux.HandleFunc("GET /api/v1/admin/signup-invites", h.requireAuth(h.listSignupInvites))
	mux.HandleFunc("DELETE /api/v1/admin/signup-invites/{token}", h.requireAuth(h.revokeSignupInvite))
	// Platform-admin SMTP smoke test — send one message through the
	// transactional Mailer to confirm it actually delivers. See admin_smtp.go.
	mux.HandleFunc("POST /api/v1/admin/smtp-test", h.requireAuth(h.smtpTest))
	mux.HandleFunc("GET /api/v1/admin/members", h.requireAuth(h.listMembers))
	mux.HandleFunc("PATCH /api/v1/admin/members/{email}", h.requireAuth(h.updateMemberRoles))
	mux.HandleFunc("DELETE /api/v1/admin/members/{email}", h.requireAuth(h.removeMember))
	// GDPR erasure (Art. 17): erase a whole account (platform admin) or
	// delete an entire org/tenant (platform admin, or org admin of that
	// tenant). Both confirmation-guarded; see gdpr_http.go.
	mux.HandleFunc("DELETE /api/v1/admin/users/{email}", h.requireAuth(h.adminDeleteUserHandler))
	mux.HandleFunc("GET /api/v1/admin/orgs/{tenant}/export", h.requireAuth(h.exportOrgHandler))
	mux.HandleFunc("DELETE /api/v1/admin/orgs/{tenant}", h.requireAuth(h.adminDeleteOrgHandler))

	// Platform-admin moderation surface (platform:admin only; see
	// admin_platform.go). User/org suspend-ban-list, and the drop
	// killswitch. Delete reuses the GDPR erase routes above.
	mux.HandleFunc("GET /api/v1/admin/platform/users", h.requireAuth(h.platformListUsers))
	mux.HandleFunc("GET /api/v1/admin/platform/users/{email}", h.requireAuth(h.platformGetUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/suspend", h.requireAuth(h.platformSuspendUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/unsuspend", h.requireAuth(h.platformUnsuspendUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/verify", h.requireAuth(h.platformVerifyUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/ban", h.requireAuth(h.platformBanUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/platform-admin", h.requireAuth(h.platformGrantAdmin))
	mux.HandleFunc("DELETE /api/v1/admin/platform/users/{email}/platform-admin", h.requireAuth(h.platformRevokeAdmin))
	// Support-agent management (cross-tenant vendor staff → support:agent role).
	mux.HandleFunc("GET /api/v1/admin/platform/support-agents", h.requireAuth(h.listSupportAgents))
	mux.HandleFunc("POST /api/v1/admin/platform/support-agents", h.requireAuth(h.grantSupportAgent))
	mux.HandleFunc("DELETE /api/v1/admin/platform/support-agents/{email}", h.requireAuth(h.revokeSupportAgent))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs", h.requireAuth(h.platformListOrgs))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs/{tenant}", h.requireAuth(h.platformGetOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/suspend", h.requireAuth(h.platformSuspendOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/unsuspend", h.requireAuth(h.platformUnsuspendOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/ban", h.requireAuth(h.platformBanOrg))
	mux.HandleFunc("GET /api/v1/admin/platform/drops", h.requireAuth(h.platformListDrops))
	mux.HandleFunc("POST /api/v1/admin/platform/drops/{id}/disable", h.requireAuth(h.platformDisableDrop))
	mux.HandleFunc("POST /api/v1/admin/platform/drops/{id}/enable", h.requireAuth(h.platformEnableDrop))
	// Tiers (reusable limit bundles) + per-org entitlement (tier + plan
	// grant + limit overrides) + cross-tenant member invite.
	mux.HandleFunc("GET /api/v1/admin/platform/tiers", h.requireAuth(h.platformListTiers))
	mux.HandleFunc("POST /api/v1/admin/platform/tiers", h.requireAuth(h.platformPutTier))
	mux.HandleFunc("PUT /api/v1/admin/platform/tiers/{id}", h.requireAuth(h.platformPutTier))
	mux.HandleFunc("DELETE /api/v1/admin/platform/tiers/{id}", h.requireAuth(h.platformDeleteTier))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs/{tenant}/entitlement", h.requireAuth(h.platformGetEntitlement))
	mux.HandleFunc("PUT /api/v1/admin/platform/orgs/{tenant}/entitlement", h.requireAuth(h.platformPutEntitlement))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/invite", h.requireAuth(h.platformInviteMember))

	// Unauthenticated, DB-touching public reads get the per-IP webhook limiter
	// so a stranger can't flood them into Postgres/connection-pool pressure.
	mux.HandleFunc("GET /api/v1/invitations/{token}", h.rateLimitWebhook(h.viewInvitation))
	mux.HandleFunc("POST /api/v1/invitations/{token}/accept", h.requireAuth(h.acceptInvitation))

	// Per-org SSO settings + Google sign-in round-trip.
	mux.HandleFunc("GET /api/v1/admin/org/auth-config", h.requireAuth(h.getOrgAuthConfig))
	mux.HandleFunc("PUT /api/v1/admin/org/auth-config", h.requireAuth(h.putOrgAuthConfig))
	mux.HandleFunc("DELETE /api/v1/admin/org/auth-config", h.requireAuth(h.deleteOrgAuthConfig))
	mux.HandleFunc("GET /api/v1/auth/sso/{tenant}", h.rateLimitWebhook(h.getPublicSSOStatus))
	mux.HandleFunc("GET /api/v1/auth/config", h.rateLimitWebhook(h.getPublicAuthConfig))
	// Public (pre-auth): map a wildcard host label to a tenant for sign-in.
	mux.HandleFunc("GET /api/v1/auth/resolve-subdomain", h.rateLimitWebhook(h.resolveSubdomain))
	// Public (pre-auth): Caddy on-demand-TLS authorization for org subdomains.
	mux.HandleFunc("GET /api/v1/auth/tls-allow", h.rateLimitWebhook(h.tlsAllow))
	mux.HandleFunc("GET /api/v1/admin/org/profile", h.requireAuth(h.getOrgProfile))
	mux.HandleFunc("PUT /api/v1/admin/org/profile", h.requireAuth(h.putOrgProfile))
	mux.HandleFunc("PUT /api/v1/admin/org/subdomain", h.requireAuth(h.putOrgSubdomain))
	mux.HandleFunc("GET /api/v1/admin/org/subdomain/available", h.requireAuth(h.orgSubdomainAvailable))
	mux.HandleFunc("GET /api/v1/auth/google/start", h.rateLimitWebhook(h.googleSignInStart))
	mux.HandleFunc("GET /api/v1/auth/google/callback", h.rateLimitWebhook(h.googleSignInCallback))
	// One-time handoff: the apex SSO callback bounces the session to the
	// org subdomain here so the cookie is set host-only on that origin
	// (per-org subdomains, Option B). Unauthenticated — the one-time code
	// in the query is the credential.
	mux.HandleFunc("GET /api/v1/auth/handoff", h.rateLimitWebhook(h.authHandoff))

	// Webhook trigger + hosted-form endpoints. Authenticated per-graph
	// (per-trigger bearer secret for /trigger; opt-in public_form for
	// /form) rather than via the daemon's API-key chain, so they sit
	// outside requireAuth. Mounting on the main HTTP gateway means a
	// default --http-only deploy serves these routes without the
	// operator having to spin up the optional standalone --webhook
	// listener too. cmd/dzd's --webhook flag still binds a separate
	// listener for operators who want port separation.
	//
	// Methods are listed explicitly because Go 1.22's ServeMux rejects
	// a method-any pattern that coexists with the GET / catch-all
	// (conflict: GET / matches fewer methods but a more general path).
	// The standalone WebhookListener registers method-any patterns
	// because it has no GET / catch-all to collide with; this mux does.
	if h.Webhook == nil {
		h.Webhook = NewWebhookListener(h.svc)
	}
	mux.HandleFunc("POST /trigger/", h.rateLimitWebhook(h.Webhook.handleTrigger))
	// /form/ is unauthenticated (public forms submit real runs), so it needs the
	// same per-IP throttle as /trigger/ to bound a flood of submissions.
	mux.HandleFunc("GET /form/", h.rateLimitWebhook(h.Webhook.handleForm))
	mux.HandleFunc("POST /form/", h.rateLimitWebhook(h.Webhook.handleForm))

	// Public workspace-overview snapshot for the TV-dashboard page. The
	// {token} in the path is the only credential — no requireAuth — so it's
	// throttled by the same per-IP webhook limiter as the other anonymous
	// surfaces. The token resolves to a (tenant, workspace) and returns a
	// sanitized, sensitive-data-free status snapshot.
	mux.HandleFunc("GET /api/v1/public/overview/{token}", h.rateLimitWebhook(h.publicOverview))

	// HMAC-verified approval endpoint for email/Slack approvers. The
	// token in the URL is the auth here, so no requireAuth wrapper —
	// the route 404s when Approval is nil (i.e. the operator hasn't
	// configured DAZYFLOW_APPROVAL_HMAC_SECRET + PUBLIC_BASE_URL).
	if h.Approval != nil {
		mux.HandleFunc("POST /approve/", h.Approval.handle)
	}

	// Static frontend bundle. Registered LAST so all explicit API
	// routes above match first; `GET /` is a catch-all for any
	// unknown GET path. Empty WebDist = no static serving.
	if h.WebDist != "" {
		if h.LandingDir != "" {
			mux.Handle("GET /", h.landingDistHandler(h.LandingDir, h.WebDist))
		} else {
			mux.Handle("GET /", webDistHandler(h.WebDist))
		}
	}
}
