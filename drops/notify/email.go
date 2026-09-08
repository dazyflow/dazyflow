// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/mailmsg"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/smtputil"
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
			ConnectionFields: []core.ConnectionField{
				{Key: "host", Label: "Mail server", Required: true, Placeholder: "smtp.example.com"},
				{Key: "port", Label: "Port", Placeholder: "587 (STARTTLS) or 465 (SSL/TLS)"},
				{Key: "tls", Label: "Connection security", Placeholder: "starttls, implicit, or none"},
				{Key: "username", Label: "Username", Placeholder: "usually your email address"},
				{Key: "password", Label: "Password", Secret: true, Help: "Your mail server password, or an app password if the provider issues one."},
				{Key: "from", Label: "From address", Required: true, Placeholder: "reports@example.com", Help: `The sender recipients see. Add a display name with "Reports <reports@example.com>" — most providers require the address itself to match your login.`},
			},
			Inputs: []core.Port{
				{Port: "to", Label: "To", Required: true, MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body", MIME: []string{"text/plain"}},
				{Port: "attachments", Label: "Attachments", Variadic: true},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
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
			// SMTP has no idempotency mechanism, so a retried send delivers twice.
			RetryPolicy: core.RetryNever,
			// Engine-side dedupe, so a reclaimed lease does not send the mail twice.
			DedupeWrites: true,
		},
		Execute: executeEmail,
	})
}

func splitRecipients(s string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(s, ",") {
		if addr := strings.TrimSpace(part); addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

// One envelope entry per address, or a duplicate is delivered twice.
func dedupeRecipients(lists ...[]string) []string {
	total := 0
	for _, l := range lists {
		total += len(l)
	}
	out := make([]string, 0, total)
	seen := make(map[string]bool, total)
	for _, list := range lists {
		for _, addr := range list {
			key := strings.ToLower(strings.TrimSpace(addr))
			if parsed, err := mail.ParseAddress(addr); err == nil {
				key = strings.ToLower(parsed.Address)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, addr)
		}
	}
	return out
}

// Injected as a string, so it has to be parsed rather than asserted.
func smtpPort(job core.Job) (int, error) {
	raw, present := job.Params["port"]
	if !present {
		return 587, nil
	}
	s, ok := raw.(string)
	if !ok {
		return 0, fmt.Errorf("port must be a number written as text, e.g. \"587\" — re-save the mail server on the Email integration page")
	}
	if s = strings.TrimSpace(s); s == "" {
		return 587, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("port %q isn't a number — write it as 587, 465 or 25 on the Email integration page", s)
	}
	return n, nil
}

func executeEmail(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	host := strings.TrimSpace(params.StringDefault(job.Params, "host", ""))
	if host == "" {
		return params.Err(job, "not_connected", "Email isn't connected — set up your mail server on the Email integration page"), nil
	}
	from := strings.TrimSpace(params.StringDefault(job.Params, "from", ""))
	if from == "" {
		from = strings.TrimSpace(params.StringDefault(job.Params, "username", ""))
	}
	if from == "" {
		return params.Err(job, "not_connected", "no sender address — set the From address on the Email integration page"), nil
	}

	// A wired port overrides the param.
	if raw, present := job.Params["to"]; present {
		if _, ok := raw.(string); !ok {
			return params.Err(job, "bad_param", "'to' must be one comma-separated string of addresses, e.g. \"a@x.test, b@x.test\""), nil
		}
	}
	to := splitRecipients(params.StringDefault(job.Params, "to", ""))
	if wired, ok := params.TextInputOr(job, "to", ""); !ok {
		return params.Err(job, "bad_input", "'To' input must be text"), nil
	} else if wired != "" {
		to = splitRecipients(wired)
	}
	if len(to) == 0 {
		return params.Err(job, "bad_param", "'to' is required — set it or connect the 'To' input"), nil
	}

	// CC rides a visible header; BCC rides the envelope only.
	cc := splitRecipients(params.StringDefault(job.Params, "cc", ""))
	bcc := splitRecipients(params.StringDefault(job.Params, "bcc", ""))

	subject, ok := params.TextInputOr(job, "subject", params.StringDefault(job.Params, "subject", "(no subject)"))
	if !ok {
		return params.Err(job, "bad_input", "'Subject' input must be text"), nil
	}

	port, portErr := smtpPort(job)
	if portErr != nil {
		return params.Err(job, "bad_param", portErr.Error()), nil
	}
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
		default:
			raw, mErr := json.MarshalIndent(v, "", "  ")
			if mErr != nil {
				return params.Err(job, "bad_input", mErr.Error()), nil
			}
			body = string(raw)
		}
	}

	bodyContentType := `text/html; charset="utf-8"`
	isHTML := params.StringDefault(job.Params, "format", "html") != "text"
	if !isHTML {
		bodyContentType = `text/plain; charset="utf-8"`
	}

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
	// The SMTP host is a tenant-supplied param.
	if err := hfnet.CheckDialHost(addr); err != nil {
		return params.Err(job, "ssrf_blocked", err.Error()), nil
	}
	fromHeader, fromAddr := smtputil.SplitSender(from)
	msg := buildMessage(fromHeader, fromAddr, to, cc, subject, body, bodyContentType, atts)

	auth, aerr := smtputil.Auth(host, username, password)
	if aerr != nil {
		return params.Err(job, "not_connected", "mail server login is incomplete: "+aerr.Error()), nil
	}

	// Every recipient must be in the envelope, or it is simply not delivered.
	rcpts := dedupeRecipients(to, cc, bcc)

	params.EmitProgress(progress, job, 0.3, "dial "+addr)
	if err := smtputil.Send(ctx, addr, host, tlsMode, auth, fromAddr, rcpts, msg); err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	params.EmitProgress(progress, job, 1.0, "delivered")

	meta := map[string]any{
		"host": host,
		"port": port,
		"from": fromHeader,
		"to":   to,
		"cc":   cc,
		// Blind by design: envelope only, never a header.
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

// fromHeader is the display form; the envelope carries the bare address.
func buildMessage(fromHeader, fromAddr string, to, cc []string, subject, body, bodyContentType string, atts []mailmsg.Attachment) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", mailmsg.StripCRLF(fromHeader))
	fmt.Fprintf(&sb, "To: %s\r\n", mailmsg.StripCRLF(strings.Join(to, ", ")))
	if len(cc) > 0 {
		fmt.Fprintf(&sb, "Cc: %s\r\n", mailmsg.StripCRLF(strings.Join(cc, ", ")))
	}
	fmt.Fprintf(&sb, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	// Required by RFC 5322; without them a message is a common spam signal.
	fmt.Fprintf(&sb, "Date: %s\r\n", smtputil.DateHeader(time.Now()))
	fmt.Fprintf(&sb, "Message-ID: %s\r\n", smtputil.NewMessageID(fromAddr))
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
