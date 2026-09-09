// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

type preferencesAPI struct {
	auditor
	svc   *Service
	Users auth.UserStore
}

func (h *HTTPGateway) preferencesAPI() *preferencesAPI {
	return &preferencesAPI{auditor: h.auditor(), svc: h.svc, Users: h.Users}
}

// Account preferences live under /me/preferences (the caller acts on their own
// account). Two concerns share the surface: operational notifications the user
// may turn off (flow-failure email), and interface preferences that roam with
// the account (theme, language) rather than living in one browser's
// localStorage. Transactional and security mail is NOT governed here — it always
// sends.
//
// PUT is a PARTIAL update. The three settings are edited from independent UI
// controls, so a full-replace PUT would have each control clobber the others.
// Absent fields are left untouched; the response echoes the full resolved state.

func prefsEmail(p core.Principal) string { return p.Subject }

type preferencesResponse struct {
	EmailOnFlowFailure  bool   `json:"email_on_flow_failure"`
	EmailOnSupportReply bool   `json:"email_on_support_reply"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
}

type preferencesUpdate struct {
	EmailOnFlowFailure  *bool   `json:"email_on_flow_failure"`
	EmailOnSupportReply *bool   `json:"email_on_support_reply"`
	Theme               *string `json:"theme"`
	Language            *string `json:"language"`
}

var langPattern = regexp.MustCompile(`^[A-Za-z]{2}(-[A-Za-z]{2})?$`)

func responseFor(u auth.User) preferencesResponse {
	return preferencesResponse{
		EmailOnFlowFailure:  u.Notify.EmailOnFlowFailureEnabled(),
		EmailOnSupportReply: u.Notify.EmailOnSupportReplyEnabled(),
		Theme:               u.UI.Theme,
		Language:            u.UI.Language,
	}
}

func (h *preferencesAPI) getPreferences(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password auth not configured")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), prefsEmail(p))
	if errors.Is(err, auth.ErrUnknownUser) {
		// API-key / SSO principals have no password-user record. Report
		// defaults rather than erroring, mirroring totpStatus.
		writeJSON(rw, http.StatusOK, responseFor(auth.User{}))
		return
	}
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not read preferences")
		return
	}
	writeJSON(rw, http.StatusOK, responseFor(u))
}

// putPreferences is PUT /api/v1/me/preferences — applies the present
// fields and persists. Validates theme/language before writing so the
// store never holds a value the client couldn't have produced.
func (h *preferencesAPI) putPreferences(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password auth not configured")
		return
	}
	body, ok := decodeRequestJSONOptional[preferencesUpdate](rw, r)
	if !ok {
		return
	}
	// "system" (follow the OS) is the default choice, so it has to round-trip
	// like any other — otherwise picking it on one device silently fails to
	// roam and the next device keeps whatever it had cached. "" stays valid:
	// it's what accounts predating the theme picker already hold, and the web
	// reads it as "no explicit choice", i.e. the same thing as system.
	if body.Theme != nil && *body.Theme != "" && *body.Theme != "system" &&
		*body.Theme != "dark" && *body.Theme != "light" {
		writeAPIError(rw, http.StatusBadRequest, "invalid_theme",
			`theme must be "system", "dark", "light", or ""`)
		return
	}
	if body.Language != nil && *body.Language != "" && !langPattern.MatchString(*body.Language) {
		writeAPIError(rw, http.StatusBadRequest, "invalid_language", `language must be a locale code like "en" or "pt-BR", or ""`)
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), prefsEmail(p))
	if errors.Is(err, auth.ErrUnknownUser) {
		writeAPIError(rw, http.StatusBadRequest, "no_user", "no password account for this principal")
		return
	}
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not read preferences")
		return
	}
	if body.EmailOnFlowFailure != nil {
		v := *body.EmailOnFlowFailure
		u.Notify.EmailOnFlowFailure = &v
	}
	if body.EmailOnSupportReply != nil {
		v := *body.EmailOnSupportReply
		u.Notify.EmailOnSupportReply = &v
	}
	if body.Theme != nil {
		u.UI.Theme = *body.Theme
	}
	if body.Language != nil {
		u.UI.Language = *body.Language
	}
	if err := h.Users.PutUser(r.Context(), u); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not save preferences")
		return
	}
	h.audit(r.Context(), p, "preferences.update", p.Subject, "")
	writeJSON(rw, http.StatusOK, responseFor(u))
}
