package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "gmail_send_email",
			Version:        "1.0",
			Label:          "Gmail send email",
			Color:          "#D14836",
			Icon:           "mail",
			BrandLogo:      "/brands/gmail.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Gmail",
			Tags:           []string{"gmail", "email", "send", "google"},
			Description:    "Send an email via Gmail. The body comes from the 'body' input port if connected, otherwise from params.body. format=html sets the Content-Type so HTML renders. From-address is the authorized Google account (Gmail doesn't let you spoof a different sender via the API).",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Email body (overrides params.body)"},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":   {"type":"string","default":"default"},
					"token":     {"type":"string","description":"Raw access token; overrides 'account'."},
					"to":        {"type":"string","description":"Recipient address (or comma-separated list)."},
					"cc":        {"type":"string"},
					"bcc":       {"type":"string"},
					"subject":   {"type":"string"},
					"body":      {"type":"string","description":"Default body when the input port isn't wired."},
					"format":    {"type":"string","enum":["text","html"],"default":"text","description":"text/plain vs text/html Content-Type."},
					"reply_to":  {"type":"string","description":"Reply-To header."},
					"thread_id":{"type":"string","description":"Gmail thread ID to thread this reply into."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["to"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGmailSendEmail,
	})
}

// executeGmailSendEmail constructs an RFC822 message, base64-URL-
// encodes it (the Gmail API's required wire format — NOT std
// base64, the URL variant without padding), and POSTs to
// users/me/messages/send. The "me" alias means "the authorized
// user", so the sender is implicitly the OAuth-connected account.
func executeGmailSendEmail(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	to, err := params.String(job.Params, "to")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	body, _ := params.StringOpt(job.Params, "body")
	if input, ok := job.Input["body"]; ok && input.Inline != nil {
		switch v := input.Inline.(type) {
		case string:
			body = v
		case []byte:
			body = string(v)
		default:
			return params.Err(job, "bad_input",
				fmt.Sprintf("body: expected string (text or HTML); got %T", v)), nil
		}
	}
	if body == "" {
		return params.Err(job, "bad_input", "no body — set params.body or wire the 'body' input port"), nil
	}

	subject := params.StringDefault(job.Params, "subject", "(no subject)")
	format := params.StringDefault(job.Params, "format", "text")
	contentType := "text/plain; charset=\"utf-8\""
	if format == "html" {
		contentType = "text/html; charset=\"utf-8\""
	}

	raw, err := buildRFC822(rfc822Headers{
		To:          to,
		Cc:          params.StringDefault(job.Params, "cc", ""),
		Bcc:         params.StringDefault(job.Params, "bcc", ""),
		Subject:     subject,
		ReplyTo:     params.StringDefault(job.Params, "reply_to", ""),
		ContentType: contentType,
	}, body)
	if err != nil {
		return params.Err(job, "internal", fmt.Sprintf("build rfc822: %v", err)), nil
	}

	payload := map[string]any{
		// base64-URL-encode without padding — Gmail's required
		// wire format for raw messages.
		"raw": base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(raw),
	}
	if tid, ok := params.StringOpt(job.Params, "thread_id"); ok && tid != "" {
		payload["threadId"] = tid
	}
	jsonBody, _ := json.Marshal(payload)

	url := currentHTTPBase() + "/users/me/messages/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return params.Err(job, "internal", err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	// Idempotency-Key prevents double-send on worker retry. Gmail
	// itself doesn't fully honor the header today, but sending it
	// is harmless to APIs that ignore it and forward-compatible
	// if Google starts deduping (the Cloud APIs they front do).
	req.Header.Set("Idempotency-Key", job.IdempotencyKey())

	timeoutMs := params.IntDefault(job.Params, "timeout_ms", 15000)
	client := &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return params.Err(job, "send_failed", err.Error()), nil
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return params.Err(job, "gmail_error",
			fmt.Sprintf("Gmail returned %d: %s", resp.StatusCode, extractGmailError(respBody))), nil
	}

	var parsed map[string]any
	_ = json.Unmarshal(respBody, &parsed)
	meta := map[string]any{
		"id":       stringField(parsed, "id"),
		"threadId": stringField(parsed, "threadId"),
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: meta},
		},
	}, nil
}

// rfc822Headers is the small subset of email headers we care about.
// Full MIME (attachments, multipart bodies) is a future drop —
// gmail_send_email is the "send a plain or HTML message" workhorse.
type rfc822Headers struct {
	To          string
	Cc          string
	Bcc         string
	Subject     string
	ReplyTo     string
	ContentType string
}

// buildRFC822 builds the wire-format message Gmail expects. CRLF
// line endings are spec-mandated; lone-LF works for many MTAs but
// some choke. Using \r\n keeps us safe everywhere.
func buildRFC822(h rfc822Headers, body string) ([]byte, error) {
	var buf bytes.Buffer
	header := func(name, value string) {
		if value == "" {
			return
		}
		// Strip CR/LF from header values — header injection
		// defense. A user supplying "test@x\r\nBcc: leak@y" must
		// not be able to add headers.
		v := strings.NewReplacer("\r", "", "\n", "").Replace(value)
		fmt.Fprintf(&buf, "%s: %s\r\n", name, v)
	}
	header("To", h.To)
	header("Cc", h.Cc)
	header("Bcc", h.Bcc)
	header("Reply-To", h.ReplyTo)
	header("Subject", h.Subject)
	header("MIME-Version", "1.0")
	header("Content-Type", h.ContentType)
	header("Content-Transfer-Encoding", "8bit")
	buf.WriteString("\r\n")
	buf.WriteString(body)
	return buf.Bytes(), nil
}

func stringField(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}
