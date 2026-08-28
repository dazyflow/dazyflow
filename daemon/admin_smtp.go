// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// smtpTest is POST /api/v1/admin/smtp-test {to?}. It sends one throwaway
// message through the platform Mailer so an operator can confirm
// DAZYFLOW_SMTP_URL / DAZYFLOW_SMTP_FROM actually deliver — boot only
// validates that the URL parses, not that the server accepts mail.
//
// Platform-admin only (the mailer is instance-wide infrastructure). The
// recipient defaults to the caller's own address, so the usual click is
// a zero-input "send me a test". A failure returns the real SMTP error
// (auth/TLS/dial) verbatim — that diagnostic is the whole point.
func (h *HTTPGateway) smtpTest(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.svc.Mailer == nil {
		writeJSONError(rw, http.StatusNotImplemented,
			"transactional mailer not configured (set DAZYFLOW_SMTP_URL)")
		return
	}

	// Body is optional — an empty POST means "send to me".
	var body struct {
		To string `json:"to"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	to := strings.TrimSpace(body.To)
	if to == "" {
		to = p.Subject
	}
	if !strings.Contains(to, "@") || strings.ContainsAny(to, " \r\n") {
		writeJSONError(rw, http.StatusBadRequest, "a valid recipient address is required")
		return
	}

	subject := "Dazyflow SMTP test"
	msg := fmt.Sprintf(
		"This is a test message from your Dazyflow instance.\n\n"+
			"If you're reading it, platform email is configured correctly:\n"+
			"invitation links and failure notifications will be delivered.\n\n"+
			"Sent from %s at the request of %s.",
		h.svc.Mailer.From, p.Subject)

	if err := h.svc.Mailer.Send(r.Context(), to, subject, msg); err != nil {
		h.audit(r.Context(), p, "smtp.test", to, "error="+err.Error())
		// 502: the daemon is fine; the upstream SMTP server rejected us.
		writeJSONError(rw, http.StatusBadGateway, fmt.Sprintf("send failed: %v", err))
		return
	}
	h.audit(r.Context(), p, "smtp.test", to, "ok")
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":   true,
		"to":   to,
		"from": h.svc.Mailer.From,
	})
}
