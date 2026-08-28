// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// connectionVerifyTimeout bounds a live connection check. A connect/test is
// interactive — the user is waiting — so it must fail fast on an unreachable
// host rather than hang on a TCP/TLS handshake to nowhere.
const connectionVerifyTimeout = 12 * time.Second

// putConnectionBody is the request shape for PUT
// /catalog/integrations/{id}/connection: the field values the user entered,
// keyed by ConnectionField.Key. Only fields the user actually typed are
// present; omitted secret fields keep their stored value (edit-in-place).
type putConnectionBody struct {
	Values map[string]string `json:"values"`
}

// connectionFieldsForSlug finds the integration whose connection slug matches
// `slug` and returns its label and declared ConnectionFields. All drops in an
// integration declare the same fields, so the first match wins. Empty fields
// (with a nil error) means "no such connectable integration".
func (h *HTTPGateway) connectionFieldsForSlug(ctx context.Context, p core.Principal, slug string) (integration string, fields []core.ConnectionField, err error) {
	manifests, err := h.svc.ListDrops(ctx, p)
	if err != nil {
		return "", nil, err
	}
	for _, m := range manifests {
		if len(m.ConnectionFields) > 0 && core.ConnectionSlug(m.Integration) == slug {
			return m.Integration, m.ConnectionFields, nil
		}
	}
	return "", nil, nil
}

// candidateConnection builds the connection map a verifier sees: every
// declared field's stored value, overlaid with the (trimmed, non-empty)
// values submitted in this request. The returned `changed` map is just the
// submitted fields — what gets persisted once verification passes.
func (h *HTTPGateway) candidateConnection(ctx context.Context, tenant, integration string, fields []core.ConnectionField, submitted map[string]string) (conn, changed map[string]string) {
	declared := make(map[string]bool, len(fields))
	conn = make(map[string]string, len(fields))
	for _, f := range fields {
		declared[f.Key] = true
		if v, err := h.EncryptedSecrets.GetExact(ctx, tenant, core.ConnectionSecretKey(integration, f.Key)); err == nil && v != "" {
			conn[f.Key] = v
		}
	}
	changed = map[string]string{}
	for k, v := range submitted {
		if !declared[k] {
			continue // ignore stray keys the integration doesn't declare
		}
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		conn[k] = v
		changed[k] = v
	}
	return conn, changed
}

// missingRequired returns the label of the first required field left empty in
// the merged connection, or "" when all required fields are satisfied.
func missingRequired(fields []core.ConnectionField, conn map[string]string) string {
	for _, f := range fields {
		if f.Required && strings.TrimSpace(conn[f.Key]) == "" {
			return f.Label
		}
	}
	return ""
}

// putIntegrationConnection handles PUT /api/v1/catalog/integrations/{id}/connection.
// It verifies the candidate credentials against the live service (when the
// integration has a registered verifier) BEFORE storing them, so a bad DSN /
// key is rejected with the real error and never lands in the secret store
// showing a misleading "Connected". Integrations without a verifier just
// store — the field-by-field PUT /secrets path still works too; this one adds
// the atomic verify-then-save the Apps page uses.
func (h *HTTPGateway) putIntegrationConnection(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "missing_tenant", "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeAPIError(rw, http.StatusForbidden, "permission_denied", err.Error())
		return
	}
	slug := r.PathValue("id")
	integration, fields, err := h.connectionFieldsForSlug(r.Context(), p, slug)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if len(fields) == 0 {
		writeAPIError(rw, http.StatusNotFound, "integration_not_found", "no connectable integration: "+slug)
		return
	}

	var body putConnectionBody
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return
	}

	conn, changed := h.candidateConnection(r.Context(), p.Tenant, integration, fields, body.Values)
	if label := missingRequired(fields, conn); label != "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_field", fmt.Sprintf("%s is required", label))
		return
	}

	// Verify-before-save: a registered verifier gets the resolved candidate
	// connection. On failure nothing is stored, so the connection state never
	// flips to "Connected" on credentials that don't work.
	if verify, ok := engine.ConnectionVerifierFor(slug); ok {
		vctx, cancel := context.WithTimeout(r.Context(), connectionVerifyTimeout)
		defer cancel()
		if verr := verify(vctx, conn); verr != nil {
			writeAPIError(rw, http.StatusBadGateway, "verification_failed", verr.Error())
			return
		}
	}

	for k, v := range changed {
		if err := h.EncryptedSecrets.PutScoped(r.Context(), p.Tenant, "", ScopeTenant, core.ConnectionSecretKey(integration, k), v); err != nil {
			writeAPIError(rw, http.StatusInternalServerError, "store_failed", fmt.Sprintf("store %s: %v", k, err))
			return
		}
	}
	h.audit(r.Context(), p, "connection.put", slug, "")
	rw.WriteHeader(http.StatusNoContent)
}

// verifyIntegrationConnection handles POST /api/v1/catalog/integrations/{id}/verify.
// It re-tests the connection already stored for the tenant — the "Test
// connection" button on an established connection. Returns 200 with
// {"ok":true} on success or {"ok":false,"error":"…"} on a reachable failure,
// so the UI can render the outcome inline without treating it as a request
// error. 501 when the integration has no verifier; 409 when nothing is stored
// yet to test.
func (h *HTTPGateway) verifyIntegrationConnection(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "missing_tenant", "principal has no tenant")
		return
	}
	// Testing reads the decrypted stored credentials, so it's a secret:write-
	// gated action like connecting (a viewer can't probe the connection).
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeAPIError(rw, http.StatusForbidden, "permission_denied", err.Error())
		return
	}
	slug := r.PathValue("id")
	verify, ok := engine.ConnectionVerifierFor(slug)
	if !ok {
		writeAPIError(rw, http.StatusNotImplemented, "not_verifiable", "this connection can't be tested")
		return
	}
	integration, fields, err := h.connectionFieldsForSlug(r.Context(), p, slug)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if len(fields) == 0 {
		writeAPIError(rw, http.StatusNotFound, "integration_not_found", "no connectable integration: "+slug)
		return
	}

	conn, _ := h.candidateConnection(r.Context(), p.Tenant, integration, fields, nil)
	if label := missingRequired(fields, conn); label != "" {
		writeAPIError(rw, http.StatusConflict, "not_connected", fmt.Sprintf("connect %s first — %s is not set", integration, label))
		return
	}

	vctx, cancel := context.WithTimeout(r.Context(), connectionVerifyTimeout)
	defer cancel()
	if verr := verify(vctx, conn); verr != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"ok": false, "error": verr.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}
