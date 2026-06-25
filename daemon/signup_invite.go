package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
)

// Platform signup-invites. On a deployment with self-serve signup
// disabled (EnableSignup=false), a platform owner still needs a way to
// onboard specific people without flipping signup on for the whole
// internet. A signup-invite is exactly that: the owner names an email,
// the daemon emails that address a /signup link with the email
// pre-filled, and the recipient just sets a password. The resulting
// account is an ordinary self-serve account — its OWN tenant and the
// default signup roles — not a membership in the owner's org (that's
// what the org-invite flow in orgs.go is for).
//
// Storage piggybacks on the invitations store via the SignupInviteTenant
// sentinel (see auth/invitation.go), so signup-invites inherit its TTL,
// audit, and GDPR erasure. The signUp gate (httpsignup.go) is what
// actually consumes the token; these handlers only mint, list, and
// revoke them.

const signupInviteTTL = 14 * 24 * time.Hour

// validSignupInvite reports whether token is a pending platform
// signup-invite addressed to email. It's the signUp gate's check — a
// true result is the third way past a disabled-signup deployment.
// Empty token, no invitations store, unknown/expired/used token, or an
// email mismatch all return false. The email match is case-insensitive
// and binds the account to the invited address: a recipient can't edit
// the readonly form field (or forge the request) to claim a different
// email than the one the owner invited.
func (h *HTTPGateway) validSignupInvite(ctx context.Context, email, token string) bool {
	if token == "" || h.Invitations == nil {
		return false
	}
	inv, err := h.Invitations.GetByToken(ctx, token)
	if err != nil {
		return false
	}
	return inv.IsSignupInvite() &&
		inv.IsPending(time.Now().UTC()) &&
		strings.EqualFold(inv.Email, strings.ToLower(strings.TrimSpace(email)))
}

// signupInviteURL builds the clickable link mailed to the recipient:
// the /signup page with the email pre-filled and the token attached.
// SignUp.tsx reads signup_invite to render the email field readonly,
// skip the invite-only bounce, and post the token back through signUp.
// Falls back to a path-only URL when no public base URL is configured
// (useless in an inbox, but the create response still returns it for
// copy/paste, and the UI rewrites it against window.origin).
func (h *HTTPGateway) signupInviteURL(email, token string) string {
	q := "/signup?email=" + url.QueryEscape(email) + "&signup_invite=" + url.QueryEscape(token)
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		return q
	}
	return base + q
}

