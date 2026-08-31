// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// API-key and tenant administration: issuing and revoking keys, and listing
// the tenants and users an admin can see.

import (
	"errors"
	"net/http"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// listAPIKeys, issueAPIKey, revokeAPIKey power the Admin UI's API
// keys card. All three require organization:admin (enforced in Service);
// without an AdminKeys store wired up they return 501.
func (h *HTTPGateway) listAPIKeys(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// ?tenant= narrows to a specific tenant. Platform admins may pass
	// any tenant; everyone else is force-scoped to their own.
	keys, err := h.svc.ListAPIKeys(r.Context(), p, r.URL.Query().Get("tenant"))
	if err != nil {
		adminError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"keys": keys})
}

// listTenants returns the set of tenants on this dzd. Platform admins
// only. Powers the tenant switcher in the top bar for super-admin UIs.
func (h *HTTPGateway) listTenants(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenants, err := h.svc.ListTenants(r.Context(), p)
	if err != nil {
		adminError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tenants": tenants})
}

func (h *HTTPGateway) issueAPIKey(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	params, ok := decodeRequestJSON[IssueAPIKeyParams](rw, r)
	if !ok {
		return
	}
	issued, err := h.svc.IssueAPIKey(r.Context(), p, params)
	if err != nil {
		adminError(rw, err)
		return
	}
	h.audit(r.Context(), p, "apikey.issue", params.Subject, "")
	writeJSON(rw, http.StatusCreated, issued)
}

// listUsers derives one entry per distinct Subject from the API keys
// in the principal's tenant. Roles + permissions are rolled up.
func (h *HTTPGateway) listUsers(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	users, err := h.svc.ListUsers(r.Context(), p, r.URL.Query().Get("tenant"))
	if err != nil {
		adminError(rw, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"users": users})
}

func (h *HTTPGateway) revokeAPIKey(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	id := r.PathValue("id")
	if err := h.svc.RevokeAPIKey(r.Context(), p, id); err != nil {
		adminError(rw, err)
		return
	}
	h.audit(r.Context(), p, "apikey.revoke", id, "")
	rw.WriteHeader(http.StatusNoContent)
}

// adminError maps known Service errors to HTTP statuses without
// duplicating the classification at every handler.
//
// Classifies on typed sentinels, not on message substrings. The substring
// form was fragile in both directions: rewording a user-facing message
// silently changed the status code, and any authorization error that wrapped
// core.ErrUnauthorized without also containing the literal phrase "requires
// permission" — e.g. the platform-admin-only role grant, or a key-id
// collision — fell through to 500 when it should have been 403.
func adminError(rw http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		writeJSONError(rw, http.StatusForbidden, msg)
	case errors.Is(err, errAdminNotConfigured):
		writeJSONError(rw, http.StatusNotImplemented, msg)
	case errors.Is(err, errAdminBadRequest),
		// A malformed / unparseable key id is bad client input, not a
		// server fault — e.g. DELETE /admin/api-keys/{id} with a junk id.
		errors.Is(err, auth.ErrInvalidCredential):
		writeJSONError(rw, http.StatusBadRequest, msg)
	default:
		writeJSONError(rw, http.StatusInternalServerError, msg)
	}
}
