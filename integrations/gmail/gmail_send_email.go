package gmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/sandbox"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "gmail_send_email",
			Version:        "1.1",
			Label:          "Gmail send email",
			Color:          "#D14836",
			Icon:           "mail",
			BrandLogo:      "/brands/gmail.svg",
			Category:       "network",
			Provider:       "internal",
			Integration:    "Gmail",
			Tags:           []string{"gmail", "email", "send", "google", "attachments"},
			Description: "Send an email through your connected Gmail account. The body comes from the 'body' input or from the 'body' param; set format to html for rich content. Wire any number of file-producing nodes into the 'attachments' input port to attach files (PDFs, spreadsheets, anything else) — each ref's MIME + filename ride through to the recipient. The 'from' address is fixed to your authorized Google account — Gmail's API doesn't allow spoofing.",
			Summary:     "Send an email through the user's connected Gmail account, in plain text or HTML, with optional attachments and threading.",
			Examples: []core.ParamsExample{
				{
					Title:  "Plain-text alert",
					Params: json.RawMessage(`{"to":"oncall@example.com","subject":"Build failed","body":"main is red, see https://ci.example.com/run/123","token":"${secret:GMAIL_OAUTH}"}`),
				},
				{
					Title:  "HTML newsletter to a list",
					Params: json.RawMessage(`{"to":"team@example.com","cc":"leads@example.com","subject":"Weekly digest","body":"<h1>Highlights</h1><p>Shipped the new onboarding flow.</p>","format":"html","token":"${secret:GMAIL_OAUTH}"}`),
				},
				{
					Title:  "Threaded reply",
					Params: json.RawMessage(`{"to":"alice@example.com","subject":"Re: deploy plan","body":"Sounds good — running it at 16:00 UTC.","thread_id":"18f9d3a2c0e1b4a5","reply_to":"ops@example.com","token":"${secret:GMAIL_OAUTH}"}`),
					Notes:  "thread_id keeps the reply in the same Gmail conversation as the original message.",
				},
				{
					Title:  "Daily report with the spreadsheet as a PDF attachment",
					Params: json.RawMessage(`{"to":"me@example.com","subject":"Yesterday's comments","body":"Comments digest attached.","token":"${secret:GMAIL_OAUTH}"}`),
					Notes:  "Wire a sheets_export_pdf (or file_write of a generated PDF) output into the 'attachments' input port. The port is variadic — connect as many file-producing nodes as you want and each becomes its own attachment.",
				},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "oauth", Name: "gmail", Note: "Gmail OAuth — gmail.send / gmail.readonly scopes."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "body", Label: "Email body (overrides params.body)"},
				{
					Port:     "attachments",
					Label:    "Files to attach (wire zero or more file-producing nodes here)",
					Variadic: true,
				},
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
	bodyContentType := "text/plain; charset=\"utf-8\""
	if format == "html" {
		bodyContentType = "text/html; charset=\"utf-8\""
	}

	attachments, err := loadAttachments(job)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	raw, err := buildRFC822(rfc822Headers{
		To:              to,
		Cc:              params.StringDefault(job.Params, "cc", ""),
		Bcc:             params.StringDefault(job.Params, "bcc", ""),
		Subject:         subject,
		ReplyTo:         params.StringDefault(job.Params, "reply_to", ""),
		BodyContentType: bodyContentType,
	}, body, attachments)
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
// BodyContentType is the inner body part's type ("text/plain" or
// "text/html"); the outer Content-Type is computed by buildRFC822 and
// flips to multipart/mixed when attachments are present.
type rfc822Headers struct {
	To              string
	Cc              string
	Bcc             string
	Subject         string
	ReplyTo         string
	BodyContentType string
}

// emailAttachment carries one file's worth of bytes plus the metadata
// the recipient needs to render it (filename for Content-Disposition,
// MIME type for Content-Type). Resolved from a variadic input port at
// run time — see loadAttachments.
type emailAttachment struct {
	Filename string
	MIME     string
	Bytes    []byte
}

// buildRFC822 builds the wire-format message Gmail expects. CRLF line
// endings are spec-mandated; lone-LF works for many MTAs but some
// choke. With no attachments we emit a single-part message (preserving
// the historical wire format); with one or more we emit multipart/mixed
// with the body as the first part and each attachment as a subsequent
// base64-encoded part.
func buildRFC822(h rfc822Headers, body string, attachments []emailAttachment) ([]byte, error) {
	var buf bytes.Buffer
	headerInto := func(w *bytes.Buffer, name, value string) {
		if value == "" {
			return
		}
		// Strip CR/LF from header values — header injection defense.
		// A user supplying "test@x\r\nBcc: leak@y" must not be able to
		// add headers.
		v := strings.NewReplacer("\r", "", "\n", "").Replace(value)
		fmt.Fprintf(w, "%s: %s\r\n", name, v)
	}
	headerInto(&buf, "To", h.To)
	headerInto(&buf, "Cc", h.Cc)
	headerInto(&buf, "Bcc", h.Bcc)
	headerInto(&buf, "Reply-To", h.ReplyTo)
	// RFC 2047 encoded-word: email headers are 7-bit ASCII, so a subject
	// with non-ASCII (e.g. Swedish "Hej från Hazy Flow!") must be encoded
	// as =?utf-8?q?...?= rather than shipped as raw UTF-8 bytes — raw bytes
	// get mojibake'd by receiving clients. QEncoding leaves pure-ASCII
	// subjects untouched, so this is a no-op for the common case.
	headerInto(&buf, "Subject", mime.QEncoding.Encode("utf-8", h.Subject))
	headerInto(&buf, "MIME-Version", "1.0")

	if len(attachments) == 0 {
		headerInto(&buf, "Content-Type", h.BodyContentType)
		headerInto(&buf, "Content-Transfer-Encoding", "8bit")
		buf.WriteString("\r\n")
		buf.WriteString(body)
		return buf.Bytes(), nil
	}

	boundary, err := newBoundary()
	if err != nil {
		return nil, err
	}
	headerInto(&buf, "Content-Type", fmt.Sprintf("multipart/mixed; boundary=%q", boundary))
	buf.WriteString("\r\n")

	// Body part.
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	headerInto(&buf, "Content-Type", h.BodyContentType)
	headerInto(&buf, "Content-Transfer-Encoding", "8bit")
	buf.WriteString("\r\n")
	buf.WriteString(body)
	buf.WriteString("\r\n")

	// Attachment parts, base64-encoded with 76-column line wraps (the
	// classic MIME quoted-line width). Some receivers are strict about
	// the wrap; stdlib base64 stops short of producing that for us so
	// we wrap manually.
	for _, a := range attachments {
		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		headerInto(&buf, "Content-Type", a.MIME)
		headerInto(&buf, "Content-Disposition", dispositionHeader(a.Filename))
		headerInto(&buf, "Content-Transfer-Encoding", "base64")
		buf.WriteString("\r\n")
		encoded := base64.StdEncoding.EncodeToString(a.Bytes)
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			buf.WriteString(encoded[i:end])
			buf.WriteString("\r\n")
		}
	}
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes(), nil
}

