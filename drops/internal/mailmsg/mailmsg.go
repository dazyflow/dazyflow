// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailmsg holds the MIME assembly shared by the email-sending
// drops (gmail_send_email, email_send): loading attachments from the
// variadic 'attachments' input and the multipart/mixed encoding helpers.
package mailmsg

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/sandbox"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/internal/emailtmpl"
)

type Attachment struct {
	Filename string
	MIME     string
	Data     []byte
}

// WrapWithTemplate wraps an HTML body in the email template referenced by the
// job's "template" param, resolving the template ID to its layout shell at run
// time (a live reference — re-read every run). It returns body unchanged when
// no template is referenced. Both email drops call this just before assembling
// the message, and only for HTML sends (an HTML shell can't wrap plain text).
//
// Errors are the caller's to turn into a node failure (code "email_template"):
// a referenced template that is missing, or no provider wired at all, fails the
// node rather than silently sending an unwrapped body — so a deleted template
// surfaces instead of changing what recipients see.
func WrapWithTemplate(ctx context.Context, job core.Job, body, subject string) (string, error) {
	id, _ := job.Params["template"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return body, nil
	}
	provider, ok := engine.EmailTemplateProviderFromContext(ctx)
	if !ok || provider == nil {
		return "", fmt.Errorf("email templates are unavailable")
	}
	shell, logo, found, err := provider.TemplateHTML(ctx, job.Tenant, id)
	if err != nil {
		return "", fmt.Errorf("resolve email template %q: %w", id, err)
	}
	if !found {
		return "", fmt.Errorf("email template %q not found", id)
	}
	wrapped, err := emailtmpl.WrapBody(shell, body, subject, logo)
	if err != nil {
		return "", fmt.Errorf("render email template %q: %w", id, err)
	}
	return wrapped, nil
}

// LoadAttachments collects every ref wired into the variadic 'attachments'
// input, resolving inline bytes or sandbox files into Attachment records.
func LoadAttachments(job core.Job) ([]Attachment, *core.JobError) {
	refs := core.VariadicInputs(job.Input, "attachments")
	out := make([]Attachment, 0, len(refs))
	for i, r := range refs {
		data, err := ReadRefBytes(job, r)
		if err != nil {
			return nil, &core.JobError{Code: "bad_input", Message: fmt.Sprintf("attachment %d: %v", i, err)}
		}
		mt := r.MIME
		if mt == "" {
			mt = "application/octet-stream"
		}
		out = append(out, Attachment{Filename: AttachmentFilename(r, i), MIME: mt, Data: data})
	}
	return out, nil
}

// ReadRefBytes returns the bytes behind an input Ref: inline []byte/string,
// or a sandbox file when the ref carries a path.
func ReadRefBytes(job core.Job, ref core.Ref) ([]byte, error) {
	switch v := ref.Inline.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	}
	if ref.Ref == "" {
		return nil, fmt.Errorf("attachment has no inline bytes and no path")
	}
	root, rel, err := sandbox.OpenRoot(job, ref.Ref)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func AttachmentFilename(ref core.Ref, idx int) string {
	if ref.Ref != "" {
		base := path.Base(strings.TrimPrefix(ref.Ref, "scratch://"))
		if base != "." && base != "/" && base != "" {
			return base
		}
	}
	return fmt.Sprintf("attachment-%d%s", idx+1, ExtForMIME(ref.MIME))
}

// WriteAttachmentParts appends one MIME part per attachment plus the
// terminating boundary marker. The caller has already written the
// multipart headers and the body part.
func WriteAttachmentParts(b *strings.Builder, boundary string, atts []Attachment) {
	for _, a := range atts {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + StripCRLF(a.MIME) + "\r\n")
		b.WriteString("Content-Disposition: " + DispositionHeader(a.Filename) + "\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(Wrap76(base64.StdEncoding.EncodeToString(a.Data)) + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
}

func StripCRLF(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

func Wrap76(b64 string) string {
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

func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DispositionHeader produces an attachment Content-Disposition: ASCII
// filenames are quoted; non-ASCII use RFC 2231 filename*=utf-8”…
func DispositionHeader(filename string) string {
	if isASCII(filename) {
		return `attachment; filename="` + StripCRLF(strings.ReplaceAll(filename, `"`, "")) + `"`
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

func ExtForMIME(m string) string {
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