// createSignupInvite handler: POST /api/v1/admin/signup-invites {email}.
// Platform-admin only. Mints a pending signup-invite for the email and,
// when a mailer is wired, sends the link. The response always carries
// the link so the owner can copy/paste it regardless.
func (h *HTTPGateway) createSignupInvite(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil || h.Users == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if err := requirePlatformAdmin(p); err != nil {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	// Anti-abuse: on verification-active deployments the inviter's own
	// email must be confirmed before the daemon sends mail on their
	// behalf. Mirrors createInvitation.
	if !h.requireVerifiedInviter(rw, r, p) {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if err := validSignupEmail(email); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// No point inviting someone who already has an account — the signUp
	// gate would reject them with a 409 anyway. Tell the owner now.
	if existing, err := h.Users.GetByEmail(r.Context(), email); err == nil && existing.Email != "" {
		writeJSONError(rw, http.StatusConflict, "an account with that email already exists")
		return
	}
	token, err := auth.MintInvitationToken()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	inv := auth.Invitation{
		Token:     token,
		Email:     email,
		Tenant:    auth.SignupInviteTenant,
		InvitedBy: p.Subject,
		CreatedAt: now,
		ExpiresAt: now.Add(signupInviteTTL),
	}
	if err := h.Invitations.PutInvitation(r.Context(), inv); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "signup_invite.create", token, "email="+email)
	signupURL := h.signupInviteURL(email, token)
	emailSent := false
	if h.svc.Mailer != nil && strings.HasPrefix(signupURL, "http") {
		expFmt := inv.ExpiresAt.Format("2 January 2006")
		msg := fmt.Sprintf(
			"You've been invited to create an account on Dazyflow.\n\n"+
				"Set your password and finish signing up:\n%s\n\n"+
				"The link expires %s. If you weren't expecting this, ignore this email.",
			signupURL, expFmt)
		content := emailtheme.Content{
			Subject:    "You're invited to Dazyflow",
			Preheader:  "Create your account to get started.",
			Eyebrow:    "Invitation",
			Heading:    "Create your Dazyflow account",
			Intro:      []string{"You've been invited to create an account on Dazyflow. Set a password and you're in."},
			Button:     &emailtheme.Button{Label: "Set your password", URL: signupURL},
			Outro: []string{fmt.Sprintf(
				"This link expires %s. If you weren't expecting it, you can ignore this email.",
				expFmt)},
			FooterNote: "You're receiving this because someone invited you to Dazyflow.",
			LogoURL:    emailLogoURL(h.svc.PublicBaseURL),
		}
		if err := h.svc.Mailer.SendThemed(r.Context(), email, msg, content); err != nil {
			h.logger.Printf("signup-invite email to %s: %v", email, err)
		} else {
			emailSent = true
		}
	}
	writeJSON(rw, http.StatusCreated, map[string]any{
		"token":      token,
		"email":      email,
		"signup_url": signupURL,
		"expires_at": inv.ExpiresAt,
		"email_sent": emailSent,
	})
}

// listSignupInvites handler: GET /api/v1/admin/signup-invites. Platform-
// admin only. Returns the deployment's pending + recently-resolved
// signup-invites so the owner can see who's been invited and re-share or
// revoke a link.
func (h *HTTPGateway) listSignupInvites(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if err := requirePlatformAdmin(p); err != nil {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	rows, err := h.Invitations.ListByTenant(r.Context(), auth.SignupInviteTenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	type dto struct {
		Token      string     `json:"token"`
		Email      string     `json:"email"`
		InvitedBy  string     `json:"invited_by"`
		CreatedAt  time.Time  `json:"created_at"`
		ExpiresAt  time.Time  `json:"expires_at"`
		AcceptedAt *time.Time `json:"accepted_at,omitempty"`
		RevokedAt  *time.Time `json:"revoked_at,omitempty"`
		Pending    bool       `json:"pending"`
		SignupURL  string     `json:"signup_url"`
	}
	out := make([]dto, 0, len(rows))
	for _, i := range rows {
		out = append(out, dto{
			Token:      i.Token,
			Email:      i.Email,
			InvitedBy:  i.InvitedBy,
			CreatedAt:  i.CreatedAt,
			ExpiresAt:  i.ExpiresAt,
			AcceptedAt: i.AcceptedAt,
			RevokedAt:  i.RevokedAt,
			Pending:    i.IsPending(now),
			SignupURL:  h.signupInviteURL(i.Email, i.Token),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"invites": out})
}

// revokeSignupInvite handler: DELETE /api/v1/admin/signup-invites/{token}.
// Platform-admin only. Refuses tokens that aren't signup-invites so an
// org-invite can't be revoked through this surface.
func (h *HTTPGateway) revokeSignupInvite(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Invitations == nil {
		writeJSONError(rw, http.StatusNotImplemented, "invitations not configured")
		return
	}
	if err := requirePlatformAdmin(p); err != nil {
		writeJSONError(rw, http.StatusForbidden, "platform:admin required")
		return
	}
	token := r.PathValue("token")
	inv, err := h.Invitations.GetByToken(r.Context(), token)
	if err != nil || !inv.IsSignupInvite() {
		writeJSONError(rw, http.StatusNotFound, "signup-invite not found")
		return
	}
	if err := h.Invitations.MarkRevoked(r.Context(), token, time.Now().UTC()); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "signup_invite.revoke", token, "email="+inv.Email)
	rw.WriteHeader(http.StatusNoContent)
}
