// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

type nudgeStamper interface {
	core.TicketStore
	StampNudge(ctx context.Context, ticketID string, userSide bool, at time.Time) error
}

// StampNudge writes one side's nudge time and nothing else, so a change made
// to the ticket after the sweeper's snapshot survives it.
func stampNudgeContract(t *testing.T, s nudgeStamper) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := s.Create(ctx, mkTicket("t1", "acme", core.TicketAwaitingSupport, now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Support resolves the ticket after the sweeper took its snapshot.
	cur, err := s.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	cur.Status = core.TicketResolved
	if err := s.Update(ctx, cur); err != nil {
		t.Fatalf("update: %v", err)
	}

	at := now.Add(time.Hour)
	if err := s.StampNudge(ctx, "t1", false, at); err != nil {
		t.Fatalf("StampNudge support: %v", err)
	}
	if err := s.StampNudge(ctx, "t1", true, at.Add(time.Minute)); err != nil {
		t.Fatalf("StampNudge user: %v", err)
	}
	got, err := s.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != core.TicketResolved {
		t.Errorf("status = %q, want resolved: the stamp undid a concurrent change", got.Status)
	}
	if !got.SupportNudgedAt.Equal(at) || !got.UserNudgedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("nudged at = support %v / user %v, want %v / %v",
			got.SupportNudgedAt, got.UserNudgedAt, at, at.Add(time.Minute))
	}
	if err := s.StampNudge(ctx, "nope", true, at); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("StampNudge missing = %v, want ErrNotFound", err)
	}
}

func TestMemTicketStore_StampNudge(t *testing.T) {
	stampNudgeContract(t, NewMemTicketStore())
}

func TestPgTicketStore_StampNudge(t *testing.T) {
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres support tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgTicketStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE support_tickets, support_ticket_messages"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	stampNudgeContract(t, s)
}