// dispositionHeader builds the Content-Disposition value for an
// attachment. A non-ASCII filename (e.g. "årsrapport.pdf") can't ride as
// a raw header value, so mime.FormatMediaType emits the RFC 2231 form
// (filename*=utf-8''%C3%A5rsrapport.pdf); ASCII names stay as the plain
// filename= form. FormatMediaType returns "" if it can't encode the
// params — fall back to a quoted plain filename so we still send something
// sane rather than an empty header.
func dispositionHeader(filename string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": filename}); v != "" {
		return v
	}
	return fmt.Sprintf("attachment; filename=%q", filename)
}

// newBoundary returns a MIME boundary token unlikely to appear inside
// any attachment body. 16 random bytes hex-encoded gives 32 hex chars
// of entropy — collision-free for any practical message.
func newBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("boundary entropy: %w", err)
	}
	return "hazyflow-" + hex.EncodeToString(b[:]), nil
}

// loadAttachments collects every Ref on the variadic "attachments"
// input port and materialises the bytes. A Ref carries either inline
// bytes (string / []byte) or a sandbox path; either is resolved to a
// concrete []byte the MIME builder can base64-encode.
//
// Filename precedence: an explicit basename from the Ref's sandbox
// path beats a synthesised "attachment-N" — keeps "report.pdf" as
// "report.pdf" in the recipient's inbox.
func loadAttachments(job core.Job) ([]emailAttachment, error) {
	refs := core.VariadicInputs(job.Input, "attachments")
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]emailAttachment, 0, len(refs))
	for i, ref := range refs {
		data, err := readAttachmentBytes(job, ref)
		if err != nil {
			return nil, fmt.Errorf("attachment %d: %w", i, err)
		}
		out = append(out, emailAttachment{
			Filename: attachmentFilename(ref, i),
			MIME:     attachmentMIME(ref),
			Bytes:    data,
		})
	}
	return out, nil
}

// readAttachmentBytes reads the bytes a Ref points at. Inline strings/
// []byte are taken verbatim; a sandbox path (workspace:// implicit, or
// scratch://) is opened via the shared sandbox helper that confines
// reads to the per-run roots.
func readAttachmentBytes(job core.Job, ref core.Ref) ([]byte, error) {
	if ref.Inline != nil {
		switch v := ref.Inline.(type) {
		case []byte:
			return v, nil
		case string:
			return []byte(v), nil
		default:
			return nil, fmt.Errorf("inline attachment must be bytes or string, got %T", v)
		}
	}
	if ref.Ref == "" {
		return nil, fmt.Errorf("ref has neither inline bytes nor a sandbox path")
	}
	root, rel, err := sandbox.OpenRoot(job, ref.Ref)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		if sandbox.IsEscape(err) {
			return nil, fmt.Errorf("path %q escapes its sandbox root", ref.Ref)
		}
		return nil, fmt.Errorf("open %q: %w", ref.Ref, err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

// attachmentFilename picks a sensible filename for the
// Content-Disposition header. Sandbox path basename wins (it's what
// the user named the file); inline refs get a synthesised name with an
// extension guessed from the MIME, falling back to .bin.
func attachmentFilename(ref core.Ref, idx int) string {
	if ref.Ref != "" {
		if name := path.Base(ref.Ref); name != "" && name != "." && name != "/" {
			return name
		}
	}
	return fmt.Sprintf("attachment-%d%s", idx+1, extForMIME(ref.MIME))
}

// attachmentMIME returns the declared MIME or a generic-binary fallback
// the receiver can still save and inspect.
func attachmentMIME(ref core.Ref) string {
	if ref.MIME != "" {
		return ref.MIME
	}
	return "application/octet-stream"
}

// extForMIME maps the handful of MIME types Hazy Flow file-producing
// drops emit today to their canonical extension. Anything outside the
// list lands as .bin — better than no extension, and the MIME header is
// the authoritative signal anyway.
func extForMIME(mime string) string {
	switch strings.ToLower(strings.SplitN(mime, ";", 2)[0]) {
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
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/zip":
		return ".zip"
	}
	return ".bin"
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
