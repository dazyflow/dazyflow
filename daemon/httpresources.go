// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Flow resources (${resource.NAME}) are named pointers at external content
// — e.g. a specific Google Sheet — that the engine fetches live when a
// param references them. Their definitions are CRUD'd here and stored in
// the encrypted secret store under the reserved "res." namespace, with the
// same flow→organization scoping as secrets (see secret_scope.go). Unlike
// secrets, a resource's config is NOT sensitive, so GET returns it.

type putResourceBody struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

type resourceView struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}

// validResourceName is validSecretName plus a no-dot rule: ${resource.NAME.path}
// splits on the first dot to separate the name from the sub-path, so a
// dotted resource name would be unreferenceable.
func validResourceName(name string) error {
	if err := validSecretName(name); err != nil {
		return err
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("resource name may not contain '.'")
	}
	return nil
}

// putResource creates/replaces a resource definition. PUT semantics:
// idempotent. Gated like a flow-scoped secret write (graph:edit at flow
// scope, secret:write at organization scope).
func (h *HTTPGateway) putResource(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	name, scope, flow, ok := h.secretCRUDGate(rw, r, p, validResourceName, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var body putResourceBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if strings.TrimSpace(body.Type) == "" {
		writeJSONError(rw, http.StatusBadRequest, "type must not be empty")
		return
	}
	def := core.ResourceDef{Name: name, Type: body.Type, Config: body.Config}
	raw, err := json.Marshal(def)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("encode resource: %v", err))
		return
	}
	if err := h.EncryptedSecrets.PutScoped(r.Context(), p.Tenant, flow, scope, secretResourcePrefix+name, string(raw)); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("store resource: %v", err))
		return
	}
	h.audit(r.Context(), p, "resource.put", name, string(scope))
	rw.WriteHeader(http.StatusNoContent)
}

// listResources returns the resource definitions at one scope. Config is
// returned (it's not secret). Resources are hidden from the Credentials
// listing (the "res." prefix is reserved), so this is the only way to see
// them.
func (h *HTTPGateway) listResources(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	_, scope, flow, ok := h.secretCRUDGate(rw, r, p, noopSecretName, false)
	if !ok {
		return
	}
	storageNames, err := h.resourceStorageNames(r.Context(), p.Tenant, flow, scope)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("list resources: %v", err))
		return
	}
	out := make([]resourceView, 0, len(storageNames))
	for resName, storage := range storageNames {
		raw, err := h.EncryptedSecrets.GetExact(r.Context(), p.Tenant, storage)
		if err != nil {
			continue // racing delete, or a stray prefix collision
		}
		var def core.ResourceDef
		if err := json.Unmarshal([]byte(raw), &def); err != nil {
			continue
		}
		out = append(out, resourceView{Name: resName, Type: def.Type, Config: def.Config})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(rw, http.StatusOK, map[string]any{"resources": out, "scope": string(scope)})
}

// deleteResource removes a resource definition. Idempotent.
func (h *HTTPGateway) deleteResource(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	name, scope, flow, ok := h.secretCRUDGate(rw, r, p, validResourceName, true)
	if !ok {
		return
	}
	if err := h.EncryptedSecrets.DeleteScoped(r.Context(), p.Tenant, flow, scope, secretResourcePrefix+name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete resource: %v", err))
		return
	}
	h.audit(r.Context(), p, "resource.delete", name, string(scope))
	rw.WriteHeader(http.StatusNoContent)
}

// resourceStorageNames maps resource name → its full storage name at the
// scope. ListScoped can't be used (it hides the reserved "res." prefix), so
// this filters the raw name list itself.
func (h *HTTPGateway) resourceStorageNames(ctx context.Context, tenant, flow string, scope SecretScope) (map[string]string, error) {
	all, err := h.EncryptedSecrets.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	switch scope {
	case ScopeFlow:
		if flow == "" {
			return nil, fmt.Errorf("listing flow resources needs a flow")
		}
		prefix := secretFlowPrefix + flow + "." + secretResourcePrefix // flow.<flow>.res.
		for _, n := range all {
			if strings.HasPrefix(n, prefix) {
				out[strings.TrimPrefix(n, prefix)] = n
			}
		}
	default: // tenant
		for _, n := range all {
			// Organization resources are exactly "res.<name>" — exclude the
			// flow-scoped "flow.….res.…" entries.
			if strings.HasPrefix(n, secretResourcePrefix) && !strings.HasPrefix(n, secretFlowPrefix) {
				out[strings.TrimPrefix(n, secretResourcePrefix)] = n
			}
		}
	}
	return out, nil
}
