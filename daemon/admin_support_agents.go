// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// admin_support_agents.go is the platform-admin management surface for support
// agents (see TODO-support-tickets.md). Support agents are cross-tenant
// vendor/operator staff, so provisioning them is a platform:admin action — the
// mirror of the platform-admin grant management next door in admin_platform.go.
// A support agent need NOT have an account here (they may sign in via SSO and be
// elevated on the fly); the store is keyed on email, so we grant by email.

// listSupportAgents returns every provisioned support-agent email.
// GET /api/v1/admin/platform/support-agents
func (h *HTTPGateway) listSupportAgents(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.SupportAgents == nil {
		writeJSONError(rw, http.StatusNotImplemented, "support agents are not enabled on this deployment")
		return
	}
	agents, err := h.SupportAgents.List(r.Context())
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"agents": agents})
}

// grantSupportAgent provisions an email as a support agent. The role is stamped
// at session issue, so any live sessions for that email are dropped to force a
// re-auth that picks it up. POST /api/v1/admin/platform/support-agents {email}
func (h *HTTPGateway) grantSupportAgent(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.SupportAgents == nil {
		writeJSONError(rw, http.StatusNotImplemented, "support agents are not enabled on this deployment")
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "invalid JSON body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || !strings.Contains(email, "@") {
		writeJSONError(rw, http.StatusBadRequest, "a valid email is required")
		return
	}
	if err := h.SupportAgents.Grant(r.Context(), email, p.Subject); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Drop live sessions (if the email has an account here) so the role applies
	// on their next request rather than at session expiry. No account is fine —
	// a vendor agent may not have signed in yet.
	if h.Users != nil {
		if u, err := h.Users.GetByEmail(r.Context(), email); err == nil {
			h.revokeSubjectSessions(r.Context(), u.Subject)
		}
	}
	h.audit(r.Context(), p, "support_agent.grant", email, "")
	agents, _ := h.SupportAgents.List(r.Context())
	writeJSON(rw, http.StatusOK, map[string]any{"agents": agents})
}

// revokeSupportAgent removes a support-agent grant. Live sessions are dropped so
// the role is gone on the target's next request.
// DELETE /api/v1/admin/platform/support-agents/{email}
func (h *HTTPGateway) revokeSupportAgent(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	if h.SupportAgents == nil {
		writeJSONError(rw, http.StatusNotImplemented, "support agents are not enabled on this deployment")
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PathValue("email")))
	if email == "" {
		writeJSONError(rw, http.StatusBadRequest, "email required")
		return
	}
	if err := h.SupportAgents.Revoke(r.Context(), email); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if h.Users != nil {
		if u, err := h.Users.GetByEmail(r.Context(), email); err == nil {
			h.revokeSubjectSessions(r.Context(), u.Subject)
		}
	}
	h.audit(r.Context(), p, "support_agent.revoke", email, "")
	agents, _ := h.SupportAgents.List(r.Context())
	writeJSON(rw, http.StatusOK, map[string]any{"agents": agents})
}
