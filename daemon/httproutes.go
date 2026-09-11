// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"time"
)

func (h *HTTPGateway) mountRoutes(mux *http.ServeMux) {
	// Built here so the route list stays the one place a surface is declared.
	billing := h.billingAPI()
	flowapi := h.flowAPI()
	orgapi := h.orgAPI()
	authapi := h.authAPI()
	gdprapi := h.gdprAPI()
	auditapi := h.auditAPI()
	prefsapi := h.preferencesAPI()
	secretsapi := h.secretsAPI()
	oauthapi := h.oauthAPI()
	cloudsecrets := h.cloudSecretsAPI()
	filesapi := h.filesAPI()
	apikeys := h.apiKeyAPI()
	catalogapi := h.catalogAPI()
	platformadmin := h.platformAdminAPI()
	supportapi := h.supportAPI()
	runnerapi := h.runnerAPI()
	runctl := h.runCtlAPI()
	idem := h.idempotencyAPI()
	limitsapi := h.limitsAPI()
	staticapi := h.staticAPI()
	mapapi := h.mapAPI()
	shareapi := h.shareAPI()
	metricsapi := h.metricsAPI()
	gitmirror := h.gitMirrorAPI()
	versionapi := h.versionAPI()
	orgprofile := h.orgProfileAPI()
	webapis := h.webAPIsAPI()
	mcpapi := h.mcpAPI()

	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
		if _, err := rw.Write([]byte("ok")); err != nil {
			h.logger.Printf("healthz: write response: %v", err)
		}
	})
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
	if h.EnableMetrics {
		mux.HandleFunc("GET /metrics", metricsapi.metrics)
	}
	mux.HandleFunc("POST /api/v1/auth/signin", h.rateLimitAuth(authapi.signIn))
	mux.HandleFunc("POST /api/v1/auth/signup", h.rateLimitAuth(authapi.signUp))
	mux.HandleFunc("POST /api/v1/auth/verify-email", h.rateLimitAuth(authapi.verifyEmail))
	mux.HandleFunc("POST /api/v1/auth/forgot-password", h.rateLimitAuth(authapi.requestPasswordReset))
	mux.HandleFunc("POST /api/v1/auth/reset-password", h.rateLimitAuth(authapi.resetPassword))
	// Mints and sends mail, so it is rate-limited like the other auth routes.
	mux.HandleFunc("POST /api/v1/me/verification/resend", h.rateLimitAuth(h.requireAuth(authapi.resendVerification)))
	mux.HandleFunc("POST /api/v1/auth/signout", authapi.signOut)
	// Unauthenticated by design: the challenge token is not an authentication.
	mux.HandleFunc("POST /api/v1/auth/totp", h.rateLimitAuth(authapi.totpVerify))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(filesapi.uploadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/list", h.requireAuth(filesapi.listWorkspaceFiles))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/download", h.requireAuth(filesapi.downloadWorkspaceFile))
	mux.HandleFunc("GET /api/v1/workspaces/{tenant}/{workspace}/files/usage", h.requireAuth(filesapi.workspaceFileUsage))
	mux.HandleFunc("DELETE /api/v1/workspaces/{tenant}/{workspace}/files", h.requireAuth(filesapi.deleteWorkspaceFile))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files/mkdir", h.requireAuth(filesapi.mkdirWorkspaceDir))
	mux.HandleFunc("POST /api/v1/workspaces/{tenant}/{workspace}/files/rename", h.requireAuth(filesapi.renameWorkspaceFile))
	mux.HandleFunc("GET /api/v1/secrets", h.requireAuth(secretsapi.listSecrets))
	mux.HandleFunc("PUT /api/v1/secrets/{name}", h.requireAuth(secretsapi.putSecret))
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", h.requireAuth(secretsapi.deleteSecret))
	mux.HandleFunc("GET /api/v1/resources", h.requireAuth(secretsapi.listResources))
	mux.HandleFunc("PUT /api/v1/resources/{name}", h.requireAuth(secretsapi.putResource))
	mux.HandleFunc("DELETE /api/v1/resources/{name}", h.requireAuth(secretsapi.deleteResource))
	mux.HandleFunc("GET /api/v1/email-templates", h.requireAuth(secretsapi.listEmailTemplates))
	mux.HandleFunc("PUT /api/v1/email-templates/{name}", h.requireAuth(secretsapi.putEmailTemplate))
	mux.HandleFunc("DELETE /api/v1/email-templates/{name}", h.requireAuth(secretsapi.deleteEmailTemplate))
	mux.HandleFunc("POST /api/v1/email-templates/preview", h.requireAuth(secretsapi.previewEmailTemplate))
	mux.HandleFunc("POST /api/v1/email-templates/send-test", h.requireAuth(secretsapi.sendTestEmail))

	mux.HandleFunc("GET /api/v1/secret-manager", h.requireAuth(cloudsecrets.getSecretManager))
	mux.HandleFunc("PUT /api/v1/secret-manager", h.requireAuth(cloudsecrets.putSecretManager))
	mux.HandleFunc("DELETE /api/v1/secret-manager", h.requireAuth(cloudsecrets.deleteSecretManager))
	mux.HandleFunc("GET /api/v1/secret-manager/aws", h.requireAuth(cloudsecrets.getSecretManagerAws))
	mux.HandleFunc("PUT /api/v1/secret-manager/aws", h.requireAuth(cloudsecrets.putSecretManagerAws))
	mux.HandleFunc("DELETE /api/v1/secret-manager/aws", h.requireAuth(cloudsecrets.deleteSecretManagerAws))
	mux.HandleFunc("GET /api/v1/secret-manager/gcp", h.requireAuth(cloudsecrets.getSecretManagerGcp))
	mux.HandleFunc("PUT /api/v1/secret-manager/gcp", h.requireAuth(cloudsecrets.putSecretManagerGcp))
	mux.HandleFunc("DELETE /api/v1/secret-manager/gcp", h.requireAuth(cloudsecrets.deleteSecretManagerGcp))
	mux.HandleFunc("GET /api/v1/oauth/providers", h.requireAuth(oauthapi.oauthListProviders))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/accounts", h.requireAuth(oauthapi.oauthListAccounts))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/resources", h.requireAuth(flowapi.listAccountResources))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/authorize", h.requireAuth(oauthapi.oauthAuthorize))
	mux.HandleFunc("GET /api/v1/oauth/{provider}/callback", oauthapi.oauthCallback)
	mux.HandleFunc("GET /api/v1/drops", h.requireAuth(flowapi.listModules))
	mux.HandleFunc("GET /api/v1/modules", h.requireAuth(flowapi.listModules))

	mux.HandleFunc("GET /api/v1", catalogapi.serviceDescriptor)
	mux.HandleFunc("GET /api/v1/openapi.json", catalogapi.openAPISpec)
	mux.HandleFunc("GET /api/v1/catalog", h.requireAuth(catalogapi.catalogSummary))
	mux.HandleFunc("GET /api/v1/catalog/integrations", h.requireAuth(catalogapi.listIntegrationsHandler))
	mux.HandleFunc("GET /api/v1/catalog/integrations/{id}", h.requireAuth(catalogapi.getIntegrationHandler))
	mux.HandleFunc("PUT /api/v1/catalog/integrations/{id}/connection", h.requireAuth(secretsapi.putIntegrationConnection))
	mux.HandleFunc("POST /api/v1/catalog/integrations/{id}/verify", h.requireAuth(secretsapi.verifyIntegrationConnection))
	mux.HandleFunc("GET /api/v1/catalog/drops", h.requireAuth(catalogapi.listDropsHandler))
	mux.HandleFunc("GET /api/v1/catalog/drops/{id}", h.requireAuth(catalogapi.getDropHandler))
	mux.HandleFunc("GET /api/v1/catalog/trigger-kinds", h.requireAuth(catalogapi.triggerKindsHandler))

	mux.HandleFunc("GET /api/v1/me", h.requireAuth(catalogapi.meHandler))
	mux.HandleFunc("GET /api/v1/me/totp", h.requireAuth(authapi.totpStatus))
	mux.HandleFunc("POST /api/v1/me/totp/setup", h.requireAuth(authapi.totpSetup))
	mux.HandleFunc("POST /api/v1/me/totp/confirm", h.requireAuth(authapi.totpConfirm))
	mux.HandleFunc("POST /api/v1/me/totp/disable", h.requireAuth(authapi.totpDisable))
	mux.HandleFunc("POST /api/v1/me/totp/recovery-codes", h.requireAuth(authapi.totpRegenerate))
	mux.HandleFunc("GET /api/v1/me/preferences", h.requireAuth(prefsapi.getPreferences))
	mux.HandleFunc("PUT /api/v1/me/preferences", h.requireAuth(prefsapi.putPreferences))
	mux.HandleFunc("GET /api/v1/me/api-keys", h.requireAuth(catalogapi.listMyAPIKeysHandler))
	mux.HandleFunc("POST /api/v1/me/api-keys",
		h.requireAuth(idem.idempotencyMiddleware("/me/api-keys", catalogapi.issueMyAPIKeyHandler)))
	mux.HandleFunc("DELETE /api/v1/me/api-keys/{id}", h.requireAuth(catalogapi.revokeMyAPIKeyHandler))

	mux.HandleFunc("GET /api/v1/me/export", h.requireAuth(gdprapi.exportHandler))
	mux.HandleFunc("DELETE /api/v1/me/account", h.requireAuth(gdprapi.deleteMyAccountHandler))
	mux.HandleFunc("POST /api/v1/support/grants", h.requireAuth(supportapi.requestGrant))
	mux.HandleFunc("GET /api/v1/support/grants", h.requireAuth(supportapi.listGrants))
	mux.HandleFunc("GET /api/v1/support/grants/mine", h.requireAuth(supportapi.listMyGrants))
	mux.HandleFunc("POST /api/v1/support/grants/{id}/decide", h.requireAuth(supportapi.decideGrant))
	mux.HandleFunc("POST /api/v1/support/grants/{id}/revoke", h.requireAuth(supportapi.revokeGrant))
	mux.HandleFunc("GET /api/v1/support/flows/{tenant}/{workspace}/{flow_id}", h.requireAuth(supportapi.supportView))
	mux.HandleFunc("POST /api/v1/me/support/tickets", h.requireAuth(supportapi.createTicket))
	mux.HandleFunc("GET /api/v1/me/support/tickets", h.requireAuth(supportapi.listMyTickets))
	mux.HandleFunc("GET /api/v1/me/support/tickets/{id}", h.requireAuth(supportapi.getMyTicket))
	mux.HandleFunc("GET /api/v1/me/support/tickets/{id}/bundle", h.requireAuth(supportapi.getMyTicketBundle))
	mux.HandleFunc("POST /api/v1/me/support/tickets/{id}/messages", h.requireAuth(supportapi.postMyTicketMessage))
	mux.HandleFunc("POST /api/v1/me/support/tickets/{id}/status", h.requireAuth(supportapi.setMyTicketStatus))
	mux.HandleFunc("POST /api/v1/me/support/tickets/{id}/read", h.requireAuth(supportapi.markMyTicketRead))
	mux.HandleFunc("GET /api/v1/support/tickets", h.requireAuth(supportapi.listTicketQueue))
	mux.HandleFunc("GET /api/v1/support/tickets/summary", h.requireAuth(supportapi.ticketQueueSummary))
	mux.HandleFunc("GET /api/v1/support/tickets/{id}", h.requireAuth(supportapi.getSupportTicket))
	mux.HandleFunc("GET /api/v1/support/tickets/{id}/bundle", h.requireAuth(supportapi.getSupportTicketBundle))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/messages", h.requireAuth(supportapi.postSupportTicketMessage))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/status", h.requireAuth(supportapi.setSupportTicketStatus))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/read", h.requireAuth(supportapi.markSupportTicketRead))
	mux.HandleFunc("POST /api/v1/support/tickets/{id}/assign", h.requireAuth(supportapi.assignSupportTicket))
	mux.HandleFunc("POST /api/v1/me/password", h.requireAuth(authapi.changePasswordHandler))
	mux.HandleFunc("POST /api/v1/me/email", h.requireAuth(authapi.changeEmailHandler))

	mux.HandleFunc("GET /api/v1/me/usage", h.requireAuth(prefsapi.usageMe))
	mux.HandleFunc("GET /api/v1/me/billing", h.requireAuth(billing.billingMe))
	mux.HandleFunc("GET /api/v1/me/plans", h.requireAuth(billing.plansMe))
	mux.HandleFunc("POST /api/v1/me/billing/checkout", h.requireAuth(billing.billingCheckout))
	mux.HandleFunc("POST /api/v1/me/billing/portal", h.requireAuth(billing.billingPortal))
	mux.HandleFunc("GET /api/v1/me/flows", h.requireAuth(flowapi.listFlowsMe))
	mux.HandleFunc("GET /api/v1/me/flows/suggestions", h.requireAuth(flowapi.suggestionsMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}", h.requireAuth(flowapi.loadFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/history", h.requireAuth(flowapi.historyFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/published", h.requireAuth(flowapi.publishedFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/publish",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/publish", flowapi.publishFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/unpublish",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/unpublish", flowapi.unpublishFlowMe)))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/watch", h.requireAuth(flowapi.watchFlowMe))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/references", h.requireAuth(secretsapi.listReferences))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/input-fields", h.requireAuth(flowapi.listInputFields))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/samples", h.requireAuth(flowapi.flowSamples))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/restore",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/restore", flowapi.restoreFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/duplicate",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/duplicate", flowapi.duplicateFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/label",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/label", flowapi.labelRevisionMe)))
	mux.HandleFunc("PUT /api/v1/me/flows/{flow_id}",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}", flowapi.saveFlowMe)))
	mux.HandleFunc("PATCH /api/v1/me/flows/{flow_id}",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}", flowapi.patchFlowMe)))
	mux.HandleFunc("DELETE /api/v1/me/flows/{flow_id}", h.requireAuth(flowapi.deleteFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/enable",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/enable", flowapi.enableFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/disable",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/disable", flowapi.disableFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/run",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/run", flowapi.runFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/nodes/{node_id}/reset-state",
		h.requireAuth(flowapi.resetNodeStateMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/validate", h.requireAuth(flowapi.validateFlowMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/test-trigger",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/test-trigger", flowapi.testTriggerFlowMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/nodes/{node_id}/sample",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/nodes/{node_id}/sample", flowapi.sampleFlowNodeMe)))
	mux.HandleFunc("GET /api/v1/me/flows/{flow_id}/runs", h.requireAuth(flowapi.listFlowRunsMe))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/triggers/{node_id}/enable",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/triggers/{node_id}/enable", flowapi.enableTriggerMe)))
	mux.HandleFunc("POST /api/v1/me/flows/{flow_id}/triggers/{node_id}/disable",
		h.requireAuth(idem.idempotencyMiddleware("/me/flows/{flow_id}/triggers/{node_id}/disable", flowapi.disableTriggerMe)))

	mux.HandleFunc("GET /api/v1/me/schedules", h.requireAuth(flowapi.listSchedulesMe))

	mux.HandleFunc("GET /api/v1/me/boards", h.requireAuth(flowapi.listBoardsMe))
	mux.HandleFunc("GET /api/v1/me/boards/{name}", h.requireAuth(flowapi.getBoardMe))
	mux.HandleFunc("DELETE /api/v1/me/boards/{name}", h.requireAuth(flowapi.clearBoardMe))
	mux.HandleFunc("DELETE /api/v1/me/boards/{name}/rows/{rowid}", h.requireAuth(flowapi.deleteBoardRowMe))

	mux.HandleFunc("GET /api/v1/git/credentials", h.requireAuth(secretsapi.listGitCredsMe))
	mux.HandleFunc("PUT /api/v1/git/credentials/{account}", h.requireAuth(secretsapi.putGitCredMe))
	mux.HandleFunc("DELETE /api/v1/git/credentials/{account}", h.requireAuth(secretsapi.deleteGitCredMe))
	mux.HandleFunc("GET /api/v1/ssh/credentials", h.requireAuth(secretsapi.listSSHCredsMe))
	mux.HandleFunc("PUT /api/v1/ssh/credentials/{account}", h.requireAuth(secretsapi.putSSHCredMe))
	mux.HandleFunc("DELETE /api/v1/ssh/credentials/{account}", h.requireAuth(secretsapi.deleteSSHCredMe))
	mux.HandleFunc("POST /api/v1/ssh/credentials/{account}/verify", h.requireAuth(secretsapi.verifySSHCredMe))
	mux.HandleFunc("POST /api/v1/ssh/host-key", h.requireAuth(secretsapi.scanSSHHostKeyMe))
	mux.HandleFunc("POST /api/v1/ssh/keypair", h.requireAuth(secretsapi.generateSSHKeyMe))
	mux.HandleFunc("GET /api/v1/ssh/logins", h.requireAuth(secretsapi.listSSHLoginsMe))
	mux.HandleFunc("PUT /api/v1/ssh/logins/{name}", h.requireAuth(secretsapi.putSSHLoginMe))
	mux.HandleFunc("DELETE /api/v1/ssh/logins/{name}", h.requireAuth(secretsapi.deleteSSHLoginMe))

	mux.HandleFunc("GET /api/v1/git/mirror", h.requireAuth(gitmirror.getGitMirrorMe))
	mux.HandleFunc("PUT /api/v1/git/mirror", h.requireAuth(gitmirror.putGitMirrorMe))
	mux.HandleFunc("DELETE /api/v1/git/mirror", h.requireAuth(gitmirror.deleteGitMirrorMe))
	mux.HandleFunc("POST /api/v1/git/mirror/push", h.requireAuth(gitmirror.pushGitMirrorMe))

	mux.HandleFunc("GET /api/v1/me/runs", h.requireAuth(flowapi.listRunsMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}", h.requireAuth(flowapi.getRunMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/nodes", h.requireAuth(flowapi.listRunNodesMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/logs", h.requireAuth(flowapi.listRunLogsMe))
	mux.HandleFunc("DELETE /api/v1/me/runs/{run_id}/logs", h.requireAuth(flowapi.deleteRunLogsMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/nodes/{node_id}", h.requireAuth(flowapi.getRunNodeMe))
	mux.HandleFunc("GET /api/v1/me/runs/{run_id}/events", h.requireAuth(flowapi.runEventsMe))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/cancel",
		h.requireAuth(idem.idempotencyMiddleware("/me/runs/{run_id}/cancel", flowapi.cancelRunMe)))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/resume", h.requireAuth(flowapi.resumeRunMe))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/retry",
		h.requireAuth(idem.idempotencyMiddleware("/me/runs/{run_id}/retry", flowapi.retryRunMe)))
	mux.HandleFunc("POST /api/v1/me/runs/{run_id}/replay",
		h.requireAuth(idem.idempotencyMiddleware("/me/runs/{run_id}/replay", flowapi.replayRunMe)))

	mux.HandleFunc("GET /api/v1/me/share", h.requireAuth(shareapi.getShareMe))
	mux.HandleFunc("POST /api/v1/me/share", h.requireAuth(shareapi.createShareMe))
	mux.HandleFunc("DELETE /api/v1/me/share", h.requireAuth(shareapi.deleteShareMe))

	mux.HandleFunc("GET /api/v1/me/collection-shares", h.requireAuth(shareapi.listCollectionSharesMe))
	mux.HandleFunc("GET /api/v1/me/collection-shares/{name}", h.requireAuth(shareapi.getCollectionShareMe))
	mux.HandleFunc("POST /api/v1/me/collection-shares/{name}", h.requireAuth(shareapi.createCollectionShareMe))
	mux.HandleFunc("DELETE /api/v1/me/collection-shares/{name}", h.requireAuth(shareapi.deleteCollectionShareMe))

	mux.HandleFunc("GET /api/v1/me/connections", h.requireAuth(flowapi.listConnectionsMe))
	mux.HandleFunc("POST /api/v1/me/connections/{provider}/authorize",
		h.requireAuth(idem.idempotencyMiddleware("/me/connections/{provider}/authorize", flowapi.startConnectionMe)))
	mux.HandleFunc("DELETE /api/v1/me/connections/{provider}", h.requireAuth(flowapi.disconnectConnectionMe))
	mux.HandleFunc("POST /api/v1/validate/cron", h.requireAuth(validateCron))
	mux.HandleFunc("POST /api/v1/tools/render-template/preview", h.requireAuth(flowapi.renderTemplatePreview))
	mux.HandleFunc("POST /api/v1/tools/render-text/preview", h.requireAuth(flowapi.renderTextPreview))
	mux.HandleFunc("POST /api/v1/tools/expression/validate", h.requireAuth(flowapi.validateExpression))
	mux.HandleFunc("POST /api/v1/tools/render-template/assist", h.requireAuth(flowapi.renderTemplateAssist))
	mux.HandleFunc("GET /api/v1/tools/llm-providers", h.requireAuth(flowapi.renderTemplateLLMProviders))
	mux.HandleFunc("POST /api/v1/tools/flow/generate", h.requireAuth(flowapi.renderFlowGenerate))
	mux.HandleFunc("POST /api/v1/tools/flow/generate/stream", h.requireAuth(flowapi.renderFlowGenerateStream))
	mux.HandleFunc("POST /api/v1/validate/graph", h.requireAuth(flowapi.validateGraphLiteral))
	mux.HandleFunc("POST /api/v1/events/slack/{tenant}", h.rateLimitWebhook(runctl.slackEvents))
	mux.HandleFunc("POST /api/v1/events/github/{tenant}", h.rateLimitWebhook(runctl.githubEvents))
	mux.HandleFunc("POST /api/v1/events/stripe", h.rateLimitWebhook(billing.stripeEvents))
	mux.HandleFunc("POST /api/v1/events/stripe/{tenant}", h.rateLimitWebhook(runctl.stripeTenantEvents))
	mux.HandleFunc("GET /api/v1/approvals/pending", h.requireAuth(runctl.listPendingApprovals))
	mux.HandleFunc("GET /api/v1/approvals/pending/count", h.requireAuth(runctl.countPendingApprovals))
	mux.HandleFunc("GET /api/v1/approvals/decided", h.requireAuth(runctl.listDecidedApprovals))
	mux.HandleFunc("POST /api/v1/approvals/{runID}/{nodeID}", h.requireAuth(runctl.approveAuthed))
	mux.HandleFunc("GET /api/v1/admin/api-keys", h.requireAuth(apikeys.listAPIKeys))
	mux.HandleFunc("POST /api/v1/admin/api-keys", h.requireAuth(apikeys.issueAPIKey))
	mux.HandleFunc("DELETE /api/v1/admin/api-keys/{id}", h.requireAuth(apikeys.revokeAPIKey))
	mux.HandleFunc("GET /api/v1/admin/users", h.requireAuth(apikeys.listUsers))
	mux.HandleFunc("GET /api/v1/admin/tenants", h.requireAuth(apikeys.listTenants))
	mux.HandleFunc("GET /api/v1/admin/audit", h.requireAuth(auditapi.listAudit))
	mux.HandleFunc("GET /api/v1/admin/limits", h.requireAuth(limitsapi.workspaceLimits))
	mux.HandleFunc("GET /api/v1/admin/version", h.requireAuth(versionapi.adminVersion))
	mux.HandleFunc("GET /api/v1/admin/system/log", h.requireAuth(orgapi.systemLogTail))
	mux.HandleFunc("GET /api/v1/admin/mcp-servers", h.requireAuth(mcpapi.listMCPServers))
	mux.HandleFunc("POST /api/v1/admin/mcp-servers", h.requireAuth(mcpapi.saveMCPServer))
	mux.HandleFunc("PUT /api/v1/admin/mcp-servers/{name}", h.requireAuth(mcpapi.saveMCPServer))
	mux.HandleFunc("GET /api/v1/admin/mcp-servers/{name}/usage", h.requireAuth(mcpapi.mcpServerUsage))
	mux.HandleFunc("POST /api/v1/admin/mcp-servers/{name}/refresh", h.requireAuth(mcpapi.refreshMCPServer))
	mux.HandleFunc("DELETE /api/v1/admin/mcp-servers/{name}", h.requireAuth(mcpapi.deleteMCPServer))

	// No refresh route: a re-import is a diff the admin has to confirm.
	mux.HandleFunc("GET /api/v1/admin/web-apis", h.requireAuth(webapis.listWebAPIs))
	mux.HandleFunc("POST /api/v1/admin/web-apis", h.requireAuth(webapis.saveWebAPI))
	// Stores nothing: the import is a separate, confirmed step.
	mux.HandleFunc("POST /api/v1/admin/web-apis/spec", h.requireAuth(webapis.parseWebAPISpec))
	mux.HandleFunc("PUT /api/v1/admin/web-apis/{name}", h.requireAuth(webapis.saveWebAPI))
	mux.HandleFunc("GET /api/v1/admin/web-apis/{name}/usage", h.requireAuth(webapis.webAPIUsage))
	mux.HandleFunc("DELETE /api/v1/admin/web-apis/{name}", h.requireAuth(webapis.deleteWebAPI))

	mux.HandleFunc("GET /api/v1/admin/runners", h.requireAuth(runnerapi.listRunners))
	mux.HandleFunc("POST /api/v1/admin/runners/token", h.requireAuth(runnerapi.mintRunnerToken))
	mux.HandleFunc("PUT /api/v1/admin/runners/{name}/labels", h.requireAuth(runnerapi.setRunnerLabels))
	mux.HandleFunc("DELETE /api/v1/admin/runners/{name}", h.requireAuth(runnerapi.deleteRunner))
	mux.HandleFunc("GET /api/v1/runners", h.requireAuth(runnerapi.listRunnerTargets))

	// Outside requireAuth on purpose: an agent holds a runner key, not a session.
	mux.HandleFunc("GET /dzrunner.py", runnerapi.serveRunnerAgent)
	mux.HandleFunc("GET /runner.sh", runnerapi.serveRunnerScript)

	// Unauthenticated and DB-touching, so it is throttled per IP.
	mux.HandleFunc("POST /api/v1/runner/register", h.rateLimitWebhook(runnerapi.registerRunner))
	mux.HandleFunc("POST /api/v1/runner/claim", h.rateLimitRunner(runnerapi.claimRunnerTask))
	mux.HandleFunc("POST /api/v1/runner/tasks/{id}/progress", h.rateLimitRunner(runnerapi.runnerTaskProgress))
	mux.HandleFunc("POST /api/v1/runner/tasks/{id}/result", h.rateLimitRunner(runnerapi.runnerTaskResult))

	mux.HandleFunc("GET /api/v1/admin/oauth-providers", h.requireAuth(oauthapi.listAdminOAuthProviders))
	mux.HandleFunc("PUT /api/v1/admin/oauth-providers/{name}", h.requireAuth(oauthapi.upsertAdminOAuthProvider))
	mux.HandleFunc("DELETE /api/v1/admin/oauth-providers/{name}", h.requireAuth(oauthapi.deleteAdminOAuthProvider))
	mux.HandleFunc("POST /api/v1/auth/switch-org", h.requireAuth(orgapi.switchOrg))
	mux.HandleFunc("POST /api/v1/me/orgs", h.requireAuth(orgapi.createOrg))

	mux.HandleFunc("POST /api/v1/admin/invitations", h.requireAuth(orgapi.createInvitation))
	mux.HandleFunc("GET /api/v1/admin/invitations", h.requireAuth(orgapi.listInvitations))
	mux.HandleFunc("DELETE /api/v1/admin/invitations/{token}", h.requireAuth(orgapi.revokeInvitation))
	mux.HandleFunc("POST /api/v1/admin/signup-invites", h.requireAuth(authapi.createSignupInvite))
	mux.HandleFunc("GET /api/v1/admin/signup-invites", h.requireAuth(authapi.listSignupInvites))
	mux.HandleFunc("DELETE /api/v1/admin/signup-invites/{token}", h.requireAuth(authapi.revokeSignupInvite))
	mux.HandleFunc("POST /api/v1/admin/smtp-test", h.requireAuth(orgapi.smtpTest))
	mux.HandleFunc("GET /api/v1/admin/members", h.requireAuth(orgapi.listMembers))
	mux.HandleFunc("PATCH /api/v1/admin/members/{email}", h.requireAuth(orgapi.updateMemberRoles))
	mux.HandleFunc("DELETE /api/v1/admin/members/{email}", h.requireAuth(orgapi.removeMember))
	mux.HandleFunc("DELETE /api/v1/admin/users/{email}", h.requireAuth(gdprapi.adminDeleteUserHandler))
	mux.HandleFunc("GET /api/v1/admin/orgs/{tenant}/export", h.requireAuth(gdprapi.exportOrgHandler))
	mux.HandleFunc("DELETE /api/v1/admin/orgs/{tenant}", h.requireAuth(gdprapi.adminDeleteOrgHandler))

	mux.HandleFunc("GET /api/v1/admin/platform/users", h.requireAuth(platformadmin.platformListUsers))
	mux.HandleFunc("GET /api/v1/admin/platform/users/{email}", h.requireAuth(platformadmin.platformGetUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/suspend", h.requireAuth(platformadmin.platformSuspendUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/unsuspend", h.requireAuth(platformadmin.platformUnsuspendUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/verify", h.requireAuth(platformadmin.platformVerifyUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/ban", h.requireAuth(platformadmin.platformBanUser))
	mux.HandleFunc("POST /api/v1/admin/platform/users/{email}/platform-admin", h.requireAuth(platformadmin.platformGrantAdmin))
	mux.HandleFunc("DELETE /api/v1/admin/platform/users/{email}/platform-admin", h.requireAuth(platformadmin.platformRevokeAdmin))
	mux.HandleFunc("GET /api/v1/admin/platform/support-agents", h.requireAuth(orgapi.listSupportAgents))
	mux.HandleFunc("POST /api/v1/admin/platform/support-agents", h.requireAuth(orgapi.grantSupportAgent))
	mux.HandleFunc("DELETE /api/v1/admin/platform/support-agents/{email}", h.requireAuth(orgapi.revokeSupportAgent))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs", h.requireAuth(platformadmin.platformListOrgs))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs/{tenant}", h.requireAuth(platformadmin.platformGetOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/suspend", h.requireAuth(platformadmin.platformSuspendOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/unsuspend", h.requireAuth(platformadmin.platformUnsuspendOrg))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/ban", h.requireAuth(platformadmin.platformBanOrg))
	mux.HandleFunc("GET /api/v1/admin/platform/drops", h.requireAuth(platformadmin.platformListDrops))
	mux.HandleFunc("POST /api/v1/admin/platform/drops/{id}/disable", h.requireAuth(platformadmin.platformDisableDrop))
	mux.HandleFunc("POST /api/v1/admin/platform/drops/{id}/enable", h.requireAuth(platformadmin.platformEnableDrop))
	mux.HandleFunc("GET /api/v1/admin/platform/tiers", h.requireAuth(platformadmin.platformListTiers))
	mux.HandleFunc("POST /api/v1/admin/platform/tiers", h.requireAuth(platformadmin.platformPutTier))
	mux.HandleFunc("PUT /api/v1/admin/platform/tiers/{id}", h.requireAuth(platformadmin.platformPutTier))
	mux.HandleFunc("DELETE /api/v1/admin/platform/tiers/{id}", h.requireAuth(platformadmin.platformDeleteTier))
	mux.HandleFunc("GET /api/v1/admin/platform/orgs/{tenant}/entitlement", h.requireAuth(platformadmin.platformGetEntitlement))
	mux.HandleFunc("PUT /api/v1/admin/platform/orgs/{tenant}/entitlement", h.requireAuth(platformadmin.platformPutEntitlement))
	mux.HandleFunc("POST /api/v1/admin/platform/orgs/{tenant}/invite", h.requireAuth(platformadmin.platformInviteMember))

	mux.HandleFunc("GET /api/v1/invitations/{token}", h.rateLimitWebhook(orgapi.viewInvitation))
	mux.HandleFunc("POST /api/v1/invitations/{token}/accept", h.requireAuth(orgapi.acceptInvitation))

	mux.HandleFunc("GET /api/v1/admin/org/auth-config", h.requireAuth(orgapi.getOrgAuthConfig))
	mux.HandleFunc("PUT /api/v1/admin/org/auth-config", h.requireAuth(orgapi.putOrgAuthConfig))
	mux.HandleFunc("DELETE /api/v1/admin/org/auth-config", h.requireAuth(orgapi.deleteOrgAuthConfig))
	mux.HandleFunc("GET /api/v1/auth/sso/{tenant}", h.rateLimitWebhook(orgapi.getPublicSSOStatus))
	mux.HandleFunc("GET /api/v1/auth/config", h.rateLimitWebhook(orgapi.getPublicAuthConfig))
	mux.HandleFunc("GET /api/v1/map/config", h.rateLimitWebhook(mapapi.getMapConfig))
	mux.HandleFunc("GET /api/v1/auth/resolve-subdomain", h.rateLimitWebhook(orgprofile.resolveSubdomain))
	mux.HandleFunc("GET /api/v1/auth/tls-allow", h.rateLimitWebhook(orgprofile.tlsAllow))
	mux.HandleFunc("GET /api/v1/admin/org/profile", h.requireAuth(orgprofile.getOrgProfile))
	mux.HandleFunc("PUT /api/v1/admin/org/profile", h.requireAuth(orgprofile.putOrgProfile))
	mux.HandleFunc("PUT /api/v1/admin/org/subdomain", h.requireAuth(orgprofile.putOrgSubdomain))
	mux.HandleFunc("GET /api/v1/admin/org/subdomain/available", h.requireAuth(orgprofile.orgSubdomainAvailable))
	mux.HandleFunc("GET /api/v1/auth/google/start", h.rateLimitWebhook(authapi.googleSignInStart))
	mux.HandleFunc("GET /api/v1/auth/google/callback", h.rateLimitWebhook(authapi.googleSignInCallback))
	mux.HandleFunc("GET /api/v1/auth/handoff", h.rateLimitWebhook(authapi.authHandoff))

	// Authenticated per-graph by the key in the request, not by a session.
	if h.Webhook == nil {
		h.Webhook = NewWebhookListener(h.svc)
	}
	h.Webhook.idempotency = h.idempotency
	mux.HandleFunc("POST /trigger/", h.rateLimitWebhook(h.Webhook.handleTrigger))
	mux.HandleFunc("POST /call/", h.rateLimitWebhook(h.Webhook.handleCall))
	mux.HandleFunc("GET /form/", h.rateLimitWebhook(h.Webhook.handleForm))
	mux.HandleFunc("POST /form/", h.rateLimitWebhook(h.Webhook.handleForm))

	// Possession of the link is the credential.
	mux.HandleFunc("GET /api/v1/public/overview/{token}", h.rateLimitWebhook(shareapi.publicOverview))

	// Same token-is-the-credential model.
	mux.HandleFunc("GET /api/v1/public/collection/{token}", h.rateLimitWebhook(shareapi.publicCollection))

	if h.Approval != nil {
		mux.HandleFunc("GET /approve/", h.rateLimitWebhook(h.Approval.handleApprovalPage))
		mux.HandleFunc("POST /approve/", h.rateLimitWebhook(h.Approval.handle))
	}

	if h.WebDist != "" {
		if h.LandingDir != "" {
			mux.Handle("GET /", staticapi.withOrgBounce(staticapi.landingDistHandler(h.LandingDir, h.WebDist)))
		} else {
			mux.Handle("GET /", staticapi.withOrgBounce(webDistHandler(h.WebDist)))
		}
	}
}
