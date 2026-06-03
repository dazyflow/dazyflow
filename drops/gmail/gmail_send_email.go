package gmail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "gmail_send_email",
			Version:     "1.0",
			Label:       "Gmail send email",
			Summary:     "Send an email as the connected Gmail account, with optional CC/BCC, HTML, and file attachments.",
			Description: "Send an email from the connected mailbox. Body text comes from params.body or the 'body' input; attach files by wiring file-producing nodes (e.g. sheets_export_pdf) into the variadic 'attachments' input.",
			Integration: "Gmail",
			Category:    "network",
			Icon:        "mail",
			BrandLogo:   "/brands/gmail.svg",
			Color:       "#D14836",
			Provider:    "internal",
			Tags:        []string{"gmail", "email", "send", "smtp"},
			Examples: []core.ParamsExample{
				{Title: "Plain-text alert", Params: json.RawMessage(`{"to":"oncall@example.com","subject":"Build failed","body":"main is red","token":"${tenant:GMAIL_OAUTH}"}`)},
				{Title: "Daily report with a PDF attachment", Params: json.RawMessage(`{"to":"me@example.com","subject":"Yesterday's comments","body":"Comments digest attached.","token":"${tenant:GMAIL_OAUTH}"}`), Notes: "Wire a file-producing node (e.g. sheets_export_pdf) into the variadic 'attachments' input."},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "google", Note: "Google OAuth — gmail.send scope."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Email body (overrides params.body)"},
				{Port: "attachments", Label: "Files to attach", Variadic: true},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Delivery metadata", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"account":{"type":"string","default":"default"},
					"token":{"type":"string","description":"Raw access token; overrides 'account'."},
					"to":{"type":"string","description":"Recipient(s), comma-separated."},
					"cc":{"type":"string"},
					"bcc":{"type":"string"},
					"subject":{"type":"string"},
					"body":{"type":"string","description":"Body text. Overridden by the 'body' input."},
					"format":{"type":"string","enum":["text","html"],"default":"text"},
					"reply_to":{"type":"string"},
					"thread_id":{"type":"string","description":"Gmail thread ID to reply within."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1}
				},
				"required":["to"]
			}`),
			Idempotent:  false,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeGmailSend,
	})
}

type gmailAttachment struct {
	filename string
	mime     string
	data     []byte
}

func executeGmailSend(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	to, _ := params.StringOpt(job.Params, "to")
	if to == "" {
		return params.Err(job, "bad_param", "'to' is required"), nil
	}
	token, err := resolveToken(ctx, job)
	if err != nil {
		return params.Err(job, "auth", err.Error()), nil
	}

	body := params.StringDefault(job.Params, "body", "")
	if in, ok := job.Input["body"]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case string:
			body = v
		case []byte:
			body = string(v)
		default:
			return params.Err(job, "bad_input", "body input must be text"), nil
		}
	}
	if body == "" {
		return params.Err(job, "bad_input", "no body — set params.body or wire the 'body' input port"), nil
	}

	bodyContentType := `text/plain; charset="utf-8"`
	if params.StringDefault(job.Params, "format", "text") == "html" {
		bodyContentType = `text/html; charset="utf-8"`
	}

	atts, jerr := loadAttachments(job)
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	msg := buildRFC822(rfcHeaders{
		to:              to,
		cc:              params.StringDefault(job.Params, "cc", ""),
		bcc:             params.StringDefault(job.Params, "bcc", ""),
		replyTo:         params.StringDefault(job.Params, "reply_to", ""),
		subject:         params.StringDefault(job.Params, "subject", "(no subject)"),
		bodyContentType: bodyContentType,
	}, body, atts)

	payload := map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte(msg))}
	if tid, _ := params.StringOpt(job.Params, "thread_id"); tid != "" {
		payload["threadId"] = tid
	}
	raw, _ := json.Marshal(payload)

	endpoint := baseURL(job) + "/users/me/messages/send"
	status, respBody, err := gmailDo(ctx, "POST", endpoint, token, "application/json; charset=utf-8", raw, params.IntDefault(job.Params, "timeout_ms", 15000))
	if err != nil {
		return params.Err(job, "gmail_http_error", err.Error()), nil
	}
	if status < 200 || status >= 300 {
		return params.Err(job, "gmail_error", fmt.Sprintf("Gmail returned %d: %s", status, extractGmailError(respBody))), nil
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

func loadAttachments(job core.Job) ([]gmailAttachment, *core.JobError) {
	refs := core.VariadicInputs(job.Input, "attachments")
	out := make([]gmailAttachment, 0, len(refs))
	for i, r := range refs {
		data, err := readRefBytes(job, r)
		if err != nil {
			return nil, &core.JobError{Code: "bad_input", Message: fmt.Sprintf("attachment %d: %v", i, err)}
		}
		mt := r.MIME
		if mt == "" {
			mt = "application/octet-stream"
		}
		out = append(out, gmailAttachment{filename: attachmentFilename(r, i), mime: mt, data: data})
	}
	return out, nil
}

func attachmentFilename(ref core.Ref, idx int) string {
	if ref.Ref != "" {
		base := path.Base(strings.TrimPrefix(ref.Ref, "scratch://"))
		if base != "." && base != "/" && base != "" {
			return base
		}
	}
	return fmt.Sprintf("attachment-%d%s", idx+1, extForMIME(ref.MIME))
}

type rfcHeaders struct {
	to, cc, bcc, replyTo, subject, bodyContentType string
}

// buildRFC822 assembles the message Gmail's send endpoint wants. Header
// values are stripped of CR/LF to defeat header injection; the subject is
// MIME-word encoded; multipart/mixed is used only when attachments exist.
func buildRFC822(h rfcHeaders, body string, atts []gmailAttachment) string {
	var lines []string
	add := func(name, value string) {
		if value == "" {
			return
		}
		lines = append(lines, name+": "+stripCRLF(value))
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

	boundary := "hazyflow-" + randomHex(16)
	add("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)

	var b strings.Builder
	b.WriteString(strings.Join(lines, "\r\n") + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: " + h.bodyContentType + "\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(body + "\r\n")
	for _, a := range atts {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + stripCRLF(a.mime) + "\r\n")
		b.WriteString("Content-Disposition: " + dispositionHeader(a.filename) + "\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(wrap76(base64.StdEncoding.EncodeToString(a.data)) + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

func stripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func wrap76(b64 string) string {
	var rows []string
	for i := 0; i < len(b64); i += 76 {
		end := i + 76
		if end > len(b64) {
			end = len(b64)
		}
		rows = append(rows, b64[i:end])
	}
	return strings.Join(rows, "\r\n")
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// dispositionHeader produces an attachment Content-Disposition: ASCII
// filenames are quoted; non-ASCII use RFC 2231 filename*=utf-8”…
func dispositionHeader(filename string) string {
	if isASCII(filename) {
		return `attachment; filename="` + stripCRLF(strings.ReplaceAll(filename, `"`, "")) + `"`
	}
	var pct strings.Builder
	for _, b := range []byte(filename) {
		if isAttrChar(b) {
			pct.WriteByte(b)
		} else {
			fmt.Fprintf(&pct, "%%%02X", b)
		}
	}
	return "attachment; filename*=utf-8''" + pct.String()
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// isAttrChar reports RFC 5987 attr-char (safe unencoded in filename*).
func isAttrChar(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}

func extForMIME(m string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(m, ";", 2)[0])) {
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "text/html":
		return ".html"
	case "application/json":
		return ".json"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "application/zip":
		return ".zip"
	}
	return ".bin"
}
