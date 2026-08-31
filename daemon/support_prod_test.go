// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// testPool opens the integration database, skipping when DAZYFLOW_TEST_DB is
// unset — same gate as support_pg_test.go. These tests TRUNCATE, so point it at
// a throwaway database, never a dev one you care about.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres support tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The support surface used to outlive the org that created it: deleting a
// tenant left its tickets, chat, bundles and grants behind forever. These lock
// in the erasure contract gdpr.go now depends on.

func seedTicket(t *testing.T, s *support.MemTicketStore, id, tenant string, status core.TicketStatus, updated time.Time) {
	t.Helper()
	if err := s.Create(context.Background(), core.Ticket{
		ID: id, Tenant: tenant, CreatedBy: "u@" + tenant, Subject: "s-" + id,
		Status: status, CreatedAt: updated, UpdatedAt: updated,
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

func TestMemTicketStore_DeleteByTenant(t *testing.T) {
	ctx := context.Background()
	s := support.NewMemTicketStore()
	now := time.Unix(1_700_000_000, 0).UTC()
	seedTicket(t, s, "t1", "acme", core.TicketAwaitingSupport, now)
	seedTicket(t, s, "t2", "acme", core.TicketResolved, now)
	seedTicket(t, s, "t3", "other", core.TicketAwaitingSupport, now)
	for _, id := range []string{"t1", "t2", "t3"} {
		if err := s.AppendMessage(ctx, core.TicketMessage{
			ID: "m-" + id, TicketID: id, AuthorKind: core.AuthorUser, Body: "hi", CreatedAt: now,
		}); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2 (tickets, not messages)", n)
	}
	if _, err := s.Get(ctx, "t1"); err == nil {
		t.Error("acme ticket survived erasure")
	}
	// The other org is untouched — erasure is tenant-scoped, not a truncate.
	if _, err := s.Get(ctx, "t3"); err != nil {
		t.Errorf("other tenant's ticket was deleted: %v", err)
	}
	if msgs, _ := s.ListMessages(ctx, "t3"); len(msgs) != 1 {
		t.Errorf("other tenant's thread = %d msgs, want 1", len(msgs))
	}
	// The erased tickets' message IDs are released, so a later ticket can reuse
	// them without tripping the cross-thread dedupe.
	if err := s.AppendMessage(ctx, core.TicketMessage{
		ID: "m-t1", TicketID: "t3", AuthorKind: core.AuthorUser, Body: "reused", CreatedAt: now,
	}); err != nil {
		t.Errorf("message id not released by erasure: %v", err)
	}
}

func TestMemBundleStore_DeleteByTenant(t *testing.T) {
	ctx := context.Background()
	s := support.NewMemBundleStore()
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, tc := range []struct{ id, tenant string }{{"b1", "acme"}, {"b2", "acme"}, {"b3", "other"}} {
		g := core.Graph{ID: "f", Tenant: tc.tenant, Workspace: "main"}
		rec, err := core.NewSupportBundleRecord(tc.id, "agent", now,
			core.BuildSupportBundle(g, nil, nil, core.RedactStructureOnly))
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if err := s.Create(ctx, rec); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	if _, err := s.Get(ctx, "b3"); err != nil {
		t.Errorf("other tenant's bundle was deleted: %v", err)
	}
}

func TestMemGrantStore_DeleteByTenant(t *testing.T) {
	ctx := context.Background()
	s := support.NewMemGrantStore()
	now := time.Unix(1_700_000_000, 0).UTC()
	mk := func(id, tenant string) core.AccessGrant {
		return core.AccessGrant{
			ID: id, Tenant: tenant, FlowID: "f", AgentSubject: "a",
			Status: core.GrantRequested, RequestedAt: now, RequestedBy: "a",
		}
	}
	for _, g := range []core.AccessGrant{mk("g1", "acme"), mk("g2", "acme"), mk("g3", "other")} {
		if err := s.Create(ctx, g); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	left, _ := s.ListForTenant(ctx, "other")
	if len(left) != 1 {
		t.Errorf("other tenant's grants = %d, want 1", len(left))
	}
}

// Postgres-backed counterparts, gated on DAZYFLOW_TEST_DB like the other
// integration tests. The Pg paths carry the real risk here — the ticket erase
// and prune both span two tables in one transaction.
// A person's erasure must scrub them out of the support history without taking
// the org's threads with them — the Postgres side of
// support.PgTicketStore.AnonymizeSubject / support.PgGrantStore.AnonymizeSubject.
func TestPgSupportAnonymizeSubject(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ts, err := support.NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("ticket store: %v", err)
	}
	gs, err := support.NewPgGrantStore(ctx, pool)
	if err != nil {
		t.Fatalf("grant store: %v", err)
	}
	for _, q := range []string{
		"TRUNCATE support_ticket_messages", "TRUNCATE support_tickets", "TRUNCATE access_grants",
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	now := time.Now().UTC()
	const gone = "alice@example.com"
	const agent = "agent@vendor.test"
	if err := ts.Create(ctx, core.Ticket{
		ID: "t1", Tenant: "acme", CreatedBy: gone, AssignedTo: agent, Subject: "s",
		Status: core.TicketAwaitingSupport, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	for _, m := range []core.TicketMessage{
		{ID: "m1", TicketID: "t1", Author: gone, AuthorKind: core.AuthorUser, Body: "secret detail", CreatedAt: now},
		{ID: "m2", TicketID: "t1", Author: agent, AuthorKind: core.AuthorSupport, Body: "on it", CreatedAt: now},
	} {
		if err := ts.AppendMessage(ctx, m); err != nil {
			t.Fatalf("append %s: %v", m.ID, err)
		}
	}
	if err := gs.Create(ctx, core.AccessGrant{
		ID: "g1", Tenant: "acme", FlowID: "f", AgentSubject: agent,
		RequestedAt: now, RequestedBy: agent,
	}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	// Create only ever writes a 'requested' row; the approver lands in
	// decided_by through Decide, which is where the erased user's address is.
	if err := gs.Decide(ctx, "g1", core.GrantApproved, gone, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("decide grant: %v", err)
	}

	n, err := ts.AnonymizeSubject(ctx, gone)
	if err != nil {
		t.Fatalf("anonymize tickets: %v", err)
	}
	if n != 2 { // the ticket's created_by + her one message
		t.Errorf("ticket rows changed = %d, want 2", n)
	}
	gn, err := gs.AnonymizeSubject(ctx, gone)
	if err != nil {
		t.Fatalf("anonymize grants: %v", err)
	}
	if gn != 1 {
		t.Errorf("grant rows changed = %d, want 1", gn)
	}

	tkt, err := ts.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("the org's ticket was deleted: %v", err)
	}
	if tkt.CreatedBy == gone {
		t.Error("created_by still carries the erased address")
	}
	if tkt.AssignedTo != agent {
		t.Errorf("assignee scrubbed but isn't the erased user: %q", tkt.AssignedTo)
	}
	msgs, err := ts.ListMessages(ctx, "t1")
	if err != nil || len(msgs) != 2 {
		t.Fatalf("thread = %d messages, %v", len(msgs), err)
	}
	for _, m := range msgs {
		if m.Author == gone {
			t.Error("message author still carries the erased address")
		}
		if m.ID == "m1" && m.Body != "" {
			t.Errorf("erased user's body survived: %q", m.Body)
		}
		if m.ID == "m2" && m.Body != "on it" {
			t.Errorf("the agent's body was scrubbed: %q", m.Body)
		}
	}
	g, err := gs.Get(ctx, "g1")
	if err != nil {
		t.Fatalf("grant deleted rather than anonymised: %v", err)
	}
	if g.DecidedBy == gone {
		t.Error("decided_by still carries the erased address")
	}
	if g.AgentSubject != agent {
		t.Errorf("the agent's own subject was scrubbed: %q", g.AgentSubject)
	}
	// An empty identifier must be a no-op, not a table-wide wipe.
	if n, err := ts.AnonymizeSubject(ctx, "  "); err != nil || n != 0 {
		t.Errorf("blank ident = %d, %v; want 0, nil", n, err)
	}
	if tkt, _ := ts.Get(ctx, "t1"); tkt.AssignedTo != agent {
		t.Error("a blank identifier scrubbed rows it should have ignored")
	}
}

func TestPgSupportEraseAndPrune(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ts, err := support.NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("ticket store: %v", err)
	}
	bs, err := support.NewPgBundleStore(ctx, pool)
	if err != nil {
		t.Fatalf("bundle store: %v", err)
	}
	gs, err := support.NewPgGrantStore(ctx, pool)
	if err != nil {
		t.Fatalf("grant store: %v", err)
	}
	for _, q := range []string{
		"TRUNCATE support_ticket_messages", "TRUNCATE support_tickets",
		"TRUNCATE support_bundles", "TRUNCATE access_grants",
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}

	now := time.Now().UTC()
	old := now.Add(-400 * 24 * time.Hour) // past a 365d window
	mkTicket := func(id, tenant string, st core.TicketStatus, at time.Time) {
		if err := ts.Create(ctx, core.Ticket{
			ID: id, Tenant: tenant, CreatedBy: "u@x", Subject: "s", Status: st,
			CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := ts.AppendMessage(ctx, core.TicketMessage{
			ID: "m-" + id, TicketID: id, AuthorKind: core.AuthorUser, Body: "b", CreatedAt: at,
		}); err != nil {
			t.Fatalf("msg %s: %v", id, err)
		}
	}
	mkTicket("keep-open", "acme", core.TicketAwaitingSupport, old) // old but OPEN
	mkTicket("sweep-me", "acme", core.TicketResolved, old)         // old + closed
	mkTicket("recent", "acme", core.TicketResolved, now)           // closed but recent

	// --- Prune: only the old CLOSED ticket goes, and its thread with it -------
	n, err := ts.Prune(ctx, 365*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	if _, err := ts.Get(ctx, "keep-open"); err != nil {
		t.Error("an OPEN ticket was pruned — backlog must never be swept away")
	}
	if _, err := ts.Get(ctx, "recent"); err != nil {
		t.Error("a ticket inside the retention window was pruned")
	}
	if _, err := ts.Get(ctx, "sweep-me"); err == nil {
		t.Error("old closed ticket survived the sweep")
	}
	var orphans int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM support_ticket_messages WHERE ticket_id = 'sweep-me'`).Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("pruning left %d orphaned message(s)", orphans)
	}

	// --- Prune bundles: one referenced by an OPEN ticket must survive --------
	mkBundle := func(id, tenant string, at time.Time) {
		g := core.Graph{ID: "f", Tenant: tenant, Workspace: "main"}
		rec, err := core.NewSupportBundleRecord(id, "agent", at,
			core.BuildSupportBundle(g, nil, nil, core.RedactStructureOnly))
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		if err := bs.Create(ctx, rec); err != nil {
			t.Fatalf("bundle create: %v", err)
		}
	}
	mkBundle("b-old", "acme", old)
	mkBundle("b-live", "acme", old)
	mkBundle("b-resolved", "acme", old)
	openT, _ := ts.Get(ctx, "keep-open")
	openT.BundleID = "b-live"
	if err := ts.Update(ctx, openT); err != nil {
		t.Fatalf("attach bundle: %v", err)
	}
	// The regression case: a ticket that is RESOLVED but still inside its own
	// retention window (recent updated_at) attached to an OLD bundle. The two
	// prunes key on different timestamps, so keying the bundle on "no OPEN
	// ticket references it" swept this bundle while its ticket lived on — and
	// the ticket's "View diagnostic" 404'd.
	resolvedT, _ := ts.Get(ctx, "recent")
	resolvedT.BundleID = "b-resolved"
	if err := ts.Update(ctx, resolvedT); err != nil {
		t.Fatalf("attach bundle to resolved ticket: %v", err)
	}
	bn, err := bs.Prune(ctx, 365*24*time.Hour, 100)
	if err != nil {
		t.Fatalf("bundle prune: %v", err)
	}
	if bn != 1 {
		t.Errorf("bundles pruned = %d, want 1 (only the unreferenced one)", bn)
	}
	if _, err := bs.Get(ctx, "b-live"); err != nil {
		t.Error("pruned a bundle still referenced by an open ticket — 'View diagnostic' would 404")
	}
	if _, err := bs.Get(ctx, "b-resolved"); err != nil {
		t.Error("pruned a bundle whose resolved ticket is still stored — 'View diagnostic' would 404")
	}
	if _, err := bs.Get(ctx, "b-old"); err == nil {
		t.Error("an unreferenced, past-retention bundle survived the sweep")
	}

	// --- Erase: the whole org leaves together --------------------------------
	if err := gs.Create(ctx, core.AccessGrant{
		ID: "g1", Tenant: "acme", FlowID: "f", AgentSubject: "a",
		Status: core.GrantRequested, RequestedAt: now, RequestedBy: "a",
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	tn, err := ts.DeleteByTenant(ctx, "acme")
	if err != nil || tn == 0 {
		t.Fatalf("ticket erase = %d, %v", tn, err)
	}
	if bn, err := bs.DeleteByTenant(ctx, "acme"); err != nil || bn == 0 {
		t.Fatalf("bundle erase = %d, %v", bn, err)
	}
	if gn, err := gs.DeleteByTenant(ctx, "acme"); err != nil || gn != 1 {
		t.Fatalf("grant erase = %d, %v", gn, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM support_ticket_messages`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Errorf("%d message(s) outlived their org", left)
	}
}

// --- Notification routing ---------------------------------------------------
//
// The transport (SMTP) has no seam worth faking, so these cover the decisions:
// who hears about what, and that a deployment with no mailer stays silent
// instead of panicking.

func TestSupportQueueRecipient(t *testing.T) {
	assigned := core.Ticket{AssignedTo: "agent@vendor.test"}
	if got := supportQueueRecipient(assigned, "inbox@vendor.test"); got != "agent@vendor.test" {
		t.Errorf("claimed ticket -> %q, want the owning agent", got)
	}
	unassigned := core.Ticket{}
	if got := supportQueueRecipient(unassigned, "inbox@vendor.test"); got != "inbox@vendor.test" {
		t.Errorf("unclaimed ticket -> %q, want the shared inbox", got)
	}
	if got := supportQueueRecipient(unassigned, ""); got != "" {
		t.Errorf("no owner and no inbox -> %q, want silence", got)
	}
}

func TestTicketURLFor_AudienceRoutes(t *testing.T) {
	h := &HTTPGateway{svc: &Service{PublicBaseURL: "https://app.example.com/"}}
	tk := core.Ticket{ID: "abc123", Tenant: "acme"}
	// An agent following the customer's URL would land on a ticket outside
	// their tenant, so the two audiences get different routes.
	//
	// The customer's view is tenant-scoped, so their link is pinned to the
	// filing org — without it, a member of several orgs opens the mail in the
	// wrong one and loadTicketForTenant answers "no ticket with that id".
	if got := h.ticketURLFor(tk, false); got != "https://app.example.com/support/abc123?org=acme" {
		t.Errorf("user URL = %q", got)
	}
	// The agent queue is cross-tenant by design, so it is NOT pinned: agents
	// generally aren't members of the filing org, and moving them there would
	// be wrong as well as useless.
	if got := h.ticketURLFor(tk, true); got != "https://app.example.com/support/queue/abc123" {
		t.Errorf("agent URL = %q", got)
	}
	// A single-tenant deployment carries no tenant on the ticket; the bare link
	// is unambiguous there.
	solo := core.Ticket{ID: "abc123"}
	if got := h.ticketURLFor(solo, false); got != "https://app.example.com/support/abc123" {
		t.Errorf("tenantless user URL = %q", got)
	}
	// No public base URL configured: no link rather than a broken relative one.
	bare := &HTTPGateway{svc: &Service{}}
	if got := bare.ticketURLFor(tk, false); got != "" {
		t.Errorf("URL without a public base = %q, want empty", got)
	}
}

func TestSupportNotify_NoMailerIsSilent(t *testing.T) {
	// A self-host with no SMTP configured must not panic on every reply.
	h := &HTTPGateway{svc: &Service{}}
	tk := core.Ticket{ID: "t1", Tenant: "acme", CreatedBy: "u@acme.test", Subject: "s"}
	h.notifySupportReplied(tk)
	h.notifyTicketResolved(tk)
	h.notifyUserReplied(tk)
	h.notifyTicketFiled(tk)
}

// --- Rate limiting ----------------------------------------------------------

func TestSupportWriteRateLimit(t *testing.T) {
	h := &HTTPGateway{SupportRateLimit: newIPRateLimiter(60, 3)}
	p := core.Principal{Subject: "user@acme.test", Tenant: "acme"}
	allowed := 0
	for i := 0; i < 10; i++ {
		rw := httptest.NewRecorder()
		if h.allowSupportWrite(rw, p) {
			allowed++
			continue
		}
		// The refusal must be actionable, not a bare 429.
		if rw.Code != http.StatusTooManyRequests {
			t.Fatalf("refusal status = %d, want 429", rw.Code)
		}
		if rw.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After — the client can't know when to retry")
		}
	}
	if allowed == 0 || allowed >= 10 {
		t.Errorf("allowed %d of 10 — burst should permit a few then throttle", allowed)
	}

	// Throttling is per SUBJECT: one noisy user must not lock out everyone
	// else behind the same office NAT.
	other := core.Principal{Subject: "colleague@acme.test", Tenant: "acme"}
	if !h.allowSupportWrite(httptest.NewRecorder(), other) {
		t.Error("a different subject was throttled by someone else's traffic")
	}
}

func TestSupportWriteRateLimit_Unset(t *testing.T) {
	// No limiter configured (a hand-built gateway in tests) must allow through
	// rather than deny-by-default.
	h := &HTTPGateway{}
	if !h.allowSupportWrite(httptest.NewRecorder(), core.Principal{Subject: "u"}) {
		t.Error("nil limiter should not block")
	}
}

// --- Notification opt-out ---------------------------------------------------

func TestEmailOnSupportReplyEnabled_DefaultsOn(t *testing.T) {
	var unset auth.NotifyPrefs
	if !unset.EmailOnSupportReplyEnabled() {
		t.Error("unset preference must default ON — a reply nobody hears about is no reply")
	}
	off := false
	if (auth.NotifyPrefs{EmailOnSupportReply: &off}).EmailOnSupportReplyEnabled() {
		t.Error("explicit false must be honoured")
	}
	on := true
	if !(auth.NotifyPrefs{EmailOnSupportReply: &on}).EmailOnSupportReplyEnabled() {
		t.Error("explicit true must be honoured")
	}
	// The two preferences are independent — turning off failure mail must not
	// silence support replies.
	if !(auth.NotifyPrefs{EmailOnFlowFailure: &off}).EmailOnSupportReplyEnabled() {
		t.Error("flow-failure opt-out leaked into support mail")
	}
}

// --- Queue summary cache ----------------------------------------------------
//
// The summary is a full-table GROUP BY (the tiles count every ticket, not a
// page). Measured at 200k rows it ran ~30ms alone but p50 111ms / p95 182ms
// under 20 concurrent agents, and the dashboard calls it on every filter click.
// It's cached for a few seconds, single-flighted, and invalidated on write.
func TestPgQueueSummary_CacheAndInvalidation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ts, err := support.NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, q := range []string{"TRUNCATE support_ticket_messages", "TRUNCATE support_tickets"} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	now := time.Now().UTC()
	mk := func(id string, st core.TicketStatus) core.Ticket {
		return core.Ticket{ID: id, Tenant: "acme", CreatedBy: "u@x", Subject: "s",
			Status: st, CreatedAt: now, UpdatedAt: now}
	}
	if err := ts.Create(ctx, mk("c1", core.TicketAwaitingSupport)); err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := ts.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if first.Total != 1 {
		t.Fatalf("total = %d, want 1", first.Total)
	}

	// A row inserted BEHIND the store's back must not appear while the cache is
	// warm — that's the whole point of the cache.
	if _, err := pool.Exec(ctx,
		`INSERT INTO support_tickets (id,tenant,workspace,created_by,subject,status,flow_id,run_id,bundle_id,assigned_to,created_at,updated_at)
		 VALUES ('sneaky','acme','main','u@x','s','awaiting_support','','','','',$1,$1)`, now); err != nil {
		t.Fatalf("sneak insert: %v", err)
	}
	cached, _ := ts.QueueSummary(ctx)
	if cached.Total != 1 {
		t.Errorf("cache not serving: total = %d, want the stale 1", cached.Total)
	}

	// But the store's OWN write invalidates, so an agent always sees the effect
	// of their own action immediately rather than waiting out the TTL.
	if err := ts.Create(ctx, mk("c2", core.TicketResolved)); err != nil {
		t.Fatalf("create c2: %v", err)
	}
	after, err := ts.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("summary after write: %v", err)
	}
	if after.Total != 3 { // c1 + sneaky + c2
		t.Errorf("total after invalidating write = %d, want 3", after.Total)
	}

	// Update invalidates too (claim / resolve are Updates).
	tk, _ := ts.Get(ctx, "c1")
	tk.Status = core.TicketClosed
	if err := ts.Update(ctx, tk); err != nil {
		t.Fatalf("update: %v", err)
	}
	upd, _ := ts.QueueSummary(ctx)
	if upd.ByStatus[core.TicketClosed] != 1 {
		t.Errorf("closed count after update = %d, want 1 (Update must invalidate)",
			upd.ByStatus[core.TicketClosed])
	}
}

// The summary cache is read and written from every request goroutine, so its
// mutex discipline (including the single-flight flag) has to hold under the
// race detector, not just in a single-threaded test.
func TestPgQueueSummary_ConcurrentAccess(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	ts, err := support.NewPgTicketStore(ctx, pool)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE support_tickets"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		if err := ts.Create(ctx, core.Ticket{
			ID: "cc-" + strconv.Itoa(i), Tenant: "acme", CreatedBy: "u@x", Subject: "s",
			Status: core.TicketAwaitingSupport, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Readers hammering the cache while writers keep invalidating it — the mix
	// that made the stampede visible in the load test.
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := ts.QueueSummary(ctx); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				tk, err := ts.Get(ctx, "cc-"+strconv.Itoa(n))
				if err != nil {
					errs <- err
					return
				}
				tk.UpdatedAt = time.Now().UTC()
				if err := ts.Update(ctx, tk); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent summary: %v", err)
	}

	// After all writes settle, the next read must reflect the true count — a
	// stuck single-flight flag would pin the cache to a stale value forever.
	final, err := ts.QueueSummary(ctx)
	if err != nil {
		t.Fatalf("final: %v", err)
	}
	if final.Total != 20 {
		t.Errorf("total = %d, want 20", final.Total)
	}
}
