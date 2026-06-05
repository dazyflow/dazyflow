package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
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

// secretScopeFromRequest reads the ?scope= (and ?flow= for flow scope) query
// params. Defaults to tenant scope so existing callers are unaffected.
func secretScopeFromRequest(r *http.Request) (scope SecretScope, flow string, err error) {
	switch s := SecretScope(r.URL.Query().Get("scope")); s {
	case "", ScopeTenant:
		return ScopeTenant, "", nil
	case ScopeWorkspace:
		return ScopeWorkspace, "", nil
	case ScopeFlow:
		flow = r.URL.Query().Get("flow")
		if flow == "" {
			return "", "", fmt.Errorf("scope=flow requires a flow id")
		}
		return ScopeFlow, flow, nil
	default:
		return "", "", fmt.Errorf("unknown scope %q", s)
	}
}

// authorizeSecretScope gates a secret operation by scope. Tenant/workspace
// reads need secret:read and writes secret:write (workspace additionally
// requires a workspace-bound principal). Flow scope needs graph:edit — if you
// can edit flows here you can manage a flow's own secrets; the resolution-time
// blast-radius guard (a flow resolves only its own flow secrets) is what makes
// this safe. Returns (status, message) with status 0 meaning authorized.
func authorizeSecretScope(p core.Principal, scope SecretScope, write bool) (int, string) {
	switch scope {
	case ScopeFlow:
		if err := core.Require(p, core.PermGraphEdit); err != nil {
			return http.StatusForbidden, err.Error()
		}
	case ScopeWorkspace:
		if p.Workspace == "" {
			return http.StatusBadRequest, "workspace scope requires a workspace-bound principal"
		}
		fallthrough
	default: // tenant
		perm := core.PermSecretRead
		if write {
			perm = core.PermSecretWrite
		}
		if err := core.Require(p, perm); err != nil {
			return http.StatusForbidden, err.Error()
		}
	}
	return 0, ""
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
	scope, flow, err := secretScopeFromRequest(r)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if status, msg := authorizeSecretScope(p, scope, true); status != 0 {
		writeJSONError(rw, status, msg)
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
	if err := h.EncryptedSecrets.PutScoped(r.Context(), p.Tenant, p.Workspace, flow, scope, name, body.Value); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("store secret: %v", err))
		return
	}
	// Audit the name + scope only — never the value.
	h.audit(r.Context(), p, "secret.put", name, string(scope))
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
	scope, flow, err := secretScopeFromRequest(r)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Listing names is gated on read/edit even though values aren't
	// returned — names alone leak which services a flow uses.
	if status, msg := authorizeSecretScope(p, scope, false); status != 0 {
		writeJSONError(rw, status, msg)
		return
	}
	// ListScoped strips the scope prefix and, at tenant scope, hides every
	// reserved namespace (ws./flow./conn./oauth./cfg:).
	names, err := h.EncryptedSecrets.ListScoped(r.Context(), p.Tenant, p.Workspace, flow, scope)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("list secrets: %v", err))
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"secrets": names, "scope": string(scope)})
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
	scope, flow, err := secretScopeFromRequest(r)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if status, msg := authorizeSecretScope(p, scope, true); status != 0 {
		writeJSONError(rw, status, msg)
		return
	}
	if err := h.EncryptedSecrets.DeleteScoped(r.Context(), p.Tenant, p.Workspace, flow, scope, name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete secret: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret.delete", name, string(scope))
	rw.WriteHeader(http.StatusNoContent)
}
