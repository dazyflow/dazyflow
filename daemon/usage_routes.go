package daemon

import (
	"net/http"
	"strconv"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// GET /api/v1/me/usage — the caller's tenant usage, newest month first.
// ?tenant= overrides the principal binding (scope-checked), ?months=
// bounds the history (default 6, max 24). The current month is always
// present — synthesized at zero when the tenant has no activity yet —
// so the UI's headline numbers never depend on "did anything run".
func (h *HTTPGateway) usageMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.svc.Usage == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"usage metering is not enabled on this deployment")
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant == "" {
		writeAPIError(rw, http.StatusBadRequest, "missing_scope",
			"tenant required (no principal binding)")
		return
	}
	if p.Tenant != "" && tenant != p.Tenant && !isPlatformAdmin(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden_scope",
			"cannot read usage for another tenant")
		return
	}

	months := 6
	if m, err := strconv.Atoi(r.URL.Query().Get("months")); err == nil && m > 0 {
		months = min(m, 24)
	}

	buckets, err := h.svc.Usage.Usage(r.Context(), tenant, months)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	current := usagePeriod(time.Now())
	if len(buckets) == 0 || buckets[0].Period != current {
		buckets = append([]UsageCounters{{Period: current}}, buckets...)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"usage": buckets})
}
