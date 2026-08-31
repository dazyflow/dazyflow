// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
)

// The Postgres half of the erasure and retention coverage. Gated on
// DAZYFLOW_TEST_DB like the rest of the support integration tests: the SQL in
// the erase cascade and the two pruners is Postgres-only, so a memory-store
// test cannot reach it and a bug there ships silently.

// erasurePool opens the gated pool and truncates every support table, so each
// test starts from a known-empty schema regardless of order.
func erasurePool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
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
	// The stores create their own tables; make sure they all exist before the
	// truncate so a fresh database doesn't fail on an unknown relation.
	for _, ensure := range []func() error{
		func() error { _, err := NewPgTicketStore(ctx, pool); return err },
		func() error { _, err := NewPgGrantStore(ctx, pool); return err },
		func() error { _, err := NewPgBundleStore(ctx, pool); return err },
		func() error { return EnsurePgAgentSchema(ctx, pool) },
	} {
		if err := ensure(); err != nil {
			t.Fatalf("ensure schema: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `TRUNCATE support_tickets, support_ticket_messages,
		access_grants, support_bundles, support_agents`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return ctx, pool
}

// ---- support-agent store (Postgres) ---------------------------------------

// TestPgAgentStore_Lifecycle covers the whole PgAgentStore, which had no test
// at all: every support agent's session-issue elevation reads its cached
// snapshot, so a write that fails to refresh the cache would leave a revoked
// vendor agent holding the role until the next poll tick.
func TestPgAgentStore_Lifecycle(t *testing.T) {
	ctx, pool := erasurePool(t)

	s, err := NewPgAgentStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgAgentStore: %v", err)
	}
	// A second construction is safe: EnsurePgAgentSchema is idempotent.
	if _, err := NewPgAgentStore(ctx, pool); err != nil {
		t.Fatalf("second NewPgAgentStore: %v", err)
	}

	if s.Granted("agent@vendor.com") {
		t.Fatal("no grants yet")
	}
	// Mixed case + padding on the way in; the store normalizes the key.
	if err := s.Grant(ctx, "  Agent@Vendor.COM ", "operator-1"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Granted reads the snapshot — a write must have refreshed it synchronously
	// rather than waiting for the refresh loop.
	if !s.Granted("agent@vendor.com") || !s.Granted(" AGENT@vendor.com ") {
		t.Error("Granted should normalize and see the new grant immediately")
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d, want 1", len(list))
	}
	if list[0].Email != "agent@vendor.com" {
		t.Errorf("stored email = %q, want normalized", list[0].Email)
	}
	if list[0].GrantedBy != "operator-1" {
		t.Errorf("GrantedBy = %q, want operator-1", list[0].GrantedBy)
	}
	if list[0].CreatedAt.IsZero() {
		t.Error("CreatedAt not populated by the schema default")
	}

	// Re-granting the same agent updates the granter instead of erroring on the
	// primary key (ON CONFLICT DO UPDATE).
	if err := s.Grant(ctx, "agent@vendor.com", "operator-2"); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	list, _ = s.List(ctx)
	if len(list) != 1 || list[0].GrantedBy != "operator-2" {
		t.Errorf("re-grant = %+v, want one row granted by operator-2", list)
	}

	// An empty email is rejected on both write paths — it must never become a
	// row that grants the role to everyone whose email normalizes to "".
	if err := s.Grant(ctx, "   ", "op"); err == nil {
		t.Error("Grant(blank) should fail")
	}
	if err := s.Revoke(ctx, ""); err == nil {
		t.Error("Revoke(blank) should fail")
	}
	if s.Granted("") {
		t.Error("empty email is never granted")
	}

	if err := s.Revoke(ctx, " Agent@Vendor.com "); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s.Granted("agent@vendor.com") {
		t.Error("revoked agent still granted — the snapshot did not refresh")
	}
	// Revoking an agent who holds nothing is a no-op, not an error.
	if err := s.Revoke(ctx, "ghost@vendor.com"); err != nil {
		t.Errorf("Revoke(unknown) = %v, want nil", err)
	}

	// List sorts by email so the admin table is stable across page loads.
	_ = s.Grant(ctx, "zed@vendor.com", "op")
	_ = s.Grant(ctx, "abe@vendor.com", "op")
	list, _ = s.List(ctx)
	if len(list) != 2 || list[0].Email != "abe@vendor.com" {
		t.Errorf("list order = %+v, want abe first", list)
	}
}

// TestPgAgentStore_AnonymizeGrantedBy is the roleRevoker half of the erase
// cascade on Postgres: erasing the OPERATOR who granted a role scrubs their
// email off the grantee's row while the grantee keeps the role.
func TestPgAgentStore_AnonymizeGrantedBy(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgAgentStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgAgentStore: %v", err)
	}

	if err := s.Grant(ctx, "agent-a@vendor.com", "leaver@acme.com"); err != nil {
		t.Fatalf("grant a: %v", err)
	}
	if err := s.Grant(ctx, "agent-b@vendor.com", "leaver@acme.com"); err != nil {
		t.Fatalf("grant b: %v", err)
	}
	if err := s.Grant(ctx, "agent-c@vendor.com", "stays@acme.com"); err != nil {
		t.Fatalf("grant c: %v", err)
	}

	n, err := s.AnonymizeGrantedBy(ctx, "leaver@acme.com")
	if err != nil {
		t.Fatalf("AnonymizeGrantedBy: %v", err)
	}
	if n != 2 {
		t.Errorf("anonymised = %d, want 2", n)
	}
	list, _ := s.List(ctx)
	for _, g := range list {
		switch g.Email {
		case "agent-a@vendor.com", "agent-b@vendor.com":
			if g.GrantedBy != core.ErasedIdentity {
				t.Errorf("%s GrantedBy = %q, want %q", g.Email, g.GrantedBy, core.ErasedIdentity)
			}
		case "agent-c@vendor.com":
			if g.GrantedBy != "stays@acme.com" {
				t.Errorf("agent-c GrantedBy = %q, want untouched", g.GrantedBy)
			}
		}
	}
	// The grantees keep the role: erasing a granter is not a revocation.
	if !s.Granted("agent-a@vendor.com") || !s.Granted("agent-c@vendor.com") {
		t.Error("a grantee lost the role when a granter was erased")
	}
	if _, err := s.AnonymizeGrantedBy(ctx, "  "); err == nil {
		t.Error("AnonymizeGrantedBy(blank) should fail, not scrub every row")
	}
}

// TestPgAgentStore_AnonymizeGrantedByNormalizes is the regression test for a
// GDPR hole: Grant() normalizes the GRANTEE's email but stores grantedBy
// verbatim, so a granter recorded from an admin form as "Operator@Acme.COM"
// never matched the normalized identifier an erasure request arrives with. The
// UPDATE reported 0 rows changed and the erased person's address stayed in
// support_agents.granted_by — on Postgres only, since the memory store already
// normalizes both sides.
func TestPgAgentStore_AnonymizeGrantedByNormalizes(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgAgentStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgAgentStore: %v", err)
	}
	// Stored exactly as typed: mixed case and padding.
	if err := s.Grant(ctx, "agent-a@vendor.com", "  Operator@Acme.COM "); err != nil {
		t.Fatalf("grant: %v", err)
	}

	n, err := s.AnonymizeGrantedBy(ctx, "operator@acme.com")
	if err != nil {
		t.Fatalf("AnonymizeGrantedBy: %v", err)
	}
	if n != 1 {
		t.Errorf("anonymised = %d, want 1 — a mixed-case granter must still match", n)
	}
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Fatalf("list = %d, want 1", len(list))
	}
	if list[0].GrantedBy != core.ErasedIdentity {
		t.Errorf("GrantedBy = %q, want %q — the erased email survived erasure",
			list[0].GrantedBy, core.ErasedIdentity)
	}
}

// ---- ticket erasure + retention (Postgres) --------------------------------

func TestPgTicketStore_Erasure(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgTicketStore: %v", err)
	}
	ticketErasureConformance(t, s)
}

func TestPgTicketStore_DeleteByTenant(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgTicketStore: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	for _, tk := range []core.Ticket{
		mkTicket("a1", "acme", core.TicketOpen, now),
		mkTicket("a2", "acme", core.TicketResolved, now),
		mkTicket("g1", "globex", core.TicketOpen, now),
	} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("create %s: %v", tk.ID, err)
		}
	}
	if err := s.AppendMessage(ctx, mkMsg("m-a1", "a1", "u@acme.com", "hi", core.AuthorUser, now)); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = s.AppendMessage(ctx, mkMsg("m-g1", "g1", "u@globex.com", "hi", core.AuthorUser, now))

	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2 tickets (not messages)", n)
	}
	// Messages go with their tickets — the transaction must not leave a thread
	// pointing at a ticket that is already gone.
	var orphans int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM support_ticket_messages m
		  WHERE NOT EXISTS (SELECT 1 FROM support_tickets t WHERE t.id = m.ticket_id)`).
		Scan(&orphans); err != nil {
		t.Fatalf("orphan count: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d orphaned messages left behind", orphans)
	}
	if left, _ := s.ListForTenant(ctx, "globex", core.TicketListOpts{}); len(left) != 1 {
		t.Errorf("globex tickets = %d, want 1 untouched", len(left))
	}
	if msgs, _ := s.ListMessages(ctx, "g1"); len(msgs) != 1 {
		t.Errorf("globex thread = %d, want 1", len(msgs))
	}
	if n, err := s.DeleteByTenant(ctx, "nobody"); err != nil || n != 0 {
		t.Errorf("DeleteByTenant(unknown) = %d, %v; want 0, nil", n, err)
	}
}

// TestPgTicketStore_Prune covers the retention sweep's central rule: an OPEN
// ticket is never pruned however old it is, because an unanswered ticket is a
// backlog item and deleting one would hide a support failure.
func TestPgTicketStore_Prune(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgTicketStore: %v", err)
	}
	// Prune keys off wall-clock now, so ages are relative to it.
	old := time.Now().UTC().Add(-400 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)

	mk := func(id string, status core.TicketStatus, updated time.Time) {
		tk := mkTicket(id, "acme", status, updated)
		tk.UpdatedAt = updated
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("old-open", core.TicketOpen, old)                // ancient but unanswered — keep
	mk("old-awaiting", core.TicketAwaitingSupport, old) // ancient, live — keep
	mk("old-resolved", core.TicketResolved, old)        // terminal + past window — prune
	mk("old-closed", core.TicketClosed, old)            // terminal + past window — prune
	mk("new-resolved", core.TicketResolved, recent)     // terminal but inside window — keep
	if err := s.AppendMessage(ctx, mkMsg("m-old", "old-resolved", "u@acme.com",
		"thanks", core.AuthorUser, old)); err != nil {
		t.Fatalf("append: %v", err)
	}

	// olderThan <= 0 is an explicit no-op, so a misconfigured sweep can't wipe
	// the table.
	if n, err := s.Prune(ctx, 0, 100); err != nil || n != 0 {
		t.Errorf("Prune(0) = %d, %v; want 0, nil", n, err)
	}
	if n, err := s.Prune(ctx, -time.Hour, 100); err != nil || n != 0 {
		t.Errorf("Prune(negative) = %d, %v; want 0, nil", n, err)
	}

	n, err := s.Prune(ctx, 365*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want 2 (the two terminal, past-window tickets)", n)
	}
	for _, keep := range []string{"old-open", "old-awaiting", "new-resolved"} {
		if _, err := s.Get(ctx, keep); err != nil {
			t.Errorf("%s was pruned but must be kept: %v", keep, err)
		}
	}
	for _, gone := range []string{"old-resolved", "old-closed"} {
		if _, err := s.Get(ctx, gone); err == nil {
			t.Errorf("%s should have been pruned", gone)
		}
	}
	// The pruned ticket's thread went with it.
	if msgs, _ := s.ListMessages(ctx, "old-resolved"); len(msgs) != 0 {
		t.Errorf("pruned ticket left %d messages behind", len(msgs))
	}

	// batch bounds the sweep, so one pass can't lock the table for an hour.
	mk("old-r2", core.TicketResolved, old.Add(time.Minute))
	mk("old-r3", core.TicketResolved, old.Add(2*time.Minute))
	n, err = s.Prune(ctx, 365*24*time.Hour, 1)
	if err != nil {
		t.Fatalf("Prune(batch=1): %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1 (batch limit)", n)
	}
	// Oldest first: old-r2 predates old-r3, so it is the one that went.
	if _, err := s.Get(ctx, "old-r2"); err == nil {
		t.Error("Prune should take the oldest first (old-r2)")
	}
}

// ---- grant erasure (Postgres) ---------------------------------------------

func TestPgGrantStore_Erasure(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgGrantStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgGrantStore: %v", err)
	}
	grantErasureConformance(t, s)
}

