// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

type secretsAPI struct {
	auditor
	flowLoader
	svc              *Service
	EncryptedSecrets *EncryptedSecrets
	Profiles         auth.OrgProfileStore
}

func (h *HTTPGateway) secretsAPI() *secretsAPI {
	return &secretsAPI{auditor: h.auditor(), flowLoader: h.flows(), svc: h.svc, EncryptedSecrets: h.EncryptedSecrets, Profiles: h.Profiles}
}

// Values go IN but never come back out: listing returns names only.

const maxSecretValueBytes = 64 * 1024 // 64 KiB upper bound; OAuth tokens are ~hundreds of bytes

// The reserved prefixes (conn., oauth., cursor., res.) are machinery, so a user
// write there would silently shadow a connection's credential.
func checkReservedSecretWrite(scope SecretScope, name string) error {
	if strings.HasPrefix(name, secretFlowPrefix) {
		return fmt.Errorf("name %q uses the reserved %q prefix", name, secretFlowPrefix)
	}
	if scope == ScopeFlow && orgAuthoritativeSecretName(name) {
		return fmt.Errorf("name %q is organization-scoped and cannot be set per-flow", name)
	}
	return nil
}

type putSecretBody struct {
	Value string `json:"value"`
}

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

// Organization scope is read-widely, written narrowly.
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

// A flow-scoped secret additionally needs edit rights on THAT flow.
func (h *secretsAPI) authorizeFlowSecretScope(ctx context.Context, p core.Principal, scope SecretScope, flow string, write bool) (int, string) {
	if scope != ScopeFlow {
		return authorizeSecretScope(p, scope, write)
	}
	// Resolved within the principal's own workspace, so a foreign id cannot be used.
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

func noopSecretName(string) error { return nil }

// A deployment without the store answers 501, not 404.
func (h *secretsAPI) requireSecretStore(rw http.ResponseWriter, p core.Principal) bool {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return false
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return false
	}
	return true
}

// Shared, so the two surfaces cannot drift apart on authorization.
func (h *secretsAPI) secretCRUDGate(rw http.ResponseWriter, r *http.Request, p core.Principal, validate func(string) error, write bool) (name string, scope SecretScope, flow string, ok bool) {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return "", "", "", false
	}
	name = r.PathValue("name")
	if err := validate(name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return "", "", "", false
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return "", "", "", false
	}
	scope, flow, err := secretScopeFromRequest(r)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return "", "", "", false
	}
	if status, msg := h.authorizeFlowSecretScope(r.Context(), p, scope, flow, write); status != 0 {
		writeJSONError(rw, status, msg)
		return "", "", "", false
	}
	return name, scope, flow, true
}

func (h *secretsAPI) putSecret(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	name, scope, flow, ok := h.secretCRUDGate(rw, r, p, core.ValidSecretName, true)
	if !ok {
		return
	}
	if err := checkReservedSecretWrite(scope, name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	body, ok := decodeRequestJSON[putSecretBody](rw, r)
	if !ok {
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
	h.audit(r.Context(), p, "secret.put", name, string(scope))
	rw.WriteHeader(http.StatusNoContent)
}

func (h *secretsAPI) listSecrets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// Names alone still tell an attacker what a tenant integrates with.
	_, scope, flow, ok := h.secretCRUDGate(rw, r, p, noopSecretName, false)
	if !ok {
		return
	}
	names, err := h.EncryptedSecrets.ListScoped(r.Context(), p.Tenant, flow, scope)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("list secrets: %v", err))
		return
	}
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

func (h *secretsAPI) deleteSecret(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	name, scope, flow, ok := h.secretCRUDGate(rw, r, p, core.ValidSecretName, true)
	if !ok {
		return
	}
	if err := checkReservedSecretWrite(scope, name); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.EncryptedSecrets.DeleteScoped(r.Context(), p.Tenant, flow, scope, name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete secret: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret.delete", name, string(scope))
	rw.WriteHeader(http.StatusNoContent)
}
