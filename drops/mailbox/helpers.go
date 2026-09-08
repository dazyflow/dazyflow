// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The drops that READ a mail account over IMAP; sending lives elsewhere.
package mailbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers ISO-8859-*, windows-125*, KOI8-* … for header/body decoding
	"github.com/emersion/go-message/mail"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/internal/imaputil"
)

const integration = "Mailbox"

const maxRawBytes = 256 << 10

// The same cap Gmail's search uses, so the two steps behave alike.
const maxBodyBytes = 20000

// Configured once per tenant, so a flow carries no credentials.
func connectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "host", Label: "Mail server (IMAP)", Required: true, Placeholder: "imap.example.com"},
		{Key: "port", Label: "Port", Placeholder: "993 (SSL/TLS) or 143 (STARTTLS)"},
		{Key: "tls", Label: "Connection security", Options: []string{"implicit", "starttls", "none"}, Placeholder: "implicit for 993, starttls for 143"},
		{Key: "username", Label: "Username", Required: true, Placeholder: "usually your email address"},
		{Key: "password", Label: "Password", Secret: true, Required: true, Help: "Your mailbox password — or, on a provider with two-factor sign-in (Gmail, Fastmail, iCloud), an app password generated for this."},
		{Key: "folder", Label: "Folder", Placeholder: "INBOX", Help: "Which folder the steps read by default. A step can point at another one."},
	}
}

func configFromJob(job core.Job) (imaputil.Config, error) {
	host := strings.TrimSpace(params.StringDefault(job.Params, "host", ""))
	if host == "" {
		return imaputil.Config{}, fmt.Errorf("no mailbox connected — set up your mail server on the Mailbox integration page")
	}
	mode, err := imaputil.ParseMode(params.StringDefault(job.Params, "tls", ""))
	if err != nil {
		return imaputil.Config{}, err
	}
	// Injected as a string, so it has to be parsed rather than asserted.
	port, err := imaputil.ParsePort(params.StringDefault(job.Params, "port", ""), mode)
	if err != nil {
		return imaputil.Config{}, err
	}
	folder := strings.TrimSpace(params.StringDefault(job.Params, "folder", ""))
	if folder == "" {
		folder = imaputil.DefaultFolder
	}
	return imaputil.Config{
		Host:     host,
		Port:     port,
		TLS:      mode,
		Username: params.StringDefault(job.Params, "username", ""),
		Password: params.StringDefault(job.Params, "password", ""),
		Folder:   folder,
	}, nil
}

// What one message costs on the wire; the body is the expensive part.
func searchFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		Envelope:     true,
		BodySection: []*imap.FetchItemBodySection{{
			Peek:    true,
			Partial: &imap.SectionPartial{Offset: 0, Size: maxRawBytes},
		}},
	}
}

func messageRecord(buf *imapclient.FetchMessageBuffer) map[string]any {
	rec := map[string]any{
		"id":      strconv.FormatUint(uint64(buf.UID), 10),
		"date":    "",
		"from":    "",
		"subject": "",
		"body":    "",
		"unread":  !hasFlag(buf.Flags, imap.FlagSeen),
	}
	rec["date"], rec["from"], rec["subject"] = headerValues(buf)
	if len(buf.BodySection) > 0 {
		rec["body"] = bodyText(buf.BodySection[0].Bytes)
	}
	return rec
}

func headerValues(buf *imapclient.FetchMessageBuffer) (date, from, subject string) {
	if env := buf.Envelope; env != nil {
		subject = env.Subject
		from = formatAddressList(env.From)
		if !env.Date.IsZero() {
			date = env.Date.Format(time.RFC1123Z)
		}
	}
	if date == "" && !buf.InternalDate.IsZero() {
		date = buf.InternalDate.Format(time.RFC1123Z)
	}
	return date, from, subject
}

func hasFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, f := range flags {
		if strings.EqualFold(string(f), string(want)) {
			return true
		}
	}
	return false
}

func formatAddressList(addrs []imap.Address) string {
	out := make([]string, 0, len(addrs))
	for i := range addrs {
		addr := addrs[i].Addr()
		if addr == "" {
			continue // a group start/end marker, not a mailbox
		}
		if name := strings.TrimSpace(addrs[i].Name); name != "" {
			out = append(out, name+" <"+addr+">")
			continue
		}
		out = append(out, addr)
	}
	return strings.Join(out, ", ")
}

