// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

// Welcome email. Sent once, right after an account is created, on every
// signup path (self-serve, platform signup-invite, and the platform-admin
// bootstrap) because they all funnel through signUp. Best-effort and
// fire-and-forget: it never blocks or fails the signup.
//
// Independent of email verification: on a verification-active deployment
// a new user receives BOTH a "confirm your address" mail (the gate for
// inviting others) and this "you're in, here's how to start" mail. They
// serve different jobs. Welcome needs only a mailer; the call no-ops on
// deployments without one.

// sendWelcomeEmail greets a freshly-created account. Returns false (and
// logs) on any failure; callers ignore the result.
func (h *HTTPGateway) sendWelcomeEmail(r *http.Request, user auth.User) bool {
	if h.svc.Mailer == nil {
		return false
	}
	// Link to the app when we know its public URL; otherwise a
	// link-less welcome is still worth sending.
	m := maillang.For(h.mailLang(r.Context(), user.Email))
	var button *emailtheme.Button
	if base := strings.TrimRight(h.svc.PublicBaseURL, "/"); base != "" {
		button = &emailtheme.Button{Label: m.WelcomeButton, URL: base + "/welcome"}
	}
	content := emailtheme.Content{
		Subject:   m.WelcomeSubject,
		Preheader: m.WelcomePreheader,
		Eyebrow:   m.WelcomeEyebrow,
		Heading:   m.WelcomeHeading,
		Intro:     []string{m.WelcomeIntro1, m.WelcomeIntro2},
		Button:    button,
		Outro:     []string{m.WelcomeOutro},
		LogoURL:   emailLogoURL(h.svc.PublicBaseURL),
	}
	if err := h.svc.Mailer.SendThemed(r.Context(), user.Email, emailtheme.PlainText(content), content); err != nil {
		h.logger.Printf("welcome email for %s: send: %v", user.Email, err)
		return false
	}
	h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.welcome_sent", "")
	return true
}
