package daemon

import (
	"net/http"

	"git.sr.ht/~klahr/hazyflow/core"
)

// workspaceLimits serves GET /api/v1/admin/limits — a read-only view of
// the effective limits that apply to the caller's tenant: the per-tenant
// disk quota (used + limit) plus the daemon-wide graph caps. tenant:admin
// only. There's no write side — these are operator-configured (flags), so
// the admin UI surfaces them rather than pretending to edit them.
func (h *HTTPGateway) workspaceLimits(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !p.Has(core.PermTenantAdmin) {
		writeJSONError(rw, http.StatusForbidden, "tenant:admin required")
		return
	}
	out := map[string]any{
		"tenant":                    p.Tenant,
		"max_graph_nodes":           h.svc.MaxGraphNodes,          // 0 = unlimited
		"max_graph_timeout_seconds": h.svc.MaxGraphTimeoutSeconds, // 0 = no ceiling
	}
	// Per-tenant disk quota, when a quota provider is wired.
	if h.svc.Engine != nil && h.svc.Engine.Quota != nil {
		q := map[string]any{"limit_bytes": h.svc.Engine.Quota.Limit(p.Tenant)}
		if used, err := h.svc.Engine.Quota.Used(p.Tenant); err == nil {
			q["used_bytes"] = used
		}
		out["quota"] = q
	}
	writeJSON(rw, http.StatusOK, out)
}
