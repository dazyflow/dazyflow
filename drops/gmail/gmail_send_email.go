package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/mailmsg"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_send_email",
			Version:     "1.0",
			Label:       "Gmail",
			Subtitle:    "Send email",
			Summary:     "Send an email as the connected Gmail account, with optional CC/BCC, HTML, and file attachments.",
			Description: "Send an email from the connected mailbox. To, Subject and Body can be typed as params or wired in from upstream (the matching input port overrides the param) — handy for per-recipient sends. Attach files by wiring file-producing nodes (e.g. sheets_export_pdf) into the variadic 'attachments' input.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "mail",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "send", "smtp"},
			Examples: []core.ParamsExample{
				{Title: "Plain-text alert", Params: json.RawMessage(`{"to":"oncall@example.com","subject":"Build failed","body":"main is red","token":"${secret.GMAIL_OAUTH}"}`)},
				{Title: "Daily report with a PDF attachment", Params: json.RawMessage(`{"to":"me@example.com","subject":"Yesterday's comments","body":"Comments digest attached.","token":"${secret.GMAIL_OAUTH}"}`), Notes: "Wire a file-producing node (e.g. sheets_export_pdf) into the variadic 'attachments' input."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.send scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// To and Body are required (an email needs a recipient and
				// content); each input overrides its param. Subject is optional
				// (defaults to "(no subject)"). All text so they wire from any
				// string output (e.g. a sheet's Email / Name column).
				{Port: "to", Label: "To", Required: true, MIME: []string{"text/plain"}},
				{Port: "subject", Label: "Subject", MIME: []string{"text/plain"}},
				{Port: "body", Label: "Body", MIME: []string{"text/plain"}},
				{Port: "attachments", Label: "Attachments", Variadic: true},
			},
			// No declared outputs: sending an email is a "do" step — "after it
			// sends, do X" chains through the pass-through pin, which fires on
			// success. The Gmail message id is still EMITTED under "meta" (see
			// the Execute result) so run records keep it for debugging; it's
			// just not a pin. Re-expose it as a named port if a reply-in-thread
			// feature ever needs to wire it.
			Outputs: []core.Port{},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"to":{"type":"string","description":"Recipient(s), comma-separated. Overridden by the 'To' input."},
					"cc":{"type":"string","title":"CC","description":"Carbon-copy recipient(s), comma-separated. Everyone on the email sees who's CC'd."},
					"bcc":{"type":"string","title":"BCC","description":"Blind-copy recipient(s), comma-separated. Hidden from the other recipients."},
					"subject":{"type":"string","title":"Subject","description":"The email's subject line — e.g. \"Re: your submission\". Leave blank and it sends as \"(no subject)\". Overridden by the 'Subject' input."},
					"body":{"type":"string","format":"multiline","description":"Body text. Overridden by the 'Body' input."},
					"format":{"type":"string","enum":["text","html"],"enumNames":["Text","HTML"],"default":"html"},
					"reply_to":{"type":"string"},
					"thread_id":{"type":"string","description":"Gmail thread ID to reply within."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["to"]
			}`),
			Idempotent: false,
			// Gmail's messages.send has no generic idempotency header, so
			// a retried POST sends the email twice. This drop is a terminal
			// leaf the engine auto-retries on backoff, so retries must be
			// off here.
			RetryPolicy: core.RetryNever,
			// …and the engine dedupes a same-job re-execution (expired-lease
			// reclaim / crash recovery) so a recovered run doesn't re-send.
			DedupeWrites: true,
		},
		Execute: executeGmailSend,
	})
}

func executeGmailSend(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// to / subject / body each take their value from the matching input port
	// when one is wired, otherwise from the param (the "input overrides param"
	// pattern). A non-text value wired into any of them is a mistake we reject.
	to, ok := params.TextInputOr(job, "to", params.StringDefault(job.Params, "to", ""))
	if !ok {
		return params.Err(job, "bad_input", "'To' input must be text"), nil
	}
	if to == "" {
		return params.Err(job, "bad_param", "'to' is required — set it or wire the 'To' input"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	// Body is optional — an empty body is allowed (send a subject-only email).
	// Minimal friction for non-tech authors; To is the only hard requirement.
	body, ok := params.TextInputOr(job, "body", params.StringDefault(job.Params, "body", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Body' input must be text"), nil
	}
	subject, ok := params.TextInputOr(job, "subject", params.StringDefault(job.Params, "subject", "(no subject)"))
	if !ok {
		return params.Err(job, "bad_input", "'Subject' input must be text"), nil
	}

	// Default to HTML (matches the schema default) so an unset format isn't
	// sent as plain text when the form shows HTML selected. Explicit "text"
	// still sends text/plain.
	bodyContentType := `text/html; charset="utf-8"`
	if params.StringDefault(job.Params, "format", "html") == "text" {
		bodyContentType = `text/plain; charset="utf-8"`
	}

	atts, jerr := mailmsg.LoadAttachments(job)
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	msg := buildRFC822(rfcHeaders{
		to:              to,
		cc:              params.StringDefault(job.Params, "cc", ""),
		bcc:             params.StringDefault(job.Params, "bcc", ""),
		replyTo:         params.StringDefault(job.Params, "reply_to", ""),
		subject:         subject,
		bodyContentType: bodyContentType,
	}, body, atts)

	payload := map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte(msg))}
	if tid, _ := params.StringOpt(job.Params, "thread_id"); tid != "" {
		payload["threadId"] = tid
	}
	raw, _ := json.Marshal(payload)

	endpoint := baseURL(job) + "/users/me/messages/send"
	status, respBody, err := gmailDo(ctx, "POST", endpoint, token, "application/json; charset=utf-8", raw, params.IntDefault(job.Params, "timeout_ms", 15000))
	if fail := params.HTTPFailure(job, "gmail", "Gmail", status, respBody, err, extractGmailError); fail != nil {
		return *fail, nil
	}

	var parsed struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"meta": {MIME: "application/json", Inline: map[string]any{
			"id": parsed.ID, "threadId": parsed.ThreadID,
		}}},
	}, nil
}

type rfcHeaders struct {
	to, cc, bcc, replyTo, subject, bodyContentType string
}

// buildRFC822 assembles the message Gmail's send endpoint wants. Header
// values are stripped of CR/LF to defeat header injection; the subject is
// MIME-word encoded; multipart/mixed is used only when attachments exist.
func buildRFC822(h rfcHeaders, body string, atts []mailmsg.Attachment) string {
	var lines []string
	add := func(name, value string) {
		if value == "" {
			return
		}
		lines = append(lines, name+": "+mailmsg.StripCRLF(value))
	}
	add("To", h.to)
	add("Cc", h.cc)
	add("Bcc", h.bcc)
	add("Reply-To", h.replyTo)
	add("Subject", mime.QEncoding.Encode("utf-8", h.subject))
	add("MIME-Version", "1.0")

	if len(atts) == 0 {
		add("Content-Type", h.bodyContentType)
		add("Content-Transfer-Encoding", "8bit")
		return strings.Join(lines, "\r\n") + "\r\n\r\n" + body
	}

	boundary := "dazyflow-" + mailmsg.RandomHex(16)
	add("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)

	var b strings.Builder
	b.WriteString(strings.Join(lines, "\r\n") + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: " + h.bodyContentType + "\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(body + "\r\n")
	mailmsg.WriteAttachmentParts(&b, boundary, atts)
	return b.String()
}
