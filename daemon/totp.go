// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"golang.org/x/crypto/bcrypt"
)

// TOTP 2FA HTTP surface. Enrolment + management lives under /me/totp
// (authenticated, the caller acts on their own account); the login
// second-factor leg is POST /auth/totp (unauthenticated — the challenge
// token IS the principal at that point). All endpoints 503 when the
// install hasn't configured a TOTP key, so a client can detect the
// feature is off rather than half-failing midway through enrolment.

// totpConfigured reports whether 2FA is usable on this install: a valid
// 32-byte key AND a challenge store to bridge the login legs.
func (h *HTTPGateway) totpConfigured() bool {
	return len(h.TOTPKey) == 32 && h.TOTPChallenges != nil
}

// requireTOTP writes a 503 and returns false when 2FA isn't configured.
// Centralised so every endpoint reports the same posture.
func (h *HTTPGateway) requireTOTP(rw http.ResponseWriter) bool {
	if h.totpConfigured() {
		return true
	}
	writeAPIError(rw, http.StatusServiceUnavailable, "totp_not_configured",
		"two-factor authentication is not configured on this server")
	return false
}

// totpEmail resolves the user-store email for a principal. Password
// users carry their email as the subject (see httpsignup.go); anything
// else (e.g. an API-key principal) has no user record and can't enrol.
func totpEmail(p core.Principal) string { return p.Subject }

// totpStatus is GET /api/v1/me/totp — the Settings UI reads this to pick
// which card to render.
func (h *HTTPGateway) totpStatus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password auth not configured")
		return
	}
	st, err := auth.LoadTOTPStatus(r.Context(), h.Users, totpEmail(p))
	if errors.Is(err, auth.ErrUnknownUser) {
		// API-key / SSO principals have no password-user record; report
		// 2FA as simply off rather than an error.
		writeJSON(rw, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not read totp status")
		return
	}
	resp := map[string]any{
		"enabled":             st.Enabled,
		"recovery_codes_left": st.RecoveryCodesLeft,
	}
	if st.EnrolledAt != nil {
		resp["enrolled_at"] = st.EnrolledAt
	}
	writeJSON(rw, http.StatusOK, resp)
}

// totpSetup is POST /api/v1/me/totp/setup — mints a fresh secret, stores
// it pending (enabled=false), and returns provisioning data. Idempotent:
// calling again before confirm replaces the pending secret.
func (h *HTTPGateway) totpSetup(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requireTOTP(rw) {
		return
	}
	setup, err := auth.EnrolStart(r.Context(), h.Users, h.TOTPKey, totpEmail(p))
	switch {
	case errors.Is(err, auth.ErrTOTPAlreadyEnrolled):
		writeAPIError(rw, http.StatusConflict, "totp_already_enrolled",
			"two-factor is already enabled; disable it first to re-enrol")
		return
	case errors.Is(err, auth.ErrUnknownUser):
		writeAPIError(rw, http.StatusBadRequest, "no_user", "no password account for this principal")
		return
	case err != nil:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not start enrolment")
		return
	}
	h.audit(r.Context(), p, "totp.setup", p.Subject, "")
	writeJSON(rw, http.StatusOK, map[string]any{
		"otp_auth_url":    setup.OTPAuthURL,
		"secret_base32":   setup.SecretBase32,
		"qr_png_data_url": setup.QRPNGDataURL,
	})
}

// totpConfirm is POST /api/v1/me/totp/confirm — validates the first code
// against the pending secret, enables 2FA, and returns the recovery
// codes ONCE.
func (h *HTTPGateway) totpConfirm(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requireTOTP(rw) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return
	}
	if body.Code == "" {
		writeAPIError(rw, http.StatusBadRequest, "code_required", "code is required")
		return
	}
	codes, err := auth.EnrolConfirm(r.Context(), h.Users, h.TOTPKey, totpEmail(p), body.Code)
	switch {
	case errors.Is(err, auth.ErrTOTPAlreadyEnrolled):
		writeAPIError(rw, http.StatusConflict, "totp_already_enrolled", "two-factor is already enabled")
		return
	case errors.Is(err, auth.ErrTOTPInvalid):
		writeAPIError(rw, http.StatusUnauthorized, "totp_invalid", "invalid code")
		return
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		writeAPIError(rw, http.StatusBadRequest, "no_pending_enrolment", "no pending enrolment; call /me/totp/setup first")
		return
	case errors.Is(err, auth.ErrTOTPSecretCorrupt):
		writeAPIError(rw, http.StatusServiceUnavailable, "totp_config_error", "totp configuration error")
		return
	case err != nil:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not confirm enrolment")
		return
	}
	h.audit(r.Context(), p, "totp.enable", p.Subject, "")
	writeJSON(rw, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// totpDisable is POST /api/v1/me/totp/disable — clears 2FA. Re-auth
// gate: the caller must pass their current password, so a stolen session
// cookie alone can't turn 2FA off.
func (h *HTTPGateway) totpDisable(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requireTOTP(rw) {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return
	}
	if body.Password == "" {
		writeAPIError(rw, http.StatusBadRequest, "password_required", "current password is required")
		return
	}
	u, err := h.Users.GetByEmail(r.Context(), totpEmail(p))
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "no_user", "no password account for this principal")
		return
	}
	if bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(body.Password)) != nil {
		writeAPIError(rw, http.StatusUnauthorized, "bad_password", "current password is incorrect")
		return
	}
	if err := auth.DisableTOTP(r.Context(), h.Users, totpEmail(p)); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not disable totp")
		return
	}
	h.audit(r.Context(), p, "totp.disable", p.Subject, "")
	rw.WriteHeader(http.StatusNoContent)
}

