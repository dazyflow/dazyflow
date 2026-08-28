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

	"git.sr.ht/~klahr/dazyflow/core"
)

func mkTicket(id, tenant string, status core.TicketStatus, at time.Time) core.Ticket {
	return core.Ticket{
		ID: id, Tenant: tenant, Workspace: "main", CreatedBy: "user@acme.com",
		Subject: "Flow won't run", Status: status,
		FlowID: "daily-invoice", CreatedAt: at, UpdatedAt: at,
	}
}

// ticketStoreLifecycle exercises the behavior both impls must share, so the
// in-memory and Postgres stores stay identical (mirrors the grant-store tests).
func ticketStoreLifecycle(t *testing.T, s core.TicketStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	if err := s.Create(ctx, mkTicket("t1", "acme", core.TicketAwaitingSupport, now)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(ctx, mkTicket("t1", "acme", core.TicketOpen, now)); !errors.Is(err, errTicketExists) {
		t.Errorf("duplicate create = %v, want errTicketExists", err)
	}
	// A second tenant's ticket must not leak into acme's list.
	if err := s.Create(ctx, mkTicket("t2", "globex", core.TicketOpen, now.Add(time.Minute))); err != nil {
		t.Fatalf("create t2: %v", err)
	}

	got, err := s.Get(ctx, "t1")
	if err != nil || got.Subject != "Flow won't run" {
		t.Fatalf("get t1 = %+v, %v", got, err)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("get missing = %v, want ErrNotFound", err)
	}

	// Tenant scoping.
	acme, _ := s.ListForTenant(ctx, "acme", core.TicketListOpts{})
	if len(acme) != 1 || acme[0].ID != "t1" {
		t.Errorf("acme list = %+v, want [t1]", acme)
	}
	// Status filter.
	none, _ := s.ListForTenant(ctx, "acme", core.TicketListOpts{Status: core.TicketResolved})
	if len(none) != 0 {
		t.Errorf("resolved filter = %d, want 0", len(none))
	}
	// Cross-tenant queue sees both.
	queue, _ := s.ListQueue(ctx, core.TicketListOpts{})
	if len(queue) != 2 {
		t.Errorf("queue = %d, want 2", len(queue))
	}

	// Update: resolve the ticket and bump activity.
	got.Status = core.TicketResolved
	got.AssignedTo = "agent@vendor.com"
	got.BundleID = "b1"
	got.UpdatedAt = now.Add(time.Hour)
	if err := s.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, _ := s.Get(ctx, "t1")
	if reread.Status != core.TicketResolved || reread.AssignedTo != "agent@vendor.com" || reread.BundleID != "b1" {
		t.Errorf("update not persisted: %+v", reread)
	}
	if err := s.Update(ctx, mkTicket("ghost", "acme", core.TicketOpen, now)); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("update missing = %v, want ErrNotFound", err)
	}

	// Messages: chronological, deduped, and rejected for a missing ticket.
	m1 := core.TicketMessage{ID: "m1", TicketID: "t1", Author: "user@acme.com", AuthorKind: core.AuthorUser, Body: "help", CreatedAt: now}
	m2 := core.TicketMessage{ID: "m2", TicketID: "t1", Author: "agent@vendor.com", AuthorKind: core.AuthorSupport, Body: "on it", CreatedAt: now.Add(time.Minute)}
	if err := s.AppendMessage(ctx, m2); err != nil { // append out of order
		t.Fatalf("append m2: %v", err)
	}
	if err := s.AppendMessage(ctx, m1); err != nil {
		t.Fatalf("append m1: %v", err)
	}
	if err := s.AppendMessage(ctx, m1); !errors.Is(err, errTicketMsgExists) {
		t.Errorf("duplicate message = %v, want errTicketMsgExists", err)
	}
	if err := s.AppendMessage(ctx, core.TicketMessage{ID: "mx", TicketID: "ghost", Body: "x"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("append to missing ticket = %v, want ErrNotFound", err)
	}
	msgs, _ := s.ListMessages(ctx, "t1")
	if len(msgs) != 2 || msgs[0].ID != "m1" || msgs[1].ID != "m2" {
		t.Errorf("messages out of order: %+v", msgs)
	}

	// ---- Ownership filters + queue summary (the support dashboard) -----------
	// State here: t1 = acme, resolved, assigned to agent@vendor.com;
	//             t2 = globex, open, unassigned.
	assigned, _ := s.ListQueue(ctx, core.TicketListOpts{AssignedTo: "agent@vendor.com"})
	if len(assigned) != 1 || assigned[0].ID != "t1" {
		t.Errorf("assignee filter = %+v, want [t1]", assigned)
	}
	if other, _ := s.ListQueue(ctx, core.TicketListOpts{AssignedTo: "nobody@vendor.com"}); len(other) != 0 {
		t.Errorf("filter on an agent with no tickets = %d, want 0", len(other))
	}
	unassigned, _ := s.ListQueue(ctx, core.TicketListOpts{Unassigned: true})
	if len(unassigned) != 1 || unassigned[0].ID != "t2" {
		t.Errorf("unassigned filter = %+v, want [t2]", unassigned)
	}
	// Unassigned wins over AssignedTo when both are set (documented in
	// core.TicketListOpts) — the pair must not silently return nothing.
	both, _ := s.ListQueue(ctx, core.TicketListOpts{Unassigned: true, AssignedTo: "agent@vendor.com"})
	if len(both) != 1 || both[0].ID != "t2" {
		t.Errorf("unassigned+assignee = %+v, want unassigned to win with [t2]", both)
	}
	// Filters compose with status, and apply to the tenant listing too.
	if hit, _ := s.ListQueue(ctx, core.TicketListOpts{Unassigned: true, Status: core.TicketResolved}); len(hit) != 0 {
		t.Errorf("unassigned+resolved = %d, want 0 (t2 is open, t1 is assigned)", len(hit))
	}
	if mine, _ := s.ListForTenant(ctx, "acme", core.TicketListOpts{AssignedTo: "agent@vendor.com"}); len(mine) != 1 {
		t.Errorf("tenant listing + assignee filter = %d, want 1", len(mine))
	}
	// Limit bounds the result set.
	if one, _ := s.ListQueue(ctx, core.TicketListOpts{Limit: 1}); len(one) != 1 {
		t.Errorf("limit 1 returned %d rows", len(one))
	}

	sum, err := s.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("queue summary: %v", err)
	}
	if sum.Total != 2 {
		t.Errorf("summary Total = %d, want 2", sum.Total)
	}
	// Only t2 is live; t1 is resolved, so it counts in ByStatus but not in
	// Open/Unassigned/ByAssignee.
	if sum.Open != 1 || sum.Unassigned != 1 {
		t.Errorf("summary Open/Unassigned = %d/%d, want 1/1", sum.Open, sum.Unassigned)
	}
	if sum.ByStatus[core.TicketResolved] != 1 || sum.ByStatus[core.TicketOpen] != 1 {
		t.Errorf("summary ByStatus = %v, want one resolved + one open", sum.ByStatus)
	}
	if len(sum.ByAssignee) != 0 {
		t.Errorf("summary ByAssignee = %v, want empty (the only assigned ticket is resolved)", sum.ByAssignee)
	}
}

func TestMemTicketStore(t *testing.T) {
	ticketStoreLifecycle(t, NewMemTicketStore())
}

// Gated on DAZYFLOW_TEST_DB, like the other Postgres support tests.
func TestPgTicketStore(t *testing.T) {
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
	ticketStoreLifecycle(t, s)
}
