// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "unicode/utf8"

// Bounds one free-form string in a notification the DAEMON sends. Both are read
// off a RUN RESULT rather than the graph, so nothing but the value ceiling bounded
// them: a 4 MiB approval prompt put 43.7 MB on the wire for five approvers, and
// the run budget allows 200.
const MaxNotificationTextBytes = 4000

// A mail HEADER, where RFC 5321 caps a line at 1000 octets and a server seeing a
// longer one drops the connection — so a long flow name meant NO email: nobody was
// told the run was waiting, and it could never be unblocked.
const MaxNotificationLabelBytes = 200

func ClipNotificationLabel(s string) string {
	return clipRunes(s, MaxNotificationLabelBytes)
}

func ClipNotificationText(s string) string {
	return clipRunes(s, MaxNotificationTextBytes)
}

// Cuts on a rune boundary: a string cut mid-rune renders as a replacement
// character in some clients and breaks quoted-printable in others.
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