func TestPgGrantStore_ListForAgentAndDeleteByTenant(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgGrantStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgGrantStore: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	_ = s.Create(ctx, reqGrant("g1", "agent-a", now))
	_ = s.Create(ctx, reqGrant("g2", "agent-a", now.Add(time.Minute)))
	_ = s.Create(ctx, reqGrant("g3", "agent-b", now))
	other := reqGrant("g4", "agent-a", now)
	other.Tenant = "globex"
	_ = s.Create(ctx, other)

	list, err := s.ListForAgent(ctx, "agent-a")
	if err != nil {
		t.Fatalf("ListForAgent: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("agent-a = %d grants, want 3 across both tenants", len(list))
	}
	if list[0].ID != "g2" {
		t.Errorf("first = %s, want g2 (newest request first)", list[0].ID)
	}
	if none, err := s.ListForAgent(ctx, "agent-zzz"); err != nil || len(none) != 0 {
		t.Errorf("ListForAgent(unknown) = %v, %v; want empty, nil", none, err)
	}

	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 3 {
		t.Errorf("deleted = %d, want 3", n)
	}
	if left, _ := s.ListForTenant(ctx, "globex"); len(left) != 1 {
		t.Errorf("globex grants = %d, want 1 untouched", len(left))
	}
}

// ---- bundle erasure + retention (Postgres) --------------------------------

func TestPgBundleStore_Erasure(t *testing.T) {
	ctx, pool := erasurePool(t)
	s, err := NewPgBundleStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgBundleStore: %v", err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	for _, spec := range []struct{ id, tenant, by string }{
		{"b1", "acme", "leaver@vendor.com"},
		{"b2", "acme", "stays@vendor.com"},
		{"b3", "globex", "leaver@vendor.com"},
	} {
		rec := bundleRec(spec.id, spec.tenant, now)
		rec.CreatedBy = spec.by
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("create %s: %v", spec.id, err)
		}
	}

	n, err := s.AnonymizeSubject(ctx, "leaver@vendor.com")
	if err != nil {
		t.Fatalf("AnonymizeSubject: %v", err)
	}
	if n != 2 {
		t.Errorf("anonymised = %d, want 2", n)
	}
	got, err := s.Get(ctx, "b1")
	if err != nil {
		t.Fatalf("get b1: %v", err)
	}
	if got.CreatedBy != core.ErasedIdentity {
		t.Errorf("b1 CreatedBy = %q, want %q", got.CreatedBy, core.ErasedIdentity)
	}
	// The bundle survives the person: it is redacted by construction and still
	// answers the ticket it was taken for.
	if len(got.Payload) == 0 || got.FlowID != "daily-invoice" {
		t.Errorf("b1 lost its content: %+v", got)
	}
	if other, _ := s.Get(ctx, "b2"); other.CreatedBy != "stays@vendor.com" {
		t.Errorf("b2 CreatedBy = %q, want untouched", other.CreatedBy)
	}
	if n, err := s.AnonymizeSubject(ctx, ""); err != nil || n != 0 {
		t.Errorf("AnonymizeSubject(blank) = %d, %v; want 0, nil", n, err)
	}

	dn, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if dn != 2 {
		t.Errorf("deleted = %d, want 2", dn)
	}
	if _, err := s.Get(ctx, "b3"); err != nil {
		t.Errorf("globex bundle collateral-damaged: %v", err)
	}
}

