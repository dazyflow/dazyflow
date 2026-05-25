// Package notify houses notification modules — channels a graph can use
// to report on its own outcome (email today; chat/Slack would slot in
// alongside).
package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/smtp"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "email_send",
			Version:        "1.0",
			Label:          "Send email",
			Color:          "#2dd4bf",
			Icon:           "mail",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Email",
			Tags:           []string{"email", "smtp", "notify", "report"},
			Description:    "Send an email via SMTP. The body input port overrides params.body so upstream node output (e.g. a build's stdout or meta JSON) can be reported directly. Use ${env:NAME} placeholders for credentials.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Email body (overrides params.body)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata (JSON)"},
			},
			ParamsSchema: json.RawMessage(
				`{
					"type":"object",
					"properties":{
						"host":{"type":"string","description":"SMTP server hostname."},
						"port":{"type":"integer","default":587,"minimum":1,"description":"SMTP server port."},
						"username":{"type":"string","description":"SMTP auth username. ${env:NAME} resolves secrets."},
						"password":{"type":"string","description":"SMTP auth password. ${env:NAME} resolves secrets."},
						"from":{"type":"string","description":"From address (must match the auth identity for most providers)."},
						"to":{"type":"array","items":{"type":"string"},"description":"Recipient addresses."},
						"subject":{"type":"string","description":"Subject line."},
						"body":{"type":"string","description":"Email body. Overridden by the body input port if connected."},
						"tls":{"type":"string","enum":["starttls","implicit","none"],"default":"starttls","description":"TLS mode. starttls upgrades after EHLO (port 587); implicit wraps the socket in TLS (port 465); none is plaintext (dev only)."}
					},
					"required":["host","from","to","subject"]
				}`,
			),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeEmail,
	})
}

func executeEmail(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	host, err := paramString(job.Params, "host")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	from, err := paramString(job.Params, "from")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	subject, err := paramString(job.Params, "subject")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	to := paramStringSlice(job.Params, "to")
	if len(to) == 0 {
		return errResult(job, "bad_param", "to: at least one recipient required"), nil
	}
	port := paramIntDefault(job.Params, "port", 587)
	tlsMode := paramStringDefault(job.Params, "tls", "starttls")
	username := paramStringDefault(job.Params, "username", "")
	password := paramStringDefault(job.Params, "password", "")

	body := paramStringDefault(job.Params, "body", "")
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
				return errResult(job, "bad_input", mErr.Error()), nil
			}
			body = string(raw)
		}
	}

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	msg := buildMessage(from, to, subject, body)

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	emitProgress(progress, job, 0.3, "dial "+addr)
	if err := sendEmail(ctx, addr, host, tlsMode, auth, from, to, msg); err != nil {
		return errResult(job, "send_failed", err.Error()), nil
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
	fmt.Fprintf(&sb, "Subject: %s\r\n", subject)
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
