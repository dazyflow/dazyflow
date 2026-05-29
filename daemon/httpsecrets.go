package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// CRUD endpoints for the built-in encrypted secret store. The shape
// is deliberately small and follows the same Bearer-auth contract as
// the rest of the gateway.
//
//	GET    /api/v1/secrets         → list this tenant's secret NAMES
//	PUT    /api/v1/secrets/{name}  → create or replace a secret
//	DELETE /api/v1/secrets/{name}  → remove a secret
//
// Note: there is NO "GET /api/v1/secrets/{name}" — values are
// write-only from the outside. The UI shows "Slack token: set ✓",
// never the value back. This is the same pattern Zapier and most
// secret managers use; preventing read-back means a compromised UI
// session can't exfiltrate already-stored tokens.

const maxSecretValueBytes = 64 * 1024 // 64 KiB upper bound; OAuth tokens are ~hundreds of bytes

// secretValidNameChars: alphanumerics + dash/underscore/dot. Stops
// path-like names ("/.." "../") and shell-special characters from
// landing in the store, which simplifies any future tooling that
// scripts against secret names.
func validSecretName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("name too long (max 128)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("name may only contain [A-Za-z0-9_.-]")
		}
	}
	return nil
}

// putSecretBody is the request shape for PUT /secrets/{name}.
type putSecretBody struct {
	Value string `json:"value"`
}

// putSecret writes a secret for the requesting principal's tenant.
// PUT semantics: idempotent, replaces any existing value at the
// same name. Returns 204 on success.
func (h *HTTPGateway) putSecret(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	name := r.PathValue("name")
	if err := validSecretName(name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var body putSecretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	body.Value = strings.TrimRight(body.Value, "\n")
	if body.Value == "" {
		writeJSONError(rw, http.StatusBadRequest, "value must not be empty")
		return
	}
	if err := h.EncryptedSecrets.Put(r.Context(), p.Tenant, name, body.Value); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("store secret: %v", err))
		return
	}
	// Audit the name only — never the value.
	h.audit(r.Context(), p, "secret.put", name, "")
	rw.WriteHeader(http.StatusNoContent)
}

// listSecrets returns the names (not the values) of the principal's
// secrets. Sorted alphabetically by the store; the UI can render
// them as-is.
func (h *HTTPGateway) listSecrets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	// Listing names is gated on PermSecretRead even though values
	// aren't returned — names alone can leak information (which
	// services a tenant uses) and shouldn't be world-readable
	// within the tenant.
	if err := core.Require(p, core.PermSecretRead); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	names, err := h.EncryptedSecrets.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("list secrets: %v", err))
		return
	}
	// Hide internal "cfg:" entries (e.g. the BYO secret-manager config) — they
	// aren't user secrets and shouldn't appear in the UI.
	names = filterReservedSecretNames(names)
	if names == nil {
		names = []string{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"secrets": names})
}

// deleteSecret removes a secret. Idempotent — deleting a missing
// secret returns 204 just like deleting an existing one.
func (h *HTTPGateway) deleteSecret(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	name := r.PathValue("name")
	if err := validSecretName(name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	if err := h.EncryptedSecrets.Delete(r.Context(), p.Tenant, name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete secret: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret.delete", name, "")
	rw.WriteHeader(http.StatusNoContent)
}