// The first text/plain part, falling back to stripped HTML.
func bodyText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	entity, err := message.Read(bytes.NewReader(raw))
	if entity == nil {
		return ""
	}
	_ = err

	var plain, html string
	reader := mail.NewReader(entity)
	for plain == "" {
		part, perr := reader.NextPart()
		if perr != nil {
			break // io.EOF, a truncated part, or a MIME structure we can't walk
		}
		if _, isAttachment := part.Header.(*mail.AttachmentHeader); isAttachment {
			continue
		}
		// Through the interface: the concrete type differs between library versions.
		mimeType := "text/plain"
		if ct := part.Header.Get("Content-Type"); ct != "" {
			parsed, _, mErr := mime.ParseMediaType(ct)
			if mErr != nil {
				continue
			}
			mimeType = parsed
		}
		body, rErr := io.ReadAll(io.LimitReader(part.Body, maxRawBytes))
		if len(body) == 0 && rErr != nil {
			continue
		}
		switch mimeType {
		case "text/plain":
			plain = string(body)
		case "text/html":
			if html == "" {
				html = string(body)
			}
		}
	}
	body := plain
	if body == "" {
		body = html
	}
	return truncateAtRuneBoundary(strings.TrimSpace(body), maxBodyBytes)
}

// Without splitting a multi-byte rune, which would emit invalid UTF-8.
func truncateAtRuneBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func folderName(job core.Job) string {
	if f := strings.TrimSpace(params.StringDefault(job.Params, "folder", "")); f != "" {
		return f
	}
	return imaputil.DefaultFolder
}

// Resolves, dials and selects in one place, so every step opens identically.
func openMailbox(ctx context.Context, job core.Job, readOnly bool) (*imaputil.Client, *imap.SelectData, *core.Result) {
	cfg, err := configFromJob(job)
	if err != nil {
		res := params.Err(job, "not_connected", err.Error())
		return nil, nil, &res
	}
	client, err := imaputil.Dial(ctx, cfg)
	if err != nil {
		res := params.Err(job, "imap_error", err.Error())
		return nil, nil, &res
	}
	state, err := client.Select(cfg.Folder, readOnly)
	if err != nil {
		client.Close()
		res := params.Err(job, "imap_error", err.Error())
		return nil, nil, &res
	}
	return client, state, nil
}

// A UID or a message-id, since a flow may carry either.
func resolveUID(job core.Job) (imap.UID, *core.Result) {
	raw, ok := resolveIDText(job)
	if !ok {
		res := params.Err(job, "bad_input", "input port 'id' must be an email id or a list of matches")
		return 0, &res
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		res := params.Err(job, "bad_param", "'id' is required — set it or connect the 'Email' input")
		return 0, &res
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || n == 0 {
		res := params.Err(job, "bad_param", fmt.Sprintf("%q isn't an email id from this mailbox — Mailbox steps address a message by the number the 'Search emails' step emitted (an id from Gmail won't work here)", params.Truncate(raw, 40)))
		return 0, &res
	}
	return imap.UID(n), nil
}

func resolveIDText(job core.Job) (string, bool) {
	fallback := params.StringDefault(job.Params, "id", "")
	in, present := job.Input["id"]
	if !present || in.Inline == nil {
		return fallback, true
	}
	recordID := func(v any) string {
		m, isMap := v.(map[string]any)
		if !isMap {
			return ""
		}
		s, _ := m["id"].(string)
		return s
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
	case map[string]any:
		if s := recordID(v); s != "" {
			return s, true
		}
		return "", false
	case []any:
		for _, item := range v {
			if s := recordID(item); s != "" {
				return s, true
			}
		}
		return "", false
	default:
		return "", false
	}
}

// A UID that no longer exists is not an error: the message was deleted.
func fetchOneUID(client *imaputil.Client, job core.Job, uid imap.UID, opts *imap.FetchOptions) (*imapclient.FetchMessageBuffer, *core.Result) {
	bufs, err := client.Fetch(imap.UIDSetNum(uid), opts).Collect()
	if err != nil {
		res := params.Err(job, "imap_error", fmt.Sprintf("couldn't read email %d: %v", uid, err))
		return nil, &res
	}
	if len(bufs) == 0 {
		res := params.Err(job, "not_found", fmt.Sprintf("there's no email %d in %q any more — it may have been deleted or moved since the search found it", uid, folderName(job)))
		return nil, &res
	}
	return bufs[0], nil
}
