// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// shareAPI serves the public-share endpoints. Its fields are the whole of what
// those handlers touch.
type shareAPI struct {
	auditor
	urlBuilder
	svc *Service
}

// shareAPI builds them from the gateway's configuration.
func (h *HTTPGateway) shareAPI() *shareAPI {
	return &shareAPI{auditor: h.auditor(), urlBuilder: h.urls(), svc: h.svc}
}

// HTTP surface for the public workspace-overview share links.
//
// Authenticated CRUD lives under /api/v1/me/share (scoped to the caller's
// tenant/workspace via the same ?tenant=/?workspace= fallback the rest of the
// /me surface uses). The public, unauthenticated read lives at
// /api/v1/public/overview/{token} — the token is the only credential, so it
// sits alongside /trigger and /approve outside requireAuth and behind the
// webhook rate limiter.

// shareResponse is the wire shape of a share link. url is the absolute,
// ready-to-paste TV page; share_url is omitted when no public base is known
// and the daemon can't derive one (the client then builds it from its origin).
type shareResponse struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// shareURL builds the public TV page URL for a token against the daemon's
// effective external origin. Path mirrors the SPA route (/tv/:token).
func (h *shareAPI) shareURL(r *http.Request, token string) string {
	base := strings.TrimRight(h.effectiveBaseURL(r), "/")
	return base + "/tv/" + token
}

// getShareMe is GET /api/v1/me/share — the workspace's current overview link,
// or 404 share_not_found when none has been minted yet.
func (h *shareAPI) getShareMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	sh, exists, err := h.svc.GetWorkspaceShare(r.Context(), p, tenant, workspace)
	if err != nil {
		h.shareError(rw, err)
		return
	}
	if !exists {
		writeAPIError(rw, http.StatusNotFound, "share_not_found", "no share link for this workspace")
		return
	}
	writeJSON(rw, http.StatusOK, shareResponse{
		Token:     sh.Token,
		URL:       h.shareURL(r, sh.Token),
		CreatedAt: sh.CreatedAt,
		CreatedBy: sh.CreatedBy,
	})
}

// createShareMe is POST /api/v1/me/share — mint or rotate the workspace's
// overview link. Rotating invalidates whatever link was handed out before.
func (h *shareAPI) createShareMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	sh, err := h.svc.CreateWorkspaceShare(r.Context(), p, tenant, workspace)
	if err != nil {
		h.shareError(rw, err)
		return
	}
	h.audit(r.Context(), p, "workspace.share.create", tenant+"/"+workspace, "")
	writeJSON(rw, http.StatusOK, shareResponse{
		Token:     sh.Token,
		URL:       h.shareURL(r, sh.Token),
		CreatedAt: sh.CreatedAt,
		CreatedBy: sh.CreatedBy,
	})
}

// deleteShareMe is DELETE /api/v1/me/share — revoke the workspace's overview
// link. Idempotent: a workspace with no link still returns 204.
func (h *shareAPI) deleteShareMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	if err := h.svc.DeleteWorkspaceShare(r.Context(), p, tenant, workspace); err != nil {
		h.shareError(rw, err)
		return
	}
	h.audit(r.Context(), p, "workspace.share.delete", tenant+"/"+workspace, "")
	rw.WriteHeader(http.StatusNoContent)
}

// publicOverview is GET /api/v1/public/overview/{token} — the unauthenticated
// status snapshot the TV page polls. The token is the credential; an unknown
// or rotated token is a flat 404 (no hint about whether the workspace exists).
func (h *shareAPI) publicOverview(rw http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		writeAPIError(rw, http.StatusNotFound, "share_not_found", "unknown share link")
		return
	}
	data, err := h.svc.PublicWorkspaceOverview(r.Context(), token, time.Now())
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "share_not_found", "unknown share link")
			return
		}
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// The page polls every few seconds; let intermediaries cache very
	// briefly to blunt a hot link, but keep it effectively live.
	rw.Header().Set("Cache-Control", "public, max-age=2")
	writeJSON(rw, http.StatusOK, data)
}

// shareError maps the service-layer errors onto status codes: a forbidden
// scope/permission is 403, a missing store is 501, everything else 500.
func (h *shareAPI) shareError(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
	case strings.Contains(err.Error(), "not configured"):
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", err.Error())
	default:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
