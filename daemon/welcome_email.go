package daemon

import (
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/auth"
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
	if base := strings.TrimRight(h.svc.PublicBaseURL, "/"); base != "" {
		cta = "Jump in and build your first flow:\n" + base + "/welcome\n\n"
	}
	body := "Welcome to Dazyflow! Your account is ready.\n\n" +
		cta +
		"Need a hand? Reply to this email or visit the docs from the app.\n\n" +
		"Happy automating."
	if err := h.svc.Mailer.Send(r.Context(), user.Email, "Welcome to Dazyflow", body); err != nil {
		h.logger.Printf("welcome email for %s: send: %v", user.Email, err)
		return false
	}
	h.auditAuth(r.Context(), r, user.Tenant, user.Email, "auth.welcome_sent", "")
	return true
}
