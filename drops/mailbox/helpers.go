// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailbox holds the drops that READ a mail account over IMAP —
// searching a folder, reading one message, taking its attachments, marking it
// read. The send side is a different protocol and a different drop: SMTP
// (drops/notify, the Email step) can only submit mail, because SMTP has no
// command to list, fetch or search a mailbox at all.
//
// Kept a separate integration from Email rather than more fields on that one.
// Two reasons, in order of how much they'd hurt: the connection UI resolves an
// integration's fields from the FIRST drop it finds declaring them
// (daemon/httpconnectionverify.go), so sharing "Email" would force IMAP fields
// onto a send-only setup; and the two halves genuinely differ — imap.host vs
// smtp.host, 993 vs 587 — so one bundle would ask everybody for six fields
// they may not have. The cost is that someone doing both configures two pages.
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

// integration is the label every drop here shares — the name of the page a
// tenant configures once, and the key its stored connection hangs off.
const integration = "Mailbox"

// maxRawBytes caps how much of each message the search pulls down. IMAP can
// slice a message server-side (a partial FETCH), so a mailbox full of 30 MB
// attachments costs a search nothing: what arrives is the front of the
// message, which is where the headers and the readable body are. Download
// attachments fetches the parts it wants in full, separately.
const maxRawBytes = 256 << 10

// maxBodyBytes caps the body carried in a search result — the same cap Gmail's
// Search emails applies, for the same reason: fifty matches must not turn into
// a multi-megabyte payload the editor then has to render. Read email returns
// the body uncapped when a single message needs all of it.
const maxBodyBytes = 20000

// connectionFields is the mailbox connection, configured once on the Mailbox
// integration page and injected into every node's params at run time
// (injectConnectionDefaults) — so flows carry only the per-search fields, and
// the password never lands in a graph.
//
// Every drop in the integration MUST declare this same slice: the connection
// UI takes the fields from whichever drop it finds first, so a drop that
// declared a subset would render a page missing whatever it left out.
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

// configFromJob assembles the mailbox connection from the params the engine
// injected. `folder` is declared as a param as well as a connection field, so
// a step can point at another folder while everything else comes from the
// connection — injectConnectionDefaults leaves an author's per-step value
// alone.
func configFromJob(job core.Job) (imaputil.Config, error) {
	host := strings.TrimSpace(params.StringDefault(job.Params, "host", ""))
	if host == "" {
		return imaputil.Config{}, fmt.Errorf("no mailbox connected — set up your mail server on the Mailbox integration page")
	}
	mode, err := imaputil.ParseMode(params.StringDefault(job.Params, "tls", ""))
	if err != nil {
		return imaputil.Config{}, err
	}
	// ConnectionFields inject the port as a string ("993"); a graph saved
	// before the field existed may carry it as a JSON number. Try the string
	// form first, then the numeric one — the same two-step the Email drop's
	// smtpPort makes, for the same reason.
	portStr := strings.TrimSpace(params.StringDefault(job.Params, "port", ""))
	if portStr == "" {
		if n := params.IntDefault(job.Params, "port", 0); n > 0 {
			portStr = strconv.Itoa(n)
		}
	}
	port, err := imaputil.ParsePort(portStr, mode)
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

// searchFetchOptions is what one message costs on the wire: its UID, flags,
// receive time, decoded envelope, and the capped front of the raw message.
// BODY.PEEK (not BODY) is load-bearing — a plain BODY[] fetch sets \Seen as a
// side effect, so merely searching a mailbox would mark it all read.
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

// messageRecord reduces one fetched message to the friendly record a flow
// works with: date / from / subject / body plus the id.
//
// Deliberately the SAME shape Gmail's Search emails emits, so the idioms built
// on that one — For each over the matches, ${item.id} into a step that reads
// or files the mail, an AI step handed ${item.body} — carry over unchanged,
// and a Gmail flow becomes an IMAP flow by swapping the step.
//
// `id` is the message's UID. Unlike a Gmail id it is only meaningful inside
// one folder, and only while the folder's UIDVALIDITY holds — which is why the
// steps that take an id also take the folder, and why the watermark below
// stores both.
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

// headerValues derives the three header fields both Search emails and Read
// email present, so the two steps can't disagree about them.
//
// The envelope is the server's own parse, already decoded out of RFC 2047 word
// encoding — so a Swedish subject arrives as text rather than
// =?iso-8859-1?Q?...?=. Preferred over re-parsing the raw bytes, which the
// search only ever holds a truncated prefix of.
func headerValues(buf *imapclient.FetchMessageBuffer) (date, from, subject string) {
	if env := buf.Envelope; env != nil {
		subject = env.Subject
		from = formatAddressList(env.From)
		if !env.Date.IsZero() {
			date = env.Date.Format(time.RFC1123Z)
		}
	}
	if date == "" && !buf.InternalDate.IsZero() {
		// No parseable Date: header — fall back to the server's receive time,
		// which is also what the watermark orders by.
		date = buf.InternalDate.Format(time.RFC1123Z)
	}
	return date, from, subject
}

// hasFlag reports whether the message carries flag (IMAP flags are
// case-insensitive).
func hasFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, f := range flags {
		if strings.EqualFold(string(f), string(want)) {
			return true
		}
	}
	return false
}

