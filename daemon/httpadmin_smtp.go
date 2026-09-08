// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

func (h *orgAPI) smtpTest(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.svc.Mailer == nil {
		writeJSONError(rw, http.StatusNotImplemented,
			"transactional mailer not configured (set DAZYFLOW_SMTP_URL)")
		return
	}

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
