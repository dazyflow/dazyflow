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

// preferencesAPI serves the user-preference and usage endpoints. Its fields are the whole of what
// those handlers touch.
type preferencesAPI struct {
	auditor
	svc   *Service
	Users auth.UserStore
}

// preferencesAPI builds them from the gateway's configuration.
func (h *HTTPGateway) preferencesAPI() *preferencesAPI {
	return &preferencesAPI{auditor: h.auditor(), svc: h.svc, Users: h.Users}
}

// Account preferences live under /me/preferences (authenticated; the
// caller acts on their own account). Two concerns share the surface:
//
//   - Operational notifications the user may turn off (flow-failure
//     email). Transactional/security mail (verification, password
//     reset) is NOT governed here — it always sends.
//   - Interface preferences that roam with the account (theme,
//     language) so they follow the user across devices instead of
//     living only in one browser's localStorage.
//
// PUT is a PARTIAL update: only the fields present in the body change.
// The three settings are edited from independent UI controls (the
// notification toggle, the theme picker, the language select), so a
// full-replace PUT would have each control clobber the others. Absent
// (nil) fields are left untouched; the response always echoes the full
// resolved state.

// prefsEmail resolves the user-store email for a principal: password
// users carry their email as the subject.
func prefsEmail(p core.Principal) string { return p.Subject }

// preferencesResponse is the GET body and the PUT echo — fully resolved
// values (the server flattens the notification tri-state, and reports
// theme/language as stored, "" meaning "no explicit choice").
type preferencesResponse struct {
	EmailOnFlowFailure  bool   `json:"email_on_flow_failure"`
	EmailOnSupportReply bool   `json:"email_on_support_reply"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
}

// preferencesUpdate is the PUT body. Pointers distinguish "field
// present, apply it" from "field absent, leave unchanged" — the partial
// semantics each independent UI control relies on.
type preferencesUpdate struct {
	EmailOnFlowFailure  *bool   `json:"email_on_flow_failure"`
	EmailOnSupportReply *bool   `json:"email_on_support_reply"`
	Theme               *string `json:"theme"`
	Language            *string `json:"language"`
}

// langPattern bounds the language code's shape without hardcoding the
// client's locale list: a 2-letter primary subtag with an optional
// region (e.g. "en", "sv", "pt-BR"). Empty is allowed separately and
// means "clear the choice / use browser detection".
var langPattern = regexp.MustCompile(`^[A-Za-z]{2}(-[A-Za-z]{2})?$`)

func responseFor(u auth.User) preferencesResponse {
	return preferencesResponse{
		EmailOnFlowFailure:  u.Notify.EmailOnFlowFailureEnabled(),
		EmailOnSupportReply: u.Notify.EmailOnSupportReplyEnabled(),
		Theme:               u.UI.Theme,
		Language:            u.UI.Language,
	}
}

// getPreferences is GET /api/v1/me/preferences — the Settings UI and the
// app-boot hydration both read this.
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
