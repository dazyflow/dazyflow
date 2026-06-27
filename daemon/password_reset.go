// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
)

// Password reset. Active only where a transactional mailer AND a public
// base URL are configured (the link must be clickable from an inbox);
// elsewhere /auth/forgot-password still reports success but sends
// nothing, so the UI copy ("if an account exists, we've emailed a link")
// stays honest without revealing whether the deployment can even mail.
//
// The token rides on the user record as a SHA-256 hash + short expiry —
// no extra store — exactly like email verification (email_verification.go).
// Two security properties shape the flow:
//
//   - Non-enumerating: forgot-password ALWAYS returns 200 and reset
//     errors are uniform ("invalid or expired"), so neither endpoint
//     can be mined to learn which emails have accounts.
//   - Sign out everywhere: a successful reset revokes every existing
//     session for the account. A reset is the user's lever against a
//     thief who still holds a live cookie, so leaving old sessions alive
//     would defeat the point.

// resetTokenTTL is deliberately short — a reset link is a high-value
// credential, and a legitimate user acts on it within minutes. (Email
// verification's token lives 48h; a reset token should not.)
const resetTokenTTL = 1 * time.Hour

// passwordResetActive reports whether this deployment can run the flow.
func (h *HTTPGateway) passwordResetActive() bool {
	return h.svc.Mailer != nil && h.svc.PublicBaseURL != "" && h.Users != nil
}

// requestPasswordReset is POST /api/v1/auth/forgot-password {email}.
// Unauthenticated. ALWAYS 200 (non-enumerating): the response is
// identical whether or not the address has an account, whether or not a
// mailer is configured, and whether or not the send succeeds.
func (h *HTTPGateway) requestPasswordReset(rw http.ResponseWriter, r *http.Request) {
	body, ok := decodeRequestJSON[struct {
		Email string `json:"email"`
	}](rw, r)
	if !ok {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	// Do the real work only when the deployment can mail and the account
	// exists with a password — but never let either fact change the
	// response. An SSO-only account (no password hash) is skipped: this
	// is a reset, not a first-time set.
	if h.passwordResetActive() && email != "" {
		if user, err := h.Users.GetByEmail(r.Context(), email); err == nil && user.Email != "" && len(user.PasswordHash) > 0 {
			// Audit the request now (cheap, constant-ish), but send the email
			// OFF the request path. A synchronous SMTP send (seconds) only for
			// real accounts would make response time a timing oracle for
			// account existence — defeating the uniform 200 below, which is the
			// whole point of this endpoint. The detached context lets the send
			// outlive the handler; its timeout stops a hung mail server from
			// piling up goroutines (and the route is IP-rate-limited upstream).
			h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.password_reset_requested", "")
			go func(u auth.User) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				h.sendPasswordResetEmail(ctx, u)
			}(user)
		}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

// sendPasswordResetEmail mints a fresh token onto the user record
// (invalidating any earlier one) and emails the link. Best-effort: a
// failure logs and returns false; it never surfaces to the caller (see
// requestPasswordReset's uniform response). Runs on a detached context
// from a goroutine, so it must not touch the request. The token is
// stored (PutUser) BEFORE the email is sent, so a recipient can never
// receive a link whose token isn't yet valid.
func (h *HTTPGateway) sendPasswordResetEmail(ctx context.Context, user auth.User) bool {
	if !h.passwordResetActive() {
		return false
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.logger.Printf("password reset for %s: mint token: %v", user.Email, err)
		return false
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	exp := now.Add(resetTokenTTL)
	user.ResetTokenHash = hash[:]
	user.ResetExpiresAt = &exp
	if err := h.Users.PutUser(ctx, user); err != nil {
		h.logger.Printf("password reset for %s: save token: %v", user.Email, err)
		return false
	}
	link := strings.TrimRight(h.svc.PublicBaseURL, "/") + "/reset-password?email=" +
		url.QueryEscape(user.Email) + "&token=" + token
	expFmt := exp.Format("2 January 2006, 15:04 MST")
	body := fmt.Sprintf(
		"We received a request to reset your Dazyflow password.\n\n"+
			"Choose a new password:\n%s\n\n"+
			"The link expires %s. If you didn't request this, ignore this email — your password is unchanged.",
		link, expFmt)
	content := emailtheme.Content{
		Subject:   "Reset your Dazyflow password",
		Preheader: "Choose a new password for your account.",
		Eyebrow:   "Password reset",
		Heading:   "Reset your password",
		Intro:     []string{"We received a request to reset the password for your Dazyflow account."},
		Button:    &emailtheme.Button{Label: "Choose a new password", URL: link},
		Outro: []string{fmt.Sprintf(
			"This link expires %s. If you didn't request this, ignore this email — your password is unchanged.",
			expFmt)},
		LogoURL: emailLogoURL(h.svc.PublicBaseURL),
	}
	if err := h.svc.Mailer.SendThemed(ctx, user.Email, body, content); err != nil {
		h.logger.Printf("password reset for %s: send: %v", user.Email, err)
		return false
	}
	return true
}

// resetPassword is POST /api/v1/auth/reset-password {email, token,
// password}. Unauthenticated — the token is the proof. On success the
// password is replaced, the token is burned (single-use), and every
// existing session for the account is revoked. Errors are uniform so a
// probe can't tell a bad token from an unknown email.
func (h *HTTPGateway) resetPassword(rw http.ResponseWriter, r *http.Request) {
	if h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "users not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Email    string `json:"email"`
		Token    string `json:"token"`
		Password string `json:"password"`
	}](rw, r)
	if !ok {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || body.Token == "" {
		writeJSONError(rw, http.StatusBadRequest, "email and token required")
		return
	}
	// Validate the new password BEFORE touching the token, so a rejected
	// password (too short, etc.) doesn't burn the user's one-shot link.
	if err := validSignupPassword(body.Password); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	user, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		// Same shape as a bad token — don't confirm which addresses exist.
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired reset link")
		return
	}
	hash := sha256.Sum256([]byte(body.Token))
	if len(user.ResetTokenHash) == 0 ||
		subtle.ConstantTimeCompare(user.ResetTokenHash, hash[:]) != 1 ||
		user.ResetExpiresAt == nil || time.Now().After(*user.ResetExpiresAt) {
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired reset link")
		return
	}
	newHash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	user.PasswordHash = newHash
	user.ResetTokenHash = nil
	user.ResetExpiresAt = nil
	if err := h.Users.PutUser(r.Context(), user); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save password: %v", err))
		return
	}
	// Sign out everywhere. Best-effort: the password is already changed,
	// so a revoke failure doesn't undo the reset — it just leaves stale
	// sessions to lapse on their own TTL. A session store that can't
	// revoke by subject (none in this build) simply skips this.
	revoked := 0
	if rev, ok := h.Sessions.(auth.SessionRevoker); ok {
		if n, err := rev.RevokeSubjectSessions(r.Context(), user.Subject); err != nil {
			h.logger.Printf("password reset for %s: revoke sessions: %v", user.Email, err)
		} else {
			revoked = n
		}
	}
	h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.password_reset",
		fmt.Sprintf("sessions_revoked=%d", revoked))
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}
