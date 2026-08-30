// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/support"
)

// The sweep around the rule: who it walks, what it records, and the two ways a
// reminder system fails in production — sending N copies because N nodes swept,
// and sending the same reminder every tick forever.

type sentNudge struct {
	ticket  string
	side    NudgeSide
	waiting time.Duration
}

func nudgeFixture(t *testing.T, msgs ...core.TicketMessage) (*TicketNudgeSweeper, *[]sentNudge, core.TicketStore) {
	t.Helper()
	store := support.NewMemTicketStore()
	ctx := context.Background()
	tk := core.Ticket{
		ID: "tk1", Tenant: "acme", Workspace: "main", CreatedBy: "alice",
		Subject: "Invoice flow", Status: core.TicketAwaitingSupport,
		CreatedAt: at(72), UpdatedAt: at(72),
	}
	if err := store.Create(ctx, tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	for i, m := range msgs {
		m.ID = string(rune('a' + i))
		m.TicketID = "tk1"
		if err := store.AppendMessage(ctx, m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	var sent []sentNudge
	s := &TicketNudgeSweeper{
		Tickets: store,
		After:   nudgeAfter,
		Now:     func() time.Time { return nudgeNow },
		Notify: func(tk core.Ticket, side NudgeSide, waiting time.Duration) {
			sent = append(sent, sentNudge{tk.ID, side, waiting})
		},
	}
	return s, &sent, store
}

func TestNudgeSweep_RemindsTheSideThatIsWaiting(t *testing.T) {
	s, sent, _ := nudgeFixture(t, fromUser(30))
	n, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 || len(*sent) != 1 {
		t.Fatalf("sent %d/%v, want 1", n, *sent)
	}
	if got := (*sent)[0]; got.side != NudgeSupport || got.waiting != 30*time.Hour {
		t.Errorf("got %+v, want support waiting 30h", got)
	}
}

func TestNudgeSweep_IsSilentOnASecondPass(t *testing.T) {
	// The failure that makes people filter the mailbox: nothing changed, so
	// nothing more should be sent — the first pass has to have recorded itself.
	s, sent, _ := nudgeFixture(t, fromUser(30))
	ctx := context.Background()
	if _, err := s.Sweep(ctx); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if _, err := s.Sweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(*sent) != 1 {
		t.Errorf("sent %d reminders across two passes, want 1", len(*sent))
	}
}

func TestNudgeSweep_RecordsTheReminderOnTheTicket(t *testing.T) {
	s, _, store := nudgeFixture(t, fromUser(30))
	if _, err := s.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got, err := store.Get(context.Background(), "tk1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.SupportNudgedAt.Equal(nudgeNow) {
		t.Errorf("SupportNudgedAt = %v, want %v", got.SupportNudgedAt, nudgeNow)
	}
	if !got.UserNudgedAt.IsZero() {
		t.Errorf("stamped the wrong side: UserNudgedAt = %v", got.UserNudgedAt)
	}
	// Reading and reminding are not activity: bumping UpdatedAt would reorder
	// the queue by "we emailed about it" rather than by what happened.
	if !got.UpdatedAt.Equal(at(72)) {
		t.Errorf("UpdatedAt moved to %v — a reminder is not activity", got.UpdatedAt)
	}
}

func TestNudgeSweep_DoesNothingWhenNotTheLeader(t *testing.T) {
	// Every node running this means every recipient gets one copy per node.
	s, sent, _ := nudgeFixture(t, fromUser(30))
	s.Leader = func() bool { return false }
	n, err := s.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 || len(*sent) != 0 {
		t.Errorf("a non-leader sent %d reminder(s)", len(*sent))
	}
}

func TestNudgeSweep_LeaderNilMeansSingleNode(t *testing.T) {
	s, sent, _ := nudgeFixture(t, fromUser(30))
	s.Leader = nil
	if _, err := s.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(*sent) != 1 {
		t.Errorf("single-node sent %d, want 1", len(*sent))
	}
}

func TestNudgeSweep_SkipsFinishedTickets(t *testing.T) {
	s, sent, store := nudgeFixture(t, fromUser(30))
	ctx := context.Background()
	tk, _ := store.Get(ctx, "tk1")
	tk.Status = core.TicketClosed
	if err := store.Update(ctx, tk); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(*sent) != 0 {
		t.Errorf("reminded about a closed ticket: %v", *sent)
	}
}

func TestNudgeSweep_OffWhenUnconfigured(t *testing.T) {
	// A deployment that has not set a threshold must not start mailing on its
	// own; zero means off, not "immediately".
	s, sent, _ := nudgeFixture(t, fromUser(30))
	s.After = 0
	if _, err := s.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(*sent) != 0 {
		t.Errorf("swept with no threshold configured: %v", *sent)
	}
}

func TestFormatWaited(t *testing.T) {
	// "26h" reads like a machine talking to itself; the reminder says how long
	// a person would say it has been.
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Minute, "1 hour"},
		{time.Hour, "1 hour"},
		{5 * time.Hour, "5 hours"},
		{25 * time.Hour, "1 day"},
		{49 * time.Hour, "2 days"},
	} {
		if got := formatWaited(c.in); got != c.want {
			t.Errorf("formatWaited(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
