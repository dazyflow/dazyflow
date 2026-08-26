// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package notify houses notification modules — channels a graph can use
// to report on its own outcome (email today; chat/Slack would slot in
// alongside).
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/mailmsg"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/smtputil"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "email_send",
			Version:     "1.0",
			Label:       "Email",
			Subtitle:    "Send email",
			Color:       "#2dd4bf",
			Icon:        "mail",
			Category:    "network",
			Provider:    "internal",
			Integration: "Email",
			Tags:        []string{"email", "smtp", "notify", "report"},
			Summary:     "Send an email through any mail server (SMTP) — daily summaries, alerts, or a build's output straight into someone's inbox.",
			Description: "Send an email through your own mail server (SMTP). To, Subject and Body can be typed on the step or connected from an earlier step (the matching input port overrides the param) — handy for per-recipient sends or mailing another step's output. Attach files by connecting file-producing steps (e.g. Export Sheet as PDF) into the variadic 'attachments' input. Configure the mail server (host, security, login, sender) once on the Email integration page.",
			Examples: []core.ParamsExample{
				{
					Title:  "Daily report",
					Params: json.RawMessage(`{"to":"team@example.com","subject":"Daily sales report","body":"See attached."}`),
					Notes:  "The mail server and sender are configured once on the Email integration page, not on the step.",
				},
				{
					Title:  "Alert to multiple recipients",
					Params: json.RawMessage(`{"to":"oncall@example.com,cto@example.com","subject":"Alert: error rate above threshold"}`),
					Notes:  "Body left empty here so it can be connected in from an earlier step.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			// The mail server (host/port/security/login/sender) is a per-tenant
			// ConnectionFields bundle configured once on the integration page,
			// not typed on every node — the engine injects it into each node's
			// params at run time (injectConnectionDefaults), exactly like Home
			// Assistant's URL+token. So flows carry only the per-message fields.
			ConnectionFields: []core.ConnectionField{
				{Key: "host", Label: "Mail server", Required: true, Placeholder: "smtp.example.com"},
				{Key: "port", Label: "Port", Placeholder: "587 (STARTTLS) or 465 (SSL/TLS)"},
				{Key: "tls", Label: "Connection security", Placeholder: "starttls, implicit, or none"},
				{Key: "username", Label: "Username", Placeholder: "usually your email address"},
				{Key: "password", Label: "Password", Secret: true, Help: "Your mail server password, or an app password if the provider issues one."},
				{Key: "from", Label: "From address", Required: true, Placeholder: "reports@example.com", Help: `The sender recipients see. Add a display name with "Reports <reports@example.com>" — most providers require the address itself to match your login.`},
			},
			Inputs: []core.Port{
				// Named after their params so the card shows inline editable
				// boxes (Unreal-style); a wired value overrides the typed one.
				// To takes comma-separated addresses, so it wires from any
				// string output (e.g. a sheet's Email column). Subject is
				// optional (defaults to "(no subject)").
				{Port: "to", Label: "To", Required: true, MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body", MIME: []string{"text/plain"}},
				{Port: "attachments", Label: "Attachments", Variadic: true},
			},
			// No declared outputs: sending an email is a "do" step — "after it
			// sends, do X" chains through the pass-through pin, which fires on
			// success. The delivery details are still EMITTED under "meta"
			// (see the Execute result) so run records keep them for debugging;
			// they're just not a pin (same as gmail send / ntfy).
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			// Only the per-message fields are params now; the server connection
			// lives in ConnectionFields above and is injected at run time.
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"to":{"type":"string","title":"To","description":"Recipient(s), comma-separated. Overridden by the 'To' input."},
						"cc":{"type":"string","title":"CC","description":"Carbon-copy recipient(s), comma-separated. Everyone on the email sees who's CC'd."},
						"bcc":{"type":"string","title":"BCC","description":"Blind-copy recipient(s), comma-separated. Hidden from the other recipients."},
						"subject":{"type":"string","title":"Subject","description":"The email's subject line — e.g. \"Re: your submission\". Leave blank and it sends as \"(no subject)\". Overridden by the 'Subject' input."},
						"body":{"type":"string","title":"Body","format":"multiline","description":"Email body text. Overridden by the 'Body' input."},
						"format":{"type":"string","title":"Body format","enum":["text","html"],"enumNames":["Text","HTML"],"default":"html","description":"How the body is sent. HTML renders formatting and links; Text sends it exactly as typed."},
						"template":{"type":"string","title":"Template","format":"email-template","description":"Optional reusable HTML template to wrap the body in (logo, header, footer). HTML format only. Leave blank to send the body as-is."}
					},
					"required":["to"]
				}`,
			),
			Idempotent: false,
			// SMTP send has no idempotency mechanism, so a retried send
			// delivers the email twice. This drop is a terminal leaf the
			// engine auto-retries on backoff, so retries must be off here.
			RetryPolicy: core.RetryNever,
			// A non-idempotent external write: opt into engine-side dedupe so an
			// expired-lease reclaim or crash recovery replays the recorded result
			// instead of firing the write a second time. Matches the other
			// send-style drops (discord/gmail/sheets/twilio/klarna/nshift/elks).
			DedupeWrites: true,
		},
		Execute: executeEmail,
	})
}

// emailTextInputOr returns the text wired into input port `port` (string or
// raw bytes), or `fallback` when the port is unwired/empty. ok is false only
// when the port carries a NON-text value — a wiring mistake the caller
// rejects. Lets To and Subject each be supplied by an upstream wire or a
// param (same pattern as gmail send).
func emailTextInputOr(job core.Job, port, fallback string) (val string, ok bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	}
	return "", false
}

// splitRecipients turns comma-separated addresses from the To input into the
// recipient list, dropping empties so trailing commas don't break the send.
func splitRecipients(s string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ",") {
		if addr := strings.TrimSpace(part); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

// smtpPort resolves the mail-server port. ConnectionFields inject it as a
// string ("587"); older flows may carry it as a JSON number. Try the string
// form first, fall back to the numeric form, then to 587 (STARTTLS default).
func smtpPort(job core.Job) int {
	if s := strings.TrimSpace(params.StringDefault(job.Params, "port", "")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return params.IntDefault(job.Params, "port", 587)
}

func executeEmail(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// host/port/security/login/sender come from the per-tenant connection the
	// engine injected into params (ConnectionFields). An empty host means the
	// tenant hasn't connected Email yet — say so, pointing at the right page.
	host := strings.TrimSpace(params.StringDefault(job.Params, "host", ""))
	if host == "" {
		return params.Err(job, "not_connected", "Email isn't connected — set up your mail server on the Email integration page"), nil
	}
	// From defaults to the username — the SMTP login is usually the sender
	// address, and most providers require the two to match anyway.
	from := strings.TrimSpace(params.StringDefault(job.Params, "from", ""))
	if from == "" {
		from = strings.TrimSpace(params.StringDefault(job.Params, "username", ""))
	}
	if from == "" {
		return params.Err(job, "not_connected", "no sender address — set the From address on the Email integration page"), nil
	}

	// to / subject / body each take their value from the matching input port
	// when one is wired, otherwise from the param (the "input overrides param"
	// pattern). A non-text value wired into To or Subject is a mistake we
	// reject.
	// To is a comma-separated string param; older flows may have stored it as a
	// JSON array, so fall back to StringSlice when the string form is empty.
	to := splitRecipients(params.StringDefault(job.Params, "to", ""))
	if len(to) == 0 {
		to = params.StringSlice(job.Params, "to")
	}
	if wired, ok := emailTextInputOr(job, "to", ""); !ok {
		return params.Err(job, "bad_input", "'To' input must be text"), nil
	} else if wired != "" {
		to = splitRecipients(wired)
	}
	if len(to) == 0 {
		return params.Err(job, "bad_param", "'to' is required — set it or connect the 'To' input"), nil
	}

	// CC and BCC are param-only (comma-separated). CC rides a visible header;
	// BCC must NOT appear in any header (or it isn't blind) — it's added to the
	// SMTP envelope recipients only, below.
	cc := splitRecipients(params.StringDefault(job.Params, "cc", ""))
	bcc := splitRecipients(params.StringDefault(job.Params, "bcc", ""))

	// Subject is optional — minimal friction for non-tech authors; To is the
	// only per-send hard requirement (same as gmail send).
	subject, ok := emailTextInputOr(job, "subject", params.StringDefault(job.Params, "subject", "(no subject)"))
	if !ok {
		return params.Err(job, "bad_input", "'Subject' input must be text"), nil
	}

	port := smtpPort(job)
	tlsMode := params.StringDefault(job.Params, "tls", "starttls")
	username := params.StringDefault(job.Params, "username", "")
	password := params.StringDefault(job.Params, "password", "")

	body := params.StringDefault(job.Params, "body", "")
	if input, ok := job.Input["body"]; ok {
		switch v := input.Inline.(type) {
		case string:
			if v != "" {
				body = v
			}
		case []byte:
			if len(v) > 0 {
				body = string(v)
			}
		case nil:
			// fall through to params.body
		default:
			raw, mErr := json.MarshalIndent(v, "", "  ")
			if mErr != nil {
				return params.Err(job, "bad_input", mErr.Error()), nil
			}
			body = string(raw)
		}
	}

	// Body format: HTML by default (matches the schema and gmail send), so an
	// unset format renders markup; "text" sends the body verbatim as plain text.
	bodyContentType := `text/html; charset="utf-8"`
	isHTML := params.StringDefault(job.Params, "format", "html") != "text"
	if !isHTML {
		bodyContentType = `text/plain; charset="utf-8"`
	}

	// Wrap the body in the referenced email template (HTML sends only). A
	// missing/unresolvable template fails the node rather than sending unwrapped.
	if isHTML {
		wrapped, werr := mailmsg.WrapWithTemplate(ctx, job, body, subject)
		if werr != nil {
			return params.Err(job, "email_template", werr.Error()), nil
		}
		body = wrapped
	}

	atts, jerr := mailmsg.LoadAttachments(job)
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	// SSRF guard: the SMTP host is a tenant-supplied param, so refuse
	// private/loopback/link-local targets (internal services, metadata)
	// unless the operator opted into private egress.
	if err := hfnet.CheckDialHost(addr); err != nil {
		return params.Err(job, "ssrf_blocked", err.Error()), nil
	}
	// The configured sender may carry a display name; the header takes that
	// form, the SMTP envelope only the bare address (smtputil.SplitSender).
	fromHeader, fromAddr := smtputil.SplitSender(from)
	msg := buildMessage(fromHeader, to, cc, subject, body, bodyContentType, atts)

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	// Every recipient — To, CC and BCC — must be in the SMTP envelope (RCPT
	// TO), since that, not the headers, decides who the server delivers to.
	// BCC is here but absent from the headers, which is what keeps it blind.
	rcpts := make([]string, 0, len(to)+len(cc)+len(bcc))
	rcpts = append(rcpts, to...)
	rcpts = append(rcpts, cc...)
	rcpts = append(rcpts, bcc...)

	emitProgress(progress, job, 0.3, "dial "+addr)
	if err := smtputil.Send(ctx, addr, host, tlsMode, auth, fromAddr, rcpts, msg); err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	emitProgress(progress, job, 1.0, "delivered")

	meta := map[string]any{
		"host": host,
		"port": port,
		"from": fromHeader,
		"to":   to,
		"cc":   cc,
		// BCC is blind by design: it rides the SMTP envelope only and is
		// stripped from the headers. Surfacing the addresses in the persisted,
		// downstream-wireable meta would defeat that — expose only the count.
		"bcc_count":  len(bcc),
		"subject":    subject,
		"bytes_sent": len(msg),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// buildMessage assembles the RFC 822 message. fromHeader is the header form of
// the sender, so it may carry a display name ("Reports <r@example.com>"); the
// bare-address envelope form goes to smtputil.Send separately. Address headers
// are stripped of CR/LF to defeat header injection; the subject is MIME-word
// encoded; multipart/mixed is used only when attachments exist (same shape as
// gmail send's buildRFC822). BCC is deliberately NOT a header — blind copies
// ride the SMTP envelope only (see executeEmail), so they stay hidden.
func buildMessage(fromHeader string, to, cc []string, subject, body, bodyContentType string, atts []mailmsg.Attachment) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", mailmsg.StripCRLF(fromHeader))
	fmt.Fprintf(&sb, "To: %s\r\n", mailmsg.StripCRLF(strings.Join(to, ", ")))
	if len(cc) > 0 {
		fmt.Fprintf(&sb, "Cc: %s\r\n", mailmsg.StripCRLF(strings.Join(cc, ", ")))
	}
	// RFC 2047 encoded-word — non-ASCII subjects must not ride as raw
	// UTF-8 bytes in a header, or receiving clients mojibake them.
	fmt.Fprintf(&sb, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	sb.WriteString("MIME-Version: 1.0\r\n")

	if len(atts) == 0 {
		sb.WriteString("Content-Type: " + bodyContentType + "\r\n")
		sb.WriteString("\r\n")
		sb.WriteString(body)
		return []byte(sb.String())
	}

	boundary := "dazyflow-" + mailmsg.RandomHex(16)
	sb.WriteString(`Content-Type: multipart/mixed; boundary="` + boundary + `"` + "\r\n\r\n")
	sb.WriteString("--" + boundary + "\r\n")
	sb.WriteString("Content-Type: " + bodyContentType + "\r\n")
	sb.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	sb.WriteString(body + "\r\n")
	mailmsg.WriteAttachmentParts(&sb, boundary, atts)
	return []byte(sb.String())
}
