// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// TicketNudgeSweeper walks the open ticket queue and reminds whichever side has
// left something unread past the threshold. The decision for one ticket is
// ticketNudge; this is the part that finds them and records that the mail went.
//
// MUST be leader-gated, unlike RunnerTaskSweeper. That one is safe to run
// everywhere because closing an already-closed task is a no-op, whereas this
// one sends mail: every node running it means every recipient gets N copies of
// the same reminder, which is the fastest way to teach people to filter it.
// Leader is required rather than optional — an unset Leader means single-node,
// where "am I the leader" is trivially yes.
type TicketNudgeSweeper struct {
	Tickets core.TicketStore
	After   time.Duration
	Leader  func() bool
	Notify  func(t core.Ticket, side NudgeSide, waiting time.Duration)
	Now     func() time.Time
}

// ticketNudgeBatch bounds one pass. Large enough that a real queue is covered
// in a single sweep, small enough that a pathological backlog cannot turn one
// tick into thousands of emails — the rest are picked up next tick, and a
// reminder an hour late is not a reminder lost.
const ticketNudgeBatch = 500

func (s *TicketNudgeSweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *TicketNudgeSweeper) Sweep(ctx context.Context) (int, error) {
	if s.Tickets == nil || s.Notify == nil || s.After <= 0 {
		return 0, nil
	}
	if s.Leader != nil && !s.Leader() {
		return 0, nil
	}
	now := s.now()
	tickets, err := s.Tickets.ListQueue(ctx, core.TicketListOpts{Limit: ticketNudgeBatch})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, t := range tickets {
		if t.Status.IsTerminal() {
			continue // cheap skip before paying for the thread
		}
		msgs, err := s.Tickets.ListMessages(ctx, t.ID)
		if err != nil {
			continue // one unreadable thread must not stop the sweep
		}
		side, ok := ticketNudge(t, msgs, now, s.After)
		if !ok {
			continue
		}
		// Stamp BEFORE sending. Mail is best-effort and detached, so a failed
		// send is invisible here — if the stamp came after, a persistently
		// failing recipient would be retried on every tick forever. Erring
		// toward one lost reminder beats erring toward an unbounded loop.
		waiting := now.Sub(lastHumanMessageAt(msgs))
		if side == NudgeUser {
			t.UserNudgedAt = now
		} else {
			t.SupportNudgedAt = now
		}
		if err := s.Tickets.Update(ctx, t); err != nil {
			continue // could not record it — do not send, or it repeats
		}
		s.Notify(t, side, waiting)
		sent++
	}
	return sent, nil
}

func lastHumanMessageAt(msgs []core.TicketMessage) time.Time {
	var at time.Time
	for _, m := range msgs {
		if m.AuthorKind != core.AuthorSystem && m.CreatedAt.After(at) {
			at = m.CreatedAt
		}
	}
	return at
}
