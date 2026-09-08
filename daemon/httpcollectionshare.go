// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// HTTP surface for the public collection links (see collectionshare.go).
//
// Authenticated CRUD lives under /api/v1/me/collection-shares, scoped to the
// caller's tenant/workspace via the same ?tenant=/?workspace= fallback the
// rest of the /me surface uses. The public, unauthenticated read lives at
// /api/v1/public/collection/{token} — the token is the only credential, so it
// sits alongside /trigger, /approve and the public overview outside
// requireAuth and behind the webhook rate limiter.

type collectionShareResponse struct {
	Collection string    `json:"collection"`
	Token      string    `json:"token"`
	URL        string    `json:"url"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by,omitempty"`
}

func (h *shareAPI) collectionShareURL(r *http.Request, token string) string {
	base := strings.TrimRight(h.effectiveBaseURL(r), "/")
	return base + "/board/" + token
}

func (h *shareAPI) collectionShareBody(r *http.Request, sh CollectionShare) collectionShareResponse {
	return collectionShareResponse{
		Collection: sh.Collection,
		Token:      sh.Token,
		URL:        h.collectionShareURL(r, sh.Token),
		CreatedAt:  sh.CreatedAt,
		CreatedBy:  sh.CreatedBy,
	}
}

func (h *shareAPI) listCollectionSharesMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	shares, err := h.svc.ListCollectionShares(r.Context(), p, tenant, workspace)
	if err != nil {
		h.collectionShareError(rw, err)
		return
	}
	out := make([]collectionShareResponse, 0, len(shares))
	for _, sh := range shares {
		out = append(out, h.collectionShareBody(r, sh))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"shares": out})
}

func (h *shareAPI) getCollectionShareMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	sh, exists, err := h.svc.GetCollectionShare(r.Context(), p, tenant, workspace, r.PathValue("name"))
	if err != nil {
		h.collectionShareError(rw, err)
		return
	}
	if !exists {
		writeAPIError(rw, http.StatusNotFound, "share_not_found", "no public link for this collection")
		return
	}
	writeJSON(rw, http.StatusOK, h.collectionShareBody(r, sh))
}

func (h *shareAPI) createCollectionShareMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	name := r.PathValue("name")
	sh, err := h.svc.CreateCollectionShare(r.Context(), p, tenant, workspace, name)
	if err != nil {
		h.collectionShareError(rw, err)
		return
	}
	h.audit(r.Context(), p, "collection.share.create", name, "tenant="+tenant+" workspace="+workspace)
	writeJSON(rw, http.StatusOK, h.collectionShareBody(r, sh))
}

func (h *shareAPI) deleteCollectionShareMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if err := h.svc.DeleteCollectionShare(r.Context(), p, tenant, workspace, name); err != nil {
		h.collectionShareError(rw, err)
		return
	}
	h.audit(r.Context(), p, "collection.share.delete", name, "tenant="+tenant+" workspace="+workspace)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *shareAPI) publicCollection(rw http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		writeAPIError(rw, http.StatusNotFound, "share_not_found", "unknown share link")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	data, err := h.svc.PublicCollection(r.Context(), token, limit, offset, time.Now())
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			writeAPIError(rw, http.StatusNotFound, "share_not_found", "unknown share link")
			return
		}
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	rw.Header().Set("Cache-Control", "private, no-store")
	writeJSON(rw, http.StatusOK, data)
}

func (h *shareAPI) collectionShareError(rw http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
	case isCollectionSharesUnavailable(err):
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", err.Error())
	case errors.Is(err, errBoardNotFound):
		writeAPIError(rw, http.StatusNotFound, "board_not_found", err.Error())
	case errors.Is(err, errBoardInvalidName):
		writeAPIError(rw, http.StatusBadRequest, "invalid_board", err.Error())
	case errors.Is(err, errBoardsUnavailable):
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", err.Error())
	default:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
