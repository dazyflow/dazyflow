// Package notify houses notification modules — channels a graph can use
// to report on its own outcome (email today; chat/Slack would slot in
// alongside).
package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/engine"
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
			Description: "Send an email through your own mail server (SMTP). To, Subject and Body can be typed on the step or wired in from upstream (the matching input port overrides the param) — handy for per-recipient sends or mailing another step's output.",
			Examples: []core.ParamsExample{
				{
					Title:  "Daily report via STARTTLS on port 587",
					Params: json.RawMessage(`{"host":"smtp.example.com","port":587,"tls":"starttls","username":"${secret.SMTP_USER}","password":"${secret.SMTP_PASS}","from":"reports@example.com","to":["team@example.com"],"subject":"Daily sales report","body":"See attached."}`),
				},
				{
					Title:  "Implicit TLS (port 465) to multiple recipients",
					Params: json.RawMessage(`{"host":"smtp.example.com","port":465,"tls":"implicit","username":"${secret.SMTP_USER}","password":"${secret.SMTP_PASS}","from":"alerts@example.com","to":["oncall@example.com","cto@example.com"],"subject":"Alert: error rate above threshold"}`),
					Notes:  "Body left empty here so it can be wired in from an upstream node.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Named after their params so the card shows inline editable
				// boxes (Unreal-style); a wired value overrides the typed one.
				// To takes comma-separated addresses, so it wires from any
				// string output (e.g. a sheet's Email column). Subject is
				// optional (defaults to "(no subject)").
				{Port: "to", Label: "To", Required: true, MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body", MIME: []string{"text/plain"}},
			},
			// No declared outputs: sending an email is a "do" step — "after it
			// sends, do X" chains through the pass-through pin, which fires on
			// success. The delivery details are still EMITTED under "meta"
			// (see the Execute result) so run records keep them for debugging;
			// they're just not a pin (same as gmail send / ntfy).
			Outputs: []core.Port{},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"host":{"type":"string","title":"Mail server","description":"Your SMTP server's address, e.g. smtp.example.com. Your email provider lists this in its settings."},
						"port":{"type":"integer","title":"Port","default":587,"minimum":1,"description":"Mail server port — usually 587 (STARTTLS) or 465 (SSL/TLS)."},
						"tls":{"type":"string","title":"Connection security","enum":["starttls","implicit","none"],"enumNames":["STARTTLS (port 587)","SSL/TLS (port 465)","None — insecure"],"default":"starttls","description":"How the connection is encrypted. Match what your provider says for the port you chose; None is for local testing only."},
						"username":{"type":"string","title":"Username","description":"Mail server login, usually your email address. ${secret.NAME} keeps it out of the flow."},
						"password":{"type":"string","title":"Password","description":"Mail server password or app password. ${secret.NAME} keeps it out of the flow."},
						"from":{"type":"string","title":"From address","description":"The sender address — most providers require it to match the login."},
						"to":{"type":"array","title":"To","items":{"type":"string"},"description":"Recipient addresses. Overridden by the 'To' input (comma-separated)."},
						"subject":{"type":"string","title":"Subject","description":"Subject line. Overridden by the 'Subject' input."},
						"body":{"type":"string","title":"Body","description":"Email body text. Overridden by the 'Body' input."}
					},
					"required":["host","from","to"]
				}`,
			),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
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

func executeEmail(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	host, err := params.String(job.Params, "host")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	from, err := params.String(job.Params, "from")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	// to / subject / body each take their value from the matching input port
	// when one is wired, otherwise from the param (the "input overrides param"
	// pattern). A non-text value wired into To or Subject is a mistake we
	// reject.
	to := paramStringSlice(job.Params, "to")
	if wired, ok := emailTextInputOr(job, "to", ""); !ok {
		return params.Err(job, "bad_input", "'To' input must be text"), nil
	} else if wired != "" {
		to = splitRecipients(wired)
	}
	if len(to) == 0 {
		return params.Err(job, "bad_param", "'to' is required — set it or wire the 'To' input"), nil
	}

	// Subject is optional — minimal friction for non-tech authors; To is the
	// only per-send hard requirement (same as gmail send).
	subject, ok := emailTextInputOr(job, "subject", params.StringDefault(job.Params, "subject", "(no subject)"))
	if !ok {
		return params.Err(job, "bad_input", "'Subject' input must be text"), nil
	}

	port := params.IntDefault(job.Params, "port", 587)
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

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	// SSRF guard: the SMTP host is a tenant-supplied param, so refuse
	// private/loopback/link-local targets (internal services, metadata)
	// unless the operator opted into private egress.
	if err := hfnet.CheckDialHost(addr); err != nil {
		return params.Err(job, "ssrf_blocked", err.Error()), nil
	}
	msg := buildMessage(from, to, subject, body)

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	emitProgress(progress, job, 0.3, "dial "+addr)
	if err := sendEmail(ctx, addr, host, tlsMode, auth, from, to, msg); err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	emitProgress(progress, job, 1.0, "delivered")

	meta := map[string]any{
		"host":       host,
		"port":       port,
		"from":       from,
		"to":         to,
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

func buildMessage(from string, to []string, subject, body string) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", from)
	fmt.Fprintf(&sb, "To: %s\r\n", strings.Join(to, ", "))
	// RFC 2047 encoded-word — non-ASCII subjects must not ride as raw
	// UTF-8 bytes in a header, or receiving clients mojibake them.
	fmt.Fprintf(&sb, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

func sendEmail(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	var conn net.Conn
	var err error
	switch mode {
	case "implicit":
		dialer := tls.Dialer{Config: &tls.Config{ServerName: host}}
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	default:
		dialer := &net.Dialer{}
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return c.Quit()
}