// totpRegenerate is POST /api/v1/me/totp/recovery-codes — drops every
// existing recovery code and mints a fresh set, returned once.
func (h *HTTPGateway) totpRegenerate(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.requireTOTP(rw) {
		return
	}
	codes, err := auth.RegenerateRecoveryCodes(r.Context(), h.Users, totpEmail(p))
	switch {
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		writeAPIError(rw, http.StatusBadRequest, "totp_not_enrolled", "two-factor is not enabled")
		return
	case errors.Is(err, auth.ErrUnknownUser):
		writeAPIError(rw, http.StatusBadRequest, "no_user", "no password account for this principal")
		return
	case err != nil:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not regenerate codes")
		return
	}
	h.audit(r.Context(), p, "totp.recovery_codes.regenerate", p.Subject, "")
	writeJSON(rw, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// totpVerify is POST /api/v1/auth/totp — leg 2 of sign-in. It consumes
// the challenge from leg 1 plus a TOTP code (or a recovery code), then
// mints the session exactly like signIn so the client treats both
// endpoints' success responses identically. Unauthenticated by design —
// the challenge token is the principal here — and rate-limited by the
// caller (rateLimitAuth) to bound a brute-force pass against the 6-digit
// space.
func (h *HTTPGateway) totpVerify(rw http.ResponseWriter, r *http.Request) {
	if !h.requireTOTP(rw) {
		return
	}
	if h.Sessions == nil || h.Users == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "password sign-in not configured")
		return
	}
	var body struct {
		Challenge    string `json:"challenge"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return
	}
	if body.Challenge == "" {
		writeAPIError(rw, http.StatusBadRequest, "challenge_required", "challenge is required")
		return
	}
	if body.Code == "" && body.RecoveryCode == "" {
		writeAPIError(rw, http.StatusBadRequest, "code_required", "code or recovery_code is required")
		return
	}
	res, err := auth.ConsumeTOTPChallenge(r.Context(), h.TOTPChallenges, h.Users, h.TOTPKey,
		body.Challenge, body.Code, body.RecoveryCode)
	switch {
	case errors.Is(err, auth.ErrChallengeUnknown):
		writeAPIError(rw, http.StatusBadRequest, "challenge_invalid", "challenge is invalid")
		return
	case errors.Is(err, auth.ErrChallengeExpired):
		writeAPIError(rw, http.StatusBadRequest, "challenge_expired", "challenge has expired — sign in again")
		return
	case errors.Is(err, auth.ErrTOTPInvalid):
		// The challenge is consumed inside ConsumeTOTPChallenge, so the
		// email isn't returned on the failure path — the actor is left
		// blank and the source IP (added by auditAuth) carries the
		// brute-force signal.
		h.auditAuth(r.Context(), r, "", "", "auth.signin_failed", "stage=mfa method=totp")
		writeAPIError(rw, http.StatusUnauthorized, "totp_invalid", "invalid code")
		return
	case errors.Is(err, auth.ErrRecoveryCodeInvalid):
		h.auditAuth(r.Context(), r, "", "", "auth.signin_failed", "stage=mfa method=recovery_code")
		writeAPIError(rw, http.StatusUnauthorized, "recovery_code_invalid", "invalid recovery code")
		return
	case errors.Is(err, auth.ErrTOTPNotEnrolled):
		// Race: user disabled TOTP between the two legs. Don't leak the
		// new state — surface it as a stale challenge.
		writeAPIError(rw, http.StatusBadRequest, "challenge_invalid", "challenge is invalid")
		return
	case errors.Is(err, auth.ErrTOTPSecretCorrupt):
		writeAPIError(rw, http.StatusServiceUnavailable, "totp_config_error", "totp configuration error")
		return
	case err != nil:
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "totp verify failed")
		return
	}

	// Lockout check, mirroring the password leg: a suspension that landed
	// between the two sign-in steps must still bar the session.
	if msg, locked := h.signInLockout(r.Context(), res.User); locked {
		h.auditAuth(r.Context(), r, res.User.Tenant, res.User.Email, "auth.signin_suspended", "stage=mfa")
		writeAPIError(rw, http.StatusForbidden, "suspended", msg)
		return
	}
	ttl := h.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevatePlatformAdmin(r.Context(), res.User), ttl)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", "could not create session")
		return
	}
	mfaMethod := "totp"
	if body.Code == "" && body.RecoveryCode != "" {
		mfaMethod = "recovery_code"
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signin", "method="+mfaMethod)
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
	writeJSON(rw, http.StatusOK, map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"tenant":     sess.Tenant,
		"workspace":  sess.Workspace,
		"expires_at": sess.ExpiresAt,
	})
}
