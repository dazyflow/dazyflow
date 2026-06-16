package daemon

import (
	"fmt"
	"net/http"

	"git.sr.ht/~klahr/hazyflow/core"
)

// maxRequestBody is the global ceiling on any request body. It equals the
// largest legitimate payload the API accepts (a file upload, maxUploadBytes);
// handlers that decode smaller bodies wrap r.Body in a stricter
// http.MaxBytesReader of their own (e.g. 64 KiB for secret values), which
// still trips first. This ceiling is just the backstop.
const maxRequestBody = maxUploadBytes

// limitRequestBody guards body-carrying requests two ways. It rejects a
// request whose declared Content-Length already exceeds the ceiling *before*
// any body is read, so an oversized request can't force a large allocation
// before a per-route MaxBytesReader would trip. And it wraps r.Body in
// http.MaxBytesReader as a backstop — covering chunked requests (Content-Length
// == -1, so the length pre-check can't see them) and any route that forgets to
// set its own limit. Per-route handlers may re-wrap r.Body with a smaller
// limit; the stricter one trips first, so this never loosens an existing cap.
func (h *HTTPGateway) limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			if r.ContentLength > maxRequestBody {
				writeAPIError(rw, http.StatusRequestEntityTooLarge, "payload_too_large",
					fmt.Sprintf("request body exceeds %d bytes", maxRequestBody))
				return
			}
			r.Body = http.MaxBytesReader(rw, r.Body, maxRequestBody)
		}
		next.ServeHTTP(rw, r)
	})
}

// workspaceLimits serves GET /api/v1/admin/limits — a read-only view of
// the effective limits that apply to the caller's tenant: the per-tenant
// disk quota (used + limit) plus the daemon-wide graph caps. organization:admin
// only. There's no write side — these are operator-configured (flags), so
// the admin UI surfaces them rather than pretending to edit them.
func (h *HTTPGateway) workspaceLimits(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !core.CanAdminOrg(p) {
		writeJSONError(rw, http.StatusForbidden, "organization:admin required")
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
