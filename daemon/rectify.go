// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// Self-service rectification — Right to rectification (GDPR Art. 16):
// change your own password and change your own email. Display-name/profile
// rectification for an org already exists (PUT /api/v1/admin/org/profile);
// there is no per-user display-name field to edit.

const minPasswordLen = 8

// changePasswordHandler lets a signed-in user change their own password.
// It verifies the current password (so a hijacked session can't silently
// re-key the account) and revokes all of the subject's sessions on success,
// forcing a fresh sign-in everywhere — the standard post-change hygiene.
func (h *HTTPGateway) changePasswordHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password auth not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}](rw, r)
	if !ok {
		return
	}
	if len(body.NewPassword) < minPasswordLen {
		writeAPIError(rw, http.StatusBadRequest, "weak_password", "new password must be at least 8 characters")
		return
	}
	email := strings.ToLower(strings.TrimSpace(p.Subject))
	if _, err := auth.VerifyPassword(r.Context(), h.Users, email, body.CurrentPassword); err != nil {
		writeAPIError(rw, http.StatusUnauthorized, "bad_credentials", "current password is incorrect")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "unknown_user", "no such account")
		return
	}
	hash, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "hash_failed", "could not hash password")
		return
	}
	u.PasswordHash = hash
	if err := h.Users.PutUser(r.Context(), u); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if rev, ok := h.Sessions.(auth.SessionRevoker); ok {
		_, _ = rev.RevokeSubjectSessions(r.Context(), u.Subject)
	}
	h.audit(r.Context(), p, "account.password_change", email, "self-service password change")
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":   true,
		"note": "password changed; all sessions were signed out — sign in again",
	})
}

// changeEmailHandler performs a supervised email re-key. Email is the
// identity primary key (and a human principal's subject), so changing it
// isn't an UPDATE: it creates the row under the new address and re-points
// the records that referenced the old one — memberships and API keys —
// then revokes sessions (forcing a fresh sign-in under the new email) and
// deletes the old row. Best-effort per record with warnings, so a partial
// store outage degrades visibly rather than silently corrupting identity.
func (h *HTTPGateway) changeEmailHandler(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password auth not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		NewEmail string `json:"new_email"`
		Password string `json:"password"`
	}](rw, r)
	if !ok {
		return
	}
	oldEmail := strings.ToLower(strings.TrimSpace(p.Subject))
	newEmail := strings.ToLower(strings.TrimSpace(body.NewEmail))
	if !looksLikeEmail(newEmail) {
		writeAPIError(rw, http.StatusBadRequest, "bad_email", "new_email is not a valid email address")
		return
	}
	if newEmail == oldEmail {
		writeAPIError(rw, http.StatusBadRequest, "no_change", "new_email matches the current email")
		return
	}
	// Re-auth with the current password before re-keying identity.
	if _, err := auth.VerifyPassword(r.Context(), h.Users, oldEmail, body.Password); err != nil {
		writeAPIError(rw, http.StatusUnauthorized, "bad_credentials", "password is incorrect")
		return
	}
	// The target address must be free.
	if _, err := h.Users.GetByEmail(r.Context(), newEmail); err == nil {
		writeAPIError(rw, http.StatusConflict, "email_taken", "that email is already in use")
		return
	}
	del, ok := h.Users.(userDeleter)
	if !ok {
		writeAPIError(rw, http.StatusNotImplemented, "not_supported", "user store does not support re-keying")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), oldEmail)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "unknown_user", "no such account")
		return
	}

	var warnings []string
	// 1) Create the row under the new identity (Subject mirrors Email for
	//    human accounts, so it changes too). Crucially, DROP the old address's
	//    verification state: a verified account must not carry "verified" onto
	//    an unconfirmed new address, or email-verification gating could be
	//    bypassed by re-keying. The new address re-earns verified by confirming
	//    the link sent below.
	newUser := u
	newUser.Email = newEmail
	newUser.Subject = newEmail
	newUser.VerifiedAt = nil
	newUser.VerifyTokenHash = nil
	newUser.VerifyExpiresAt = nil
	if err := h.Users.PutUser(r.Context(), newUser); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "rekey_failed", "create new identity: "+err.Error())
		return
	}
	// Send a confirmation to the new address (best-effort; mints its own
	// token and persists it). No-op when verification isn't configured.
	verificationSent := h.sendVerificationEmail(r, newUser)
	// 2) Re-point memberships (keyed by email).
	if h.Memberships != nil {
		if ms, err := h.Memberships.ListByEmail(r.Context(), oldEmail); err == nil {
			for _, m := range ms {
				m.UserEmail = newEmail
				if err := h.Memberships.PutMembership(r.Context(), m); err != nil {
					warnings = append(warnings, "membership "+m.Tenant+": "+err.Error())
					continue
				}
				_ = h.Memberships.DeleteMembership(r.Context(), oldEmail, m.Tenant)
			}
		}
	}
	// 3) Re-point API keys (subject == old email). PutKey upserts by ID.
	if lister, ok := h.svc.AdminKeys.(subjectLister); ok && h.svc.AdminKeys != nil {
		if keys, err := lister.ListBySubject(r.Context(), oldEmail); err == nil {
			for _, k := range keys {
				k.Subject = newEmail
				if err := h.svc.AdminKeys.PutKey(r.Context(), k); err != nil {
					warnings = append(warnings, "api key "+k.ID+": "+err.Error())
				}
			}
		}
	}
	// 4) Revoke the old subject's sessions — the user signs in afresh
	//    under the new email.
	if rev, ok := h.Sessions.(auth.SessionRevoker); ok {
		_, _ = rev.RevokeSubjectSessions(r.Context(), oldEmail)
	}
	// 5) Remove the old identity row.
	if err := del.DeleteUser(r.Context(), oldEmail); err != nil {
		warnings = append(warnings, "delete old row: "+err.Error())
	}
	h.audit(r.Context(), p, "account.email_change", newEmail, "re-keyed from "+oldEmail)
	note := "email changed; sign in again with the new address"
	if verificationSent {
		note += ". Check your inbox to confirm the new address."
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":                true,
		"new_email":         newEmail,
		"note":              note,
		"verification_sent": verificationSent,
		"warnings":          warnings,
	})
}

// looksLikeEmail is a deliberately loose sanity check — a single @ with
// non-empty local and domain parts and no spaces. Full RFC validation is
// pointless here; the address is verified by use (sign-in), not by regex.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return !strings.Contains(s[at+1:], "@")
}
