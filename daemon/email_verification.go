// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
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
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/datenames"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
	"git.sr.ht/~klahr/dazyflow/internal/maillang"
)

// Email verification. Active only on deployments with a transactional
// mailer AND a public base URL (the link must be clickable from an
// inbox); everywhere else signup behaves exactly as before. The token
// rides on the user record as a SHA-256 hash + expiry — no extra store —
// and the clickable link carries email+token so verification needs one
// GetByEmail, not a token index.
//
// Gating is deliberately soft: runs, flows, and sign-in all work
// unverified (hard-gating would kill the try-it-now funnel). The one
// hard gate is INVITING OTHERS — an unverified signup must not be able
// to use the operator's mailer to spam invitation emails.

const verifyTokenTTL = 48 * time.Hour

// verificationActive reports whether this deployment can run the flow.
func (h *HTTPGateway) verificationActive() bool {
	return h.svc.Mailer != nil && h.svc.PublicBaseURL != "" && h.Users != nil
}

// sendVerificationEmail mints a fresh token onto the user record and
// emails the link. Best-effort: a failure logs and reports false; the
// account works regardless.
func (h *HTTPGateway) sendVerificationEmail(r *http.Request, user auth.User) bool {
	if !h.verificationActive() {
		return false
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.logger.Printf("verification email for %s: mint token: %v", user.Email, err)
		return false
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	exp := now.Add(verifyTokenTTL)
	user.VerifyTokenHash = hash[:]
	user.VerifyExpiresAt = &exp
	if err := h.Users.PutUser(r.Context(), user); err != nil {
		h.logger.Printf("verification email for %s: save token: %v", user.Email, err)
		return false
	}
	link := strings.TrimRight(h.svc.PublicBaseURL, "/") + "/verify-email?email=" +
		url.QueryEscape(user.Email) + "&token=" + token
	// Keep this strictly about confirming the address — the separate
	// welcome email (welcome_email.go) does the greeting. Leading both
	// with "Welcome to Dazyflow" made the pair read as one mail sent twice.
	// The account exists, so it can carry a language choice — though at signup
	// it usually has not been made yet, which reads as English.
	lang := h.mailLang(r.Context(), user.Email)
	m := maillang.For(lang)
	expFmt := datenames.FormatDate(exp, lang)
	content := emailtheme.Content{
		Subject:   m.VerifySubject,
		Preheader: m.VerifyPreheader,
		Eyebrow:   m.VerifyEyebrow,
		Heading:   m.VerifyHeading,
		Intro:     []string{m.VerifyIntro},
		Button:    &emailtheme.Button{Label: m.VerifyButton, URL: link},
		Outro:     []string{fmt.Sprintf(m.VerifyExpiry, expFmt)},
		LogoURL:   emailLogoURL(h.svc.PublicBaseURL),
	}
	if err := h.svc.Mailer.SendThemed(r.Context(), user.Email, emailtheme.PlainText(content), content); err != nil {
		h.logger.Printf("verification email for %s: send: %v", user.Email, err)
		return false
	}
	return true
}

// verifyEmail is POST /api/v1/auth/verify-email {email, token} — the
// landing page's call when the user clicks the link. Unauthenticated by
// nature (the click can come from any browser); the token is the proof.
// Idempotent: re-clicking a consumed link on a verified account is a
// success, not an error.
func (h *HTTPGateway) verifyEmail(rw http.ResponseWriter, r *http.Request) {
	if h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "users not configured")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Email string `json:"email"`
		Token string `json:"token"`
	}](rw, r)
	if !ok {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || body.Token == "" {
		writeJSONError(rw, http.StatusBadRequest, "email and token required")
		return
	}
	user, err := h.Users.GetByEmail(r.Context(), email)
	if err != nil {
		// Same shape as a bad token — don't confirm which addresses exist.
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired verification link")
		return
	}
	if user.EmailVerified() {
		writeJSON(rw, http.StatusOK, map[string]any{"verified": true})
		return
	}
	hash := sha256.Sum256([]byte(body.Token))
	if len(user.VerifyTokenHash) == 0 ||
		subtle.ConstantTimeCompare(user.VerifyTokenHash, hash[:]) != 1 ||
		user.VerifyExpiresAt == nil || time.Now().After(*user.VerifyExpiresAt) {
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired verification link")
		return
	}
	now := time.Now().UTC()
	user.VerifiedAt = &now
	user.VerifyTokenHash = nil
	user.VerifyExpiresAt = nil
	if err := h.Users.PutUser(r.Context(), user); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save verification: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.email_verified", "")
	writeJSON(rw, http.StatusOK, map[string]any{"verified": true})
}

// resendVerification is POST /api/v1/me/verification/resend — the
// banner's "resend" button. Mints a fresh token (invalidating the old
// one) and sends again.
func (h *HTTPGateway) resendVerification(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.verificationActive() {
		writeJSONError(rw, http.StatusNotImplemented, "email verification is not enabled on this deployment")
		return
	}
	user, err := h.Users.GetByEmail(r.Context(), p.Subject)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, "this credential has no email account to verify")
		return
	}
	if user.EmailVerified() {
		writeJSON(rw, http.StatusOK, map[string]any{"sent": false, "already_verified": true})
		return
	}
	sent := h.sendVerificationEmail(r, user)
	if !sent {
		writeJSONError(rw, http.StatusBadGateway, "could not send the verification email — try again or contact your administrator")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"sent": true})
}

// verificationStatus resolves the whoami fields for p: whether the
// account's email is verified and whether the UI should nag. API-key
// principals (no user record) count as verified — there's no inbox to
// confirm and no banner to show.
func (h *HTTPGateway) verificationStatus(r *http.Request, p core.Principal) (verified, pending bool) {
	if h.Users == nil || p.Subject == "" || !strings.Contains(p.Subject, "@") {
		return true, false
	}
	user, err := h.Users.GetByEmail(r.Context(), p.Subject)
	if err != nil {
		return true, false
	}
	verified = user.EmailVerified()
	return verified, h.verificationActive() && !verified
}

// inviterVerified reports whether the daemon should send mail on p's behalf:
// on verification-active deployments a session principal must have confirmed
// their own address first. API-key principals (no user record) pass — they
// were minted by someone already inside the org — as do deployments where
// verification can't run at all.
//
// This is the predicate behind both the hard gate below and createInvitation's
// softer one, so the two can never drift apart on who counts as trusted.
func (h *HTTPGateway) inviterVerified(r *http.Request, p core.Principal) bool {
	if !h.verificationActive() || !strings.Contains(p.Subject, "@") {
		return true
	}
	user, err := h.Users.GetByEmail(r.Context(), p.Subject)
	return err != nil || user.EmailVerified()
}

// requireVerifiedInviter is the hard gate — refuse the action outright.
// Returns false after writing the 403.
//
// Used where the thing being created is itself the abusable resource (an extra
// tenant), rather than a message we might simply decline to send. Inviting is
// deliberately NOT in that group any more: see createInvitation.
func (h *HTTPGateway) requireVerifiedInviter(rw http.ResponseWriter, r *http.Request, p core.Principal) bool {
	if h.inviterVerified(r, p) {
		return true
	}
	writeJSONError(rw, http.StatusForbidden,
		"verify your email before inviting others — check your inbox or resend from the banner")
	return false
}
