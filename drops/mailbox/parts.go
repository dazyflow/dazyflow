// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-message/charset"
)

// part is one leaf of a message's MIME tree, paired with the section path a
// FETCH addresses it by (BODY[2.1] is path []int{2, 1}).
//
// Working from BODYSTRUCTURE rather than the raw message is what makes reading
// one email cheap: the server describes the tree, we pick the one part we
// want, and only that part crosses the wire. A mail with a 20 MB PDF attached
// costs a body read nothing.
type part struct {
	path []int
	leaf *imap.BodyStructureSinglePart
}

// leafParts flattens a body structure to its single parts, in the order the
// server reports them (DFS pre-order — so for multipart/alternative the
// plain-text alternative comes before the HTML one, which is the order the RFC
// requires senders to use: simplest first).
func leafParts(bs imap.BodyStructure) []part {
	if bs == nil {
		return nil
	}
	var out []part
	bs.Walk(func(p []int, cur imap.BodyStructure) bool {
		if leaf, ok := cur.(*imap.BodyStructureSinglePart); ok {
			// Walk hands back a slice it reuses between calls, so the path has
			// to be copied or every entry ends up pointing at the last one.
			cp := make([]int, len(p))
			copy(cp, p)
			out = append(out, part{path: cp, leaf: leaf})
		}
		return true
	})
	return out
}

// isAttachment reports whether a part is a file someone attached, as opposed
// to the message text or an inline decoration.
//
// An explicit `Content-Disposition: inline` is honoured even when the part
// carries a filename, which is how a signature logo announces itself — those
// are exactly what "Only these types" exists to avoid having to filter by
// hand. With no disposition at all, a filename is taken as the intent: some
// senders omit the header entirely.
func isAttachment(leaf *imap.BodyStructureSinglePart) bool {
	if disp := leaf.Disposition(); disp != nil {
		switch strings.ToLower(strings.TrimSpace(disp.Value)) {
		case "attachment":
			return true
		case "inline":
			return false
		}
	}
	return strings.TrimSpace(leaf.Filename()) != ""
}

// pickTextPart chooses the part to show as the message body: the first
// text/plain that isn't an attachment, falling back to the first text/html.
// Mirrors the preference Gmail's Read email applies (plain, then html), so the
// two steps don't disagree about what "the body" means.
//
// A nil result means the message has no readable text part at all — a
// notification whose entire payload is a PDF, say. That is a legitimate email,
// so it reads as an empty body rather than an error.
func pickTextPart(parts []part) *part {
	var html *part
	for i := range parts {
		if isAttachment(parts[i].leaf) {
			continue
		}
		switch parts[i].leaf.MediaType() {
		case "text/plain":
			return &parts[i]
		case "text/html":
			if html == nil {
				html = &parts[i]
			}
		}
	}
	return html
}

// decodePart undoes a part's content-transfer-encoding and converts it to
// UTF-8. Both matter for real mail: a Swedish invoice routinely arrives
// quoted-printable in ISO-8859-1, and skipping either step shows the reader
// "Fakturan =E4r betald" or mojibake instead of words.
//
// Everything degrades rather than fails. A part whose encoding this build
// can't undo comes back as the bytes we were given: partly readable beats an
// error on a step whose job is to show someone their email.
func decodePart(raw []byte, leaf *imap.BodyStructureSinglePart) []byte {
	decoded := decodeTransferEncoding(raw, leaf.Encoding)
	name := ""
	if leaf.Params != nil {
		name = leaf.Params["charset"]
	}
	if name == "" || strings.EqualFold(name, "utf-8") || strings.EqualFold(name, "us-ascii") {
		return decoded
	}
	r, err := charset.Reader(name, bytes.NewReader(decoded))
	if err != nil {
		return decoded // an encoding we don't carry a table for
	}
	converted, err := io.ReadAll(r)
	if err != nil {
		return decoded
	}
	return converted
}

// decodeTransferEncoding undoes base64 or quoted-printable. Anything else
// ("7bit", "8bit", "binary", or an empty header) is already the bytes.
func decodeTransferEncoding(raw []byte, encoding string) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		// Line breaks are mandatory in a base64 MIME part and are not part of
		// the payload, so the strict decoder has to be given a stripped
		// stream. A truncated tail (a partial FETCH, a mail cut short) decodes
		// as far as it goes rather than throwing the whole part away.
		clean := make([]byte, 0, len(raw))
		for _, b := range raw {
			switch b {
			case '\r', '\n', ' ', '\t':
			default:
				clean = append(clean, b)
			}
		}
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(out, clean)
		if err != nil && n == 0 {
			return raw
		}
		return out[:n]
	case "quoted-printable":
		out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
		if err != nil && len(out) == 0 {
			return raw
		}
		return out
	default:
		return raw
	}
}

// sectionFor is the FETCH item that pulls one part's bytes. PEEK, always: a
// plain BODY[...] fetch sets \Seen, so reading a message or taking a file off
// it would silently mark it read — and a triage flow polling for unread mail
// would then never see it again.
func sectionFor(p part) *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{Part: p.path, Peek: true}
}
