// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailfiles holds the rules for taking files OFF an email and putting
// them in the workspace, shared by gmail_get_attachments and
// imap_get_attachments: which parts to keep, what to call the saved file, and
// how much one message may yield.
//
// The read-side counterpart to mailmsg, which assembles attachments onto an
// outgoing message. Split out when the IMAP drops arrived rather than copied,
// because the interesting part is adversarial: the filename comes from whoever
// sent the mail, so SanitizeFilename is a security boundary, and a second
// hand-copied version of it is a second thing to get wrong.
package mailfiles

import (
	"fmt"
	"path"
	"strings"

	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
)

// MaxBytes caps the total one message may yield, so a single mail with a video
// in it can't fill the run's scratch area.
const MaxBytes = 32 << 20 // 32 MiB

// KeepExtensions parses the "only these types" setting into a lowercase
// extension set. An empty setting means "keep everything", expressed as a nil
// map so Keep can tell it apart from "keep nothing".
func KeepExtensions(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		ext := strings.ToLower(strings.TrimSpace(part))
		ext = strings.TrimPrefix(ext, "*")
		ext = strings.TrimPrefix(ext, ".")
		if ext != "" {
			out[ext] = true
		}
	}
	return out
}

// Keep reports whether a file passes the extension filter.
func Keep(name string, wanted map[string]bool) bool {
	if len(wanted) == 0 {
		return true
	}
	return wanted[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]
}

// Dest names the saved file. The message id prefixes the name so two emails
// whose invoice is called "invoice.pdf" don't overwrite each other, and the
// index disambiguates within one message. An empty folder puts the file in the
// run's scratch area, which is reclaimed when the run ends.
func Dest(folder, msgID string, idx int, filename string) string {
	safe := SanitizeFilename(filename)
	name := fmt.Sprintf("%s-%d-%s", SanitizeFilename(msgID), idx+1, safe)
	if folder == "" {
		return sandbox.Scheme + name
	}
	return path.Join(strings.TrimSuffix(folder, "/"), name)
}

// SanitizeFilename reduces a sender-controlled filename to something safe to
// join onto a path: no separators, no traversal, no leading dot.
//
// This is a security boundary, not tidying. The name arrives from whoever sent
// the email, so it is hostile input by default — "../../etc/passwd" or a name
// that is all dots has to come out the other side as something that can only
// land where it was told to. The sandbox root refuses an escape as well; this
// is the layer that means the escape is never attempted.
func SanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == '-' || r == '_':
			return r
		}
		return '-'
	}, path.Base(name))
	name = strings.TrimLeft(name, ".-")
	if name == "" {
		return "attachment"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}
