// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// ticket_nudge.go reminds the side that is holding an unanswered message.
//
// support_notify.go mails on every reply, which covers the case where someone
// is paying attention; this covers the one where nobody is. A side is nudged
// only if it has not opened the thread since the message arrived and the
// message is older than the threshold, and only once per waiting period
// (NudgedAt is compared against the message's own timestamp) — a reminder that
// repeats is one people filter, which buries the reply that follows.

type NudgeSide string

const (
	NudgeUser    NudgeSide = "user"    // the customer who filed it
	NudgeSupport NudgeSide = "support" // the agent who owns it, else the inbox
)

// ticketNudge returns the side that should be reminded now, if any.
//
// Whoever did not write the thread's most recent human message is the one
// holding something unanswered — that single rule also settles replies crossing
// in flight, since answering is proof of having read. System notes are skipped:
// a status change must not make a side look like it owes an answer.
func ticketNudge(t core.Ticket, msgs []core.TicketMessage, now time.Time, after time.Duration) (NudgeSide, bool) {
	if t.Status.IsTerminal() {
		return "", false
	}
	var last core.TicketMessage
	for _, m := range msgs {
		if m.AuthorKind == core.AuthorSystem {
			continue
		}
		if m.CreatedAt.After(last.CreatedAt) {
			last = m
		}
	}
	if last.CreatedAt.IsZero() {
		return "", false // nothing said yet
	}
	side, read, nudged := NudgeSupport, t.SupportReadAt, t.SupportNudgedAt
	if last.AuthorKind == core.AuthorSupport {
		side, read, nudged = NudgeUser, t.UserReadAt, t.UserNudgedAt
	}
	if !read.Before(last.CreatedAt) {
		return "", false // seen it; not answering is not a notification problem
	}
	if now.Sub(last.CreatedAt) < after {
		return "", false // not old enough yet
	}
	if !nudged.Before(last.CreatedAt) {
		return "", false // already reminded about this one
	}
	return side, true
}
