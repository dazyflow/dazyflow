// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"fmt"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

// resolveTenantScope picks the tenant a request acts on: ?tenant= if given,
// else the principal's own. A non-platform-admin is pinned to their own
// tenant (an unbound one can reach none, as core.RequireTenant enforces);
// forbiddenMsg is the 403 message shown when they ask for another.
func resolveTenantScope(rw http.ResponseWriter, r *http.Request, p core.Principal, forbiddenMsg string) (string, bool) {
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_scope",
			"tenant required (no principal binding)")
		return "", false
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope", forbiddenMsg)
		return "", false
	}
	return tenant, true
}

// resolveScope is resolveTenantScope for tenant and workspace together.
// A non-platform-admin is pinned to their own tenant whatever they ask for,
// and to their workspace when bound to one (a tenant-wide principal may
// pick any workspace in its tenant). Rejecting here gives a clean 403 rather
// than the service layer's RequireTenant ErrUnauthorized.
func resolveScope(rw http.ResponseWriter, r *http.Request, p core.Principal, action string) (tenant, workspace string, ok bool) {
	tenant = r.URL.Query().Get("tenant")
	workspace = r.URL.Query().Get("workspace")
	if tenant == "" {
		tenant = p.Tenant
	}
	if workspace == "" {
		workspace = p.Workspace
	}
	if tenant == "" || workspace == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_scope",
			"tenant and workspace required (no principal binding)")
		return "", "", false
	}
	if isPlatformAdmin(p) {
		return tenant, workspace, true
	}
	if tenant != p.Tenant {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot %s tenant %q (principal is bound to %q)", action, tenant, p.Tenant))
		return "", "", false
	}
	if p.Workspace != "" && workspace != p.Workspace {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			fmt.Sprintf("cannot %s workspace %q (principal is bound to %q)", action, workspace, p.Workspace))
		return "", "", false
	}
	return tenant, workspace, true
}