// formatAddressList renders an envelope address list the way a person reads
// it — `Ada Lovelace <ada@example.com>`, comma-separated — matching what
// Gmail's From field carries.
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

// bodyText pulls readable text out of a raw RFC 5322 message: the first
// text/plain part, falling back to text/html when a message carries no plain
// alternative (mirroring Gmail's Search emails, which prefers plain, then
// html, then the snippet).
//
// Everything here is best-effort by design. The raw bytes may have been cut
// mid-part by the partial fetch, and mail in the wild is routinely malformed;
// a body that won't parse must degrade to "" — a search that returns matches
// with empty bodies is recoverable, one that fails the whole run because a
// single message has a broken MIME boundary is not.
func bodyText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	entity, err := message.Read(bytes.NewReader(raw))
	if entity == nil {
		return ""
	}
	// A charset or encoding this build can't decode is reported by Read but
	// still yields a usable entity, so only a nil entity is fatal here.
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
		// Read through the PartHeader interface rather than the concrete
		// inline/attachment types: a part with no Content-Type at all is
		// text/plain by RFC 2045, which is exactly what a bare Get gives us.
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

// truncateAtRuneBoundary caps s at limit bytes without splitting a multi-byte
// character: the cut moves back to a rune boundary rather than leaving half a
// character, which would render as a replacement glyph in the editor.
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

// folderName is the folder a step is pointed at, for the error and detail
// messages that name it. The param wins when set, otherwise it is whatever the
// connection injected — which is the same order configFromJob resolves in.
func folderName(job core.Job) string {
	if f := strings.TrimSpace(params.StringDefault(job.Params, "folder", "")); f != "" {
		return f
	}
	return imaputil.DefaultFolder
}

// openMailbox resolves the connection from the job's params, dials, and
// selects the folder. A non-nil result is the caller's cue to return it
// unchanged; on success the caller owns Close().
//
// ctx must already carry the step's deadline — the cancel belongs in the
// caller's defer, next to the Close.
//
// readOnly picks EXAMINE over SELECT, and every step that only reads passes
// true. It is the difference between a flow that reads a mailbox and one that
// quietly marks it all read.
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

// resolveUID works out which message a step was pointed at. It accepts either
// a single id (text, e.g. ${item.id} inside a For each) or Search emails'
// "Matching emails" list wired straight in — in which case the FIRST match is
// used, so the obvious drag (Matching emails → Email) just works. Mirrors
// gmail_get_message's resolveMessageID, for the same reason the record shape
// is shared: the two steps have to feel like the same step.
//
// The id is a UID, so it is only meaningful inside one folder. A value that
// isn't a number says so plainly — the likeliest cause is a Gmail id wired
// into a Mailbox step, and "invalid syntax" would not point at that.
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

// resolveIDText pulls the id out of the param or the input port, whichever is
// wired. ok=false means the input carried something that can't be an id at
// all.
func resolveIDText(job core.Job) (string, bool) {
	fallback := params.StringDefault(job.Params, "id", "")
	in, present := job.Input["id"]
	if !present || in.Inline == nil {
		return fallback, true
	}
	// A match record, as emitted by Search emails.
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
		// The whole "Matching emails" list: take the first match.
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

// fetchOneUID runs a FETCH for a single message and returns its buffer, or a
// result explaining that the message isn't there.
//
// "Not there" is a real, ordinary outcome, not a protocol error: a UID stops
// existing the moment someone deletes or moves the mail, which can happen
// between a search and the step that reads a match. Saying so in those terms
// beats a bare empty FETCH response.
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
