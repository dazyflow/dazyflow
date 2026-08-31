// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// ticket_nudge.go decides who to remind about a message they have not read.
//
// support_notify.go already mails on every reply, which covers the case where
// someone is paying attention. This covers the one where nobody is: a mail that
// was missed, filtered, or opened and forgotten, and a ticket that then sits
// there. Without it a thread can go quiet indefinitely and neither side is ever
// told — the customer assumes support is working on it, and support assumes the
// customer went away.
//
// Two facts decide it, and the second is why read receipts alone are not
// enough:
//
//	READ    — the side has opened the thread since the message arrived. Someone
//	          who has seen it and not answered does not need a reminder that it
//	          exists; that is a staffing problem, not a notification problem.
//	AGE     — the message has been sitting for longer than the threshold. This
//	          is the floor: a side that has NEVER opened the ticket has a zero
//	          read time, so age alone carries it, and a side that opened the
//	          ticket before the message arrived is in the same position.
//
// Reminders are per WAITING PERIOD, not per sweep: NudgedAt is compared against
// the message's own timestamp, so once a side has been reminded about a given
// message it is not reminded again until a newer one arrives. A support thread
// that mails every hour is one people filter, and a filtered reminder is worse
// than none because it also buries the reply that follows.

// NudgeSide names which end of a thread is being reminded.
type NudgeSide string

const (
	NudgeUser    NudgeSide = "user"    // the customer who filed it
	NudgeSupport NudgeSide = "support" // the agent who owns it, else the inbox
)

// ticketNudge returns the side that should be reminded now, if any.
//
// At most one side is ever waiting, and which one is decided by the thread's
// most recent human message: whoever did not write it is the one holding
// something unanswered. That single rule also settles the case that looks like
// it needs its own — replies crossing in flight. If support wrote at 30h and
// the customer at 29h, support is waiting and the customer is not, because
// answering is itself proof of having read. An earlier draft asked "is there a
// message from the other side that you have not read", which reminded support
// about a question they had already replied to.
//
// System notes are skipped when finding that last message. A status change is
// narration rather than a question, and the one edge worth mailing (resolved)
// has its own mail already — so a note must not make a side look like it owes
// an answer, nor stop the side that genuinely does from being reminded.
//
// msgs is the ticket's thread; order does not matter, the newest is found by
// timestamp.
func ticketNudge(t core.Ticket, msgs []core.TicketMessage, now time.Time, after time.Duration) (NudgeSide, bool) {
	// A resolved or closed ticket is finished. Nobody owes anyone a reply, and
	// a reminder about a ticket you deliberately closed reads as a bug.
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
	// Whoever did not write it is the one waiting.
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