// TestPgBundleStore_Prune locks down the bundle/ticket pairing invariant that
// bundles.go documents as a past regression: a bundle referenced by ANY ticket
// is kept whatever that ticket's status, because the two pruners key on
// different timestamps (a bundle on created_at, a ticket on updated_at). The
// obvious version — spare only bundles whose ticket is still open — swept the
// bundle of a long-running ticket resolved last week and made "View diagnostic"
// 404 for both the customer and the agent.
func TestPgBundleStore_Prune(t *testing.T) {
	ctx, pool := erasurePool(t)
	bs, err := NewPgBundleStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgBundleStore: %v", err)
	}
	ts, err := NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgTicketStore: %v", err)
	}
	old := time.Now().UTC().Add(-400 * 24 * time.Hour)
	recent := time.Now().UTC().Add(-1 * time.Hour)

	mkBundle := func(id string, at time.Time) {
		if err := bs.Create(ctx, bundleRec(id, "acme", at)); err != nil {
			t.Fatalf("create bundle %s: %v", id, err)
		}
	}
	mkBundle("b-orphan-old", old)    // nothing points at it, past window — prune
	mkBundle("b-orphan-new", recent) // nothing points at it, inside window — keep
	mkBundle("b-open", old)          // referenced by an open ticket — keep
	mkBundle("b-resolved", old)      // referenced by a RESOLVED ticket — keep

	// The two referencing tickets. The resolved one is the regression case:
	// terminal status, but the bundle must still be spared.
	for _, spec := range []struct {
		id, bundle string
		status     core.TicketStatus
	}{
		{"t-open", "b-open", core.TicketOpen},
		{"t-resolved", "b-resolved", core.TicketResolved},
	} {
		tk := mkTicket(spec.id, "acme", spec.status, recent)
		tk.BundleID = spec.bundle
		if err := ts.Create(ctx, tk); err != nil {
			t.Fatalf("create ticket %s: %v", spec.id, err)
		}
	}

	if n, err := bs.Prune(ctx, 0, 100); err != nil || n != 0 {
		t.Errorf("Prune(0) = %d, %v; want 0, nil", n, err)
	}

	n, err := bs.Prune(ctx, 365*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned = %d, want 1 (only the unreferenced past-window bundle)", n)
	}
	if _, err := bs.Get(ctx, "b-orphan-old"); err == nil {
		t.Error("b-orphan-old should have been pruned")
	}
	for _, keep := range []string{"b-orphan-new", "b-open", "b-resolved"} {
		if _, err := bs.Get(ctx, keep); err != nil {
			t.Errorf("%s was pruned but must be kept: %v", keep, err)
		}
	}

	// Once the referencing ticket is gone, the bundle is collectable — and the
	// sweep order (tickets before bundles) means it goes in the same pass.
	if _, err := ts.DeleteByTenant(ctx, "acme"); err != nil {
		t.Fatalf("delete tickets: %v", err)
	}
	n, err = bs.Prune(ctx, 365*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("Prune after ticket delete: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned = %d, want 2 (b-open and b-resolved now unreferenced)", n)
	}
}
