// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
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
	var cta string
	var button *emailtheme.Button
	if base := strings.TrimRight(h.svc.PublicBaseURL, "/"); base != "" {
		cta = "Jump in and build your first flow:\n" + base + "/welcome\n\n"
		button = &emailtheme.Button{Label: "Build your first flow", URL: base + "/welcome"}
	}
	body := "Welcome to Dazyflow! Your account is ready.\n\n" +
		cta +
		"Need a hand? Reply to this email or visit the docs from the app.\n\n" +
		"Happy automating."
	content := emailtheme.Content{
		Subject:   "Welcome to Dazyflow",
		Preheader: "Your account is ready — build your first flow.",
		Eyebrow:   "Welcome",
		Heading:   "Your account is ready",
		Intro: []string{
			"Welcome to Dazyflow! Everything's set up and waiting for you.",
			"A flow automates a task for you — on a schedule, when a form is submitted, or when another app sends it data. Start from a template or describe what you want in plain words.",
		},
		Button:  button,
		Outro:   []string{"Need a hand? Just reply to this email, or open the docs from inside the app."},
		LogoURL: emailLogoURL(h.svc.PublicBaseURL),
	}
	if err := h.svc.Mailer.SendThemed(r.Context(), user.Email, body, content); err != nil {
		h.logger.Printf("welcome email for %s: send: %v", user.Email, err)
		return false
	}
	h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.welcome_sent", "")
	return true
}
