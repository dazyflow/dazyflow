// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "unicode/utf8"

// MaxNotificationTextBytes bounds one free-form string embedded in a
// notification the DAEMON sends — an approval prompt, a run's error message.
// These are the strings a flow author (or a failing step) supplies and the
// operator's own transactional mailer then delivers, so their size is chosen by
// someone other than whoever pays to send them.
//
// Neither was bounded by anything but the value ceiling (64 MiB), because both
// are read off a RUN RESULT rather than off the graph. Rendered into an HTML
// body and a plain-text body and sent once per recipient, a 4 MiB approval
// prompt put 43.7 MB on the wire for five approvers — 10.4x the prompt — and
// the run budget allows 200. The failure payload carries the same unbounded
// error message to a third-party webhook as well as to mail.
//
// Truncating costs nothing a reader needs: the approval mail's whole purpose is
// the link to the decision, and the Approvals inbox and the run page both show
// the full text. Generous for a question a person reads in an email.
const MaxNotificationTextBytes = 4000

// MaxNotificationLabelBytes bounds a string that becomes a mail HEADER or a
// one-line label in a notification — the flow's display name (which is the
// Subject) and the step id beside it. A header has a much harder limit than a
// body: RFC 5321 caps a line at 1000 octets, and a server that sees a longer
// one drops the connection. So an author who gave a flow a long name did not
// get a big email, they got NO email — the approval mail never arrived, nobody
// was told the run was waiting, and the run could never be unblocked. A 2 MiB
// name reproduced exactly that, and it silently broke failure mail too.
const MaxNotificationLabelBytes = 200

// ClipNotificationLabel bounds a string used as a mail header or a one-line
// label. See MaxNotificationLabelBytes for why this limit is far below the
// body one.
func ClipNotificationLabel(s string) string {
	return clipRunes(s, MaxNotificationLabelBytes)
}

// ClipNotificationText bounds s for embedding in a notification, appending an
// ellipsis when it had to cut. It cuts on a rune boundary so a truncated string
// is still valid UTF-8 — a body cut mid-rune renders as a replacement
// character in some clients and breaks quoted-printable encoding in others.
func ClipNotificationText(s string) string {
	return clipRunes(s, MaxNotificationTextBytes)
}

// clipRunes cuts s to at most max bytes on a rune boundary, appending an
// ellipsis when it had to cut. The boundary matters: a string cut mid-rune
// renders as a replacement character in some clients and breaks
// quoted-printable encoding in others.
func clipRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
