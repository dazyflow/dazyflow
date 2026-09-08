// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

var nudgeNow = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

const nudgeAfter = 24 * time.Hour

func at(h int) time.Time { return nudgeNow.Add(-time.Duration(h) * time.Hour) }

func fromUser(h int) core.TicketMessage {
	return core.TicketMessage{AuthorKind: core.AuthorUser, Author: "alice", CreatedAt: at(h)}
}
func fromSupport(h int) core.TicketMessage {
	return core.TicketMessage{AuthorKind: core.AuthorSupport, Author: "agent", CreatedAt: at(h)}
}
func fromSystem(h int) core.TicketMessage {
	return core.TicketMessage{AuthorKind: core.AuthorSystem, CreatedAt: at(h)}
}

func TestTicketNudge(t *testing.T) {
	t.Parallel()
	open := core.Ticket{Status: core.TicketAwaitingSupport}
	cases := []struct {
		name string
		tk   core.Ticket
		msgs []core.TicketMessage
		want []NudgeSide
	}{{
		name: "nobody has looked at a day-old question",
		tk:   open,
		msgs: []core.TicketMessage{fromUser(30)},
		want: []NudgeSide{NudgeSupport},
	}, {
		name: "a support reply the customer never opened",
		tk:   open,
		msgs: []core.TicketMessage{fromUser(50), fromSupport(30)},
		want: []NudgeSide{NudgeUser},
	}, {
		name: "answering counts as reading — support is not reminded of a question it replied to",
		tk:   core.Ticket{Status: core.TicketAwaitingUser, UserReadAt: at(29)},
		msgs: []core.TicketMessage{fromUser(50), fromSupport(30)},
		want: nil,
	}, {
		name: "opened the ticket, but before the message arrived",
		tk:   core.Ticket{Status: core.TicketAwaitingSupport, SupportReadAt: at(40)},
		msgs: []core.TicketMessage{fromUser(30)},
		want: []NudgeSide{NudgeSupport},
	}, {
		name: "read it and has not answered — that is not a notification problem",
		tk:   core.Ticket{Status: core.TicketAwaitingSupport, SupportReadAt: at(20)},
		msgs: []core.TicketMessage{fromUser(30)},
		want: nil,
	}, {
		name: "still inside the grace period",
		tk:   open,
		msgs: []core.TicketMessage{fromUser(2)},
		want: nil,
	}, {
		name: "already reminded about this exact message",
		tk:   core.Ticket{Status: core.TicketAwaitingSupport, SupportNudgedAt: at(5)},
		msgs: []core.TicketMessage{fromUser(30)},
		want: nil,
	}, {
		name: "reminded before, but the other side has since written again",
		tk:   core.Ticket{Status: core.TicketAwaitingSupport, SupportNudgedAt: at(40)},
		msgs: []core.TicketMessage{fromUser(50), fromUser(30)},
		want: []NudgeSide{NudgeSupport},
	}, {
		name: "replies crossed in flight",
		tk:   open,
		msgs: []core.TicketMessage{fromSupport(30), fromUser(29)},
		want: []NudgeSide{NudgeSupport},
	}, {
		name: "a resolved ticket is finished",
		tk:   core.Ticket{Status: core.TicketResolved},
		msgs: []core.TicketMessage{fromUser(30)},
		want: nil,
	}, {
		name: "a closed ticket is finished",
		tk:   core.Ticket{Status: core.TicketClosed},
		msgs: []core.TicketMessage{fromUser(30)},
		want: nil,
	}, {
		name: "system notes never trigger a reminder",
		tk:   open,
		msgs: []core.TicketMessage{fromSystem(30)},
		want: nil,
	}, {
		// And a note arriving after a question must not make the questioner
		// look like the one who owes an answer.
		name: "a system note does not hide the message underneath it",
		tk:   open,
		msgs: []core.TicketMessage{fromUser(30), fromSystem(2)},
		want: []NudgeSide{NudgeSupport},
	}, {
		name: "your own message is not something you are waiting on",
		tk:   open,
		msgs: []core.TicketMessage{fromUser(30)},
		want: []NudgeSide{NudgeSupport}, // and NOT NudgeUser
	}, {
		name: "an empty thread reminds nobody",
		tk:   open,
		msgs: nil,
		want: nil,
	}, {
		// Chronological order is the contract, but the newest message is found
		// by timestamp rather than by position, so a store that ever returned
		// them out of order would not silently remind about the wrong one.
		name: "picks the newest message, not the last in the slice",
		tk:   core.Ticket{Status: core.TicketAwaitingSupport, SupportReadAt: at(25)},
		msgs: []core.TicketMessage{fromUser(30), fromUser(40)},
		want: nil, // newest is 30h old and was read at 25h
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			side, ok := ticketNudge(c.tk, c.msgs, nudgeNow, nudgeAfter)
			var got []NudgeSide
			if ok {
				got = []NudgeSide{side}
			}
			if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestTicketNudge_ExactBoundaryDoesNotFireEarly(t *testing.T) {
	t.Parallel()
	// Off-by-one at the threshold is the classic way a "daily" reminder becomes
	// two reminders: fire at >= and a sweep at 23:59:59.9 plus clock jitter can
	// double up.
	tk := core.Ticket{Status: core.TicketAwaitingSupport}
	msg := core.TicketMessage{AuthorKind: core.AuthorUser, CreatedAt: nudgeNow.Add(-nudgeAfter)}
	if _, ok := ticketNudge(tk, []core.TicketMessage{msg}, nudgeNow, nudgeAfter); !ok {
		t.Error("exactly at the threshold should fire")
	}
	just := core.TicketMessage{AuthorKind: core.AuthorUser, CreatedAt: nudgeNow.Add(-nudgeAfter + time.Second)}
	if side, ok := ticketNudge(tk, []core.TicketMessage{just}, nudgeNow, nudgeAfter); ok {
		t.Errorf("one second short should not fire: got %v", side)
	}
}
