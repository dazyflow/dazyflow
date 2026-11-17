// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

// resolveTenantScope picks the tenant a tenant-scoped request acts on:
// ?tenant= if given, else the principal's own. It writes a 400 when neither
// names a tenant, and a 403 (with forbiddenMsg) when a non-platform-admin asks
// for a tenant other than their own. An unbound non-admin principal can't
// reach any tenant this way, matching core.RequireTenant.
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
