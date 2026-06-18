package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
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
// params. Defaults to organization (tenant) scope so existing callers are
// unaffected.
func secretScopeFromRequest(r *http.Request) (scope SecretScope, flow string, err error) {
	switch s := SecretScope(r.URL.Query().Get("scope")); s {
	case "", ScopeTenant:
		return ScopeTenant, "", nil
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

// authorizeSecretScope gates a secret operation by scope. Organization reads
// need secret:read and writes secret:write. Flow scope needs graph:edit.
//
// NOTE: this tenant-wide form does NOT bind the authorization to a specific
// flow id — it's retained only for callers that don't yet pass through the
// per-flow gate (see httpresources.go, which has the same cross-flow gap and
// should migrate to authorizeFlowSecretScope). New secret CRUD paths must use
// (*HTTPGateway).authorizeFlowSecretScope so flow scope is authorized against
// the specific ?flow=<id>. Returns (status, message); status 0 = authorized.
func authorizeSecretScope(p core.Principal, scope SecretScope, write bool) (int, string) {
	if scope == ScopeFlow {
		if err := core.Require(p, core.PermGraphEdit); err != nil {
			return http.StatusForbidden, err.Error()
		}
		return 0, ""
	}
	perm := core.PermSecretRead
	if write {
		perm = core.PermSecretWrite
	}
	if err := core.Require(p, perm); err != nil {
		return http.StatusForbidden, err.Error()
	}
	return 0, ""
}

// authorizeFlowSecretScope gates a secret operation by scope, and for flow
// scope authorizes against the SPECIFIC flow (the ?flow=<id> that flows into
// the flow.<id>.<name> storage key) rather than the tenant-wide graph:edit
// permission. A bare core.Require(p, PermGraphEdit) would let any member with
// graph:edit read/overwrite/delete ANOTHER flow's secrets within the tenant —
// so we resolve the flow's graph and gate with the per-graph helper:
// AuthorizeGraphEdit for writes (PUT/DELETE), AuthorizeGraphView for reads
// (GET). Organization scope is delegated to authorizeSecretScope unchanged.
// Returns (status, message) with status 0 meaning authorized.
func (h *HTTPGateway) authorizeFlowSecretScope(ctx context.Context, p core.Principal, scope SecretScope, flow string, write bool) (int, string) {
	if scope != ScopeFlow {
		return authorizeSecretScope(p, scope, write)
	}
	// Resolve the flow id to its graph within the principal's workspace, then
	// authorize against that graph. A missing/unreadable flow is reported as
	// forbidden — the same shape as an authz failure — so a caller can't probe
	// which flow ids exist via the secrets endpoint.
	store, err := h.svc.Workspaces.Open(p.Tenant, p.Workspace)
	if err != nil {
		return http.StatusForbidden, "flow not accessible"
	}
	g, err := store.Load(flow)
	if err != nil {
		return http.StatusForbidden, "flow not accessible"
	}
	authz := core.AuthorizeGraphView
	if write {
		authz = core.AuthorizeGraphEdit
	}
	if err := authz(p, g); err != nil {
		return http.StatusForbidden, err.Error()
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
	if status, msg := h.authorizeFlowSecretScope(r.Context(), p, scope, flow, true); status != 0 {
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
	if err := h.EncryptedSecrets.PutScoped(r.Context(), p.Tenant, flow, scope, name, body.Value); err != nil {
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
	if status, msg := h.authorizeFlowSecretScope(r.Context(), p, scope, flow, false); status != 0 {
		writeJSONError(rw, status, msg)
		return
	}
	// ListScoped strips the scope prefix and, at tenant scope, hides every
	// reserved namespace (ws./flow./conn./oauth./cfg:).
	names, err := h.EncryptedSecrets.ListScoped(r.Context(), p.Tenant, flow, scope)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("list secrets: %v", err))
		return
	}
	// The Apps page detects which integrations are connected by checking
	// for conn.<slug>.<key> names, which ListScoped hides from the org
	// listing. ?include=conn opts those back in (full names retained) so
	// connection state is visible without exposing them on the Credentials
	// page, which doesn't pass the flag.
	if scope == ScopeTenant && r.URL.Query().Get("include") == "conn" {
		conns, err := h.EncryptedSecrets.ListConnectionNames(r.Context(), p.Tenant)
		if err != nil {
			writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("list connections: %v", err))
			return
		}
		names = append(names, conns...)
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
	if status, msg := h.authorizeFlowSecretScope(r.Context(), p, scope, flow, true); status != 0 {
		writeJSONError(rw, status, msg)
		return
	}
	if err := h.EncryptedSecrets.DeleteScoped(r.Context(), p.Tenant, flow, scope, name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete secret: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret.delete", name, string(scope))
	rw.WriteHeader(http.StatusNoContent)
}
