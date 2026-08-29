// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// The GDPR cascade in daemon/gdpr.go reaches these stores by type-asserting
// them to narrow capability interfaces rather than widening core.TicketStore &
// co. Those interfaces are unexported in package daemon, so the shapes are
// restated here — if a store stops satisfying one, the cascade silently SKIPS
// it (that is the documented fallback), which is exactly the failure a test has
// to catch: an erasure that quietly scrubs nothing still reports success.
type subjectAnonymizer interface {
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
}

type tenantEraser interface {
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
}

// Compile-time proof that every support store the cascade probes still
// satisfies the shape it is probed for. A method rename would otherwise only
// show up as a skipped step in an erase report nobody reads.
var (
	_ subjectAnonymizer = (*MemTicketStore)(nil)
	_ subjectAnonymizer = (*PgTicketStore)(nil)
	_ subjectAnonymizer = (*MemGrantStore)(nil)
	_ subjectAnonymizer = (*PgGrantStore)(nil)
	_ subjectAnonymizer = (*MemBundleStore)(nil)
	_ subjectAnonymizer = (*PgBundleStore)(nil)

	_ tenantEraser = (*MemTicketStore)(nil)
	_ tenantEraser = (*PgTicketStore)(nil)
	_ tenantEraser = (*MemGrantStore)(nil)
	_ tenantEraser = (*PgGrantStore)(nil)
	_ tenantEraser = (*MemBundleStore)(nil)
	_ tenantEraser = (*PgBundleStore)(nil)
)

func mkMsg(id, ticketID, author, body string, kind core.AuthorKind, at time.Time) core.TicketMessage {
	return core.TicketMessage{
		ID: id, TicketID: ticketID, Author: author, AuthorKind: kind,
		Body: body, CreatedAt: at,
	}
}

// ---- ticket erasure --------------------------------------------------------

// ticketErasureConformance pins the behaviour both ticket stores must share:
// an erased person's identifier disappears from every column that can hold it
// and their own words are cleared, while the org's thread survives with
// everyone else untouched — and the RETURNED COUNT agrees, since the erase
// report shows it to an operator as a number of tickets.
//
// Run against both impls, mirroring ticketStoreLifecycle.
func ticketErasureConformance(t *testing.T, s core.TicketStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	// leaver@acme.com filed the ticket AND is assigned it; stays@acme.com is a
	// colleague on the same thread who must come out unchanged.
	tk := mkTicket("t-erase", "acme", core.TicketOpen, now)
	tk.CreatedBy = "leaver@acme.com"
	tk.AssignedTo = "leaver@acme.com"
	if err := s.Create(ctx, tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AppendMessage(ctx, mkMsg("m1", "t-erase", "leaver@acme.com",
		"my card number is 4111", core.AuthorUser, now)); err != nil {
		t.Fatalf("append m1: %v", err)
	}
	if err := s.AppendMessage(ctx, mkMsg("m2", "t-erase", "stays@acme.com",
		"I see it too", core.AuthorUser, now.Add(time.Second))); err != nil {
		t.Fatalf("append m2: %v", err)
	}

	n, err := s.(subjectAnonymizer).AnonymizeSubject(ctx, "leaver@acme.com")
	if err != nil {
		t.Fatalf("AnonymizeSubject: %v", err)
	}
	// 2 = the one ticket (counted ONCE even though the person was both its
	// author and its assignee) + the one message they wrote. Counting the
	// ticket's two columns separately would report 3 and overstate the erasure.
	if n != 2 {
		t.Errorf("AnonymizeSubject = %d, want 2 (1 ticket + 1 message)", n)
	}

	// The ticket survives — it is the ORG's record — but carries no identity.
	got, err := s.Get(ctx, "t-erase")
	if err != nil {
		t.Fatalf("get after anonymize: %v", err)
	}
	if got.CreatedBy != core.ErasedIdentity {
		t.Errorf("CreatedBy = %q, want %q", got.CreatedBy, core.ErasedIdentity)
	}
	if got.AssignedTo != core.ErasedIdentity {
		t.Errorf("AssignedTo = %q, want %q", got.AssignedTo, core.ErasedIdentity)
	}
	if got.Subject != "Flow won't run" {
		t.Errorf("Subject changed to %q — anonymising must keep the org's record", got.Subject)
	}

	msgs, err := s.ListMessages(ctx, "t-erase")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("thread = %d messages, want 2 (anonymise must not delete)", len(msgs))
	}
	for _, m := range msgs {
		switch m.ID {
		case "m1":
			if m.Author != core.ErasedIdentity {
				t.Errorf("m1 author = %q, want %q", m.Author, core.ErasedIdentity)
			}
			// Their own words go — the row keeps only the shape of what happened.
			if m.Body != "" {
				t.Errorf("m1 body = %q, want cleared", m.Body)
			}
		case "m2":
			if m.Author != "stays@acme.com" || m.Body != "I see it too" {
				t.Errorf("m2 changed: %+v — a colleague must be untouched", m)
			}
		}
	}

	// An empty identifier is a no-op, never a wildcard that erases everyone.
	if n, err := s.(subjectAnonymizer).AnonymizeSubject(ctx, "   "); err != nil || n != 0 {
		t.Errorf("AnonymizeSubject(blank) = %d, %v; want 0, nil", n, err)
	}
	if again, _ := s.Get(ctx, "t-erase"); again.Subject != "Flow won't run" {
		t.Error("blank identifier must not touch any row")
	}
}

func TestMemTicketStore_Erasure(t *testing.T) {
	ticketErasureConformance(t, NewMemTicketStore())
}

// TestMemTicketStore_DeleteByTenant covers the tenantEraser half: an org's
// tickets leave WITH their threads, and a second org on the same store is
// unaffected.
func TestMemTicketStore_DeleteByTenant(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemTicketStore()

	for _, tk := range []core.Ticket{
		mkTicket("a1", "acme", core.TicketOpen, now),
		mkTicket("a2", "acme", core.TicketResolved, now),
		mkTicket("g1", "globex", core.TicketOpen, now),
	} {
		if err := s.Create(ctx, tk); err != nil {
			t.Fatalf("create %s: %v", tk.ID, err)
		}
	}
	_ = s.AppendMessage(ctx, mkMsg("m-a1", "a1", "u@acme.com", "hi", core.AuthorUser, now))
	_ = s.AppendMessage(ctx, mkMsg("m-g1", "g1", "u@globex.com", "hi", core.AuthorUser, now))

	// Counts TICKETS, not messages — the erase report tallies user-visible objects.
	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2 tickets", n)
	}
	if _, err := s.Get(ctx, "a1"); err == nil {
		t.Error("a1 still readable after its tenant was erased")
	}
	if msgs, _ := s.ListMessages(ctx, "a1"); len(msgs) != 0 {
		t.Errorf("a1 thread survived its ticket: %d messages", len(msgs))
	}
	// Globex is untouched.
	if _, err := s.Get(ctx, "g1"); err != nil {
		t.Errorf("globex ticket collateral-damaged: %v", err)
	}
	if msgs, _ := s.ListMessages(ctx, "g1"); len(msgs) != 1 {
		t.Errorf("globex thread = %d messages, want 1", len(msgs))
	}

	// The deleted thread's message IDs are released, so re-filing under the
	// same ID is not rejected as a duplicate by a stale id-set.
	if err := s.Create(ctx, mkTicket("a1", "acme", core.TicketOpen, now)); err != nil {
		t.Fatalf("re-create a1: %v", err)
	}
	if err := s.AppendMessage(ctx, mkMsg("m-a1", "a1", "u@acme.com", "again", core.AuthorUser, now)); err != nil {
		t.Errorf("re-appending a deleted message id failed: %v — id-set leaked", err)
	}

	// Erasing an org with nothing on file is a no-op, not an error.
	if n, err := s.DeleteByTenant(ctx, "nobody"); err != nil || n != 0 {
		t.Errorf("DeleteByTenant(unknown) = %d, %v; want 0, nil", n, err)
	}
}

// ---- grant erasure ---------------------------------------------------------

// grantErasureConformance pins the access-grant trail's erasure contract: the
// grant ROWS stay (they are the record that someone read a tenant's flow, and
// when) but every column that can name a person is scrubbed.
func grantErasureConformance(t *testing.T, s core.GrantStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	// agent-leaver requested it; admin-stays decided it.
	g := reqGrant("g-erase", "agent-leaver", now)
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Decide(ctx, "g-erase", core.GrantApproved, "admin-stays", now, now.Add(time.Hour)); err != nil {
		t.Fatalf("decide: %v", err)
	}

	n, err := s.(subjectAnonymizer).AnonymizeSubject(ctx, "agent-leaver")
	if err != nil {
		t.Fatalf("AnonymizeSubject: %v", err)
	}
	// ONE row changed. agent-leaver is both the subject and the requester of
	// this grant — the ordinary case — so tallying the four columns separately
	// would report 2 and tell the operator two grants were scrubbed.
	if n != 1 {
		t.Errorf("AnonymizeSubject = %d, want 1 row", n)
	}

	got, err := s.Get(ctx, "g-erase")
	if err != nil {
		t.Fatalf("grant row gone after anonymise: %v — the trail must survive", err)
	}
	if got.AgentSubject != core.ErasedIdentity {
		t.Errorf("AgentSubject = %q, want %q", got.AgentSubject, core.ErasedIdentity)
	}
	if got.RequestedBy != core.ErasedIdentity {
		t.Errorf("RequestedBy = %q, want %q", got.RequestedBy, core.ErasedIdentity)
	}
	// The deciding admin is a different person and stays on the record.
	if got.DecidedBy != "admin-stays" {
		t.Errorf("DecidedBy = %q, want admin-stays untouched", got.DecidedBy)
	}
	if got.Tenant != "acme" || got.FlowID != "daily-invoice" {
		t.Errorf("scope changed: %+v — anonymise keeps what was accessed", got)
	}

	// An erased agent's identifier no longer resolves to an active grant, so
	// the scrub also closes the access it described.
	if _, ok, _ := s.ActiveGrant(ctx, "agent-leaver", "acme", "daily-invoice", now); ok {
		t.Error("erased agent still has an active grant")
	}

	if n, err := s.(subjectAnonymizer).AnonymizeSubject(ctx, ""); err != nil || n != 0 {
		t.Errorf("AnonymizeSubject(blank) = %d, %v; want 0, nil", n, err)
	}
}

func TestMemGrantStore_Erasure(t *testing.T) {
	grantErasureConformance(t, NewMemGrantStore())
}

// TestMemGrantStore_ListForAgent covers the agent-scoped listing (the "my
// requests" view) including its newest-first order and its scoping.
func TestMemGrantStore_ListForAgent(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemGrantStore()

	_ = s.Create(ctx, reqGrant("g1", "agent-a", now))
	_ = s.Create(ctx, reqGrant("g2", "agent-a", now.Add(time.Minute)))
	_ = s.Create(ctx, reqGrant("g3", "agent-b", now))

	list, err := s.ListForAgent(ctx, "agent-a")
	if err != nil {
		t.Fatalf("ListForAgent: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("agent-a = %d grants, want 2 (agent-b must not leak)", len(list))
	}
	if list[0].ID != "g2" {
		t.Errorf("first = %s, want g2 (newest request first)", list[0].ID)
	}
	// An agent with no history gets an empty slice, never nil-with-error.
	none, err := s.ListForAgent(ctx, "agent-zzz")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("ListForAgent(unknown) = %v, %v; want empty, nil", none, err)
	}
}

func TestMemGrantStore_DeleteByTenant(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemGrantStore()

	_ = s.Create(ctx, reqGrant("g1", "agent-a", now))
	_ = s.Create(ctx, reqGrant("g2", "agent-b", now))
	other := reqGrant("g3", "agent-a", now)
	other.Tenant = "globex"
	_ = s.Create(ctx, other)

	n, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	// A grant authorises reading ONE org's data; with the org gone it must not
	// linger pointing at a tenant that no longer exists.
	if left, _ := s.ListForTenant(ctx, "acme"); len(left) != 0 {
		t.Errorf("acme grants survived: %d", len(left))
	}
	if left, _ := s.ListForTenant(ctx, "globex"); len(left) != 1 {
		t.Errorf("globex grants = %d, want 1 untouched", len(left))
	}
}

// ---- bundle erasure --------------------------------------------------------

func TestMemBundleStore_Erasure(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	s := NewMemBundleStore()

	b1 := bundleRec("b1", "acme", now)
	b1.CreatedBy = "leaver@vendor.com"
	b2 := bundleRec("b2", "acme", now)
	b2.CreatedBy = "stays@vendor.com"
	b3 := bundleRec("b3", "globex", now)
	b3.CreatedBy = "leaver@vendor.com"
	for _, b := range []core.SupportBundleRecord{b1, b2, b3} {
		if err := s.Create(ctx, b); err != nil {
			t.Fatalf("create %s: %v", b.ID, err)
		}
	}

	n, err := s.AnonymizeSubject(ctx, "leaver@vendor.com")
	if err != nil {
		t.Fatalf("AnonymizeSubject: %v", err)
	}
	if n != 2 {
		t.Errorf("anonymised = %d, want 2 (both orgs' bundles they took)", n)
	}
	got, _ := s.Get(ctx, "b1")
	if got.CreatedBy != core.ErasedIdentity {
		t.Errorf("b1 CreatedBy = %q, want %q", got.CreatedBy, core.ErasedIdentity)
	}
	// The bundle itself survives — it is redacted by construction and still
	// answers the ticket it was taken for.
	if got.Payload == nil || got.FlowID != "daily-invoice" {
		t.Errorf("b1 lost its content: %+v", got)
	}
	if other, _ := s.Get(ctx, "b2"); other.CreatedBy != "stays@vendor.com" {
		t.Errorf("b2 CreatedBy = %q, want untouched", other.CreatedBy)
	}
	if n, err := s.AnonymizeSubject(ctx, ""); err != nil || n != 0 {
		t.Errorf("AnonymizeSubject(blank) = %d, %v; want 0, nil", n, err)
	}

	// Deleting the org takes its bundles: redacted or not, they describe that
	// org's flow structure.
	dn, err := s.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if dn != 2 {
		t.Errorf("deleted = %d, want 2", dn)
	}
	if _, err := s.Get(ctx, "b1"); err == nil {
		t.Error("b1 readable after its tenant was erased")
	}
	if _, err := s.Get(ctx, "b3"); err != nil {
		t.Errorf("globex bundle collateral-damaged: %v", err)
	}
}

// ---- support-agent role erasure -------------------------------------------

// TestMemAgentStore_AnonymizeGrantedBy covers the roleRevoker half of the
// cascade: when the OPERATOR who granted someone else's support-agent role is
// erased, their email must leave the grantee's row — the grantee keeps the role.
func TestMemAgentStore_AnonymizeGrantedBy(t *testing.T) {
	ctx := context.Background()
	s := NewMemAgentStore()

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
	// Erasing the granter must not revoke the grantee's role — those are two
	// different people, and the cascade has a separate Revoke for the grantee.
	if !s.Granted("agent-a@vendor.com") {
		t.Error("grantee lost the support-agent role when its granter was erased")
	}

	if _, err := s.AnonymizeGrantedBy(ctx, "  "); err == nil {
		t.Error("AnonymizeGrantedBy(blank) should fail, not scrub every row")
	}
}

// TestMemAgentStore_AnonymizeGrantedByNormalizes is the memory twin of
// TestPgAgentStore_AnonymizeGrantedByNormalizes. Grant() normalizes the
// grantee's email but stores grantedBy exactly as the admin form supplied it,
// so erasure has to compare on the normalized form or the erased person's
// address survives in the granter column.
func TestMemAgentStore_AnonymizeGrantedByNormalizes(t *testing.T) {
	ctx := context.Background()
	s := NewMemAgentStore()
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
	if len(list) != 1 || list[0].GrantedBy != core.ErasedIdentity {
		t.Errorf("GrantedBy = %+v, want %q — the erased email survived erasure",
			list, core.ErasedIdentity)
	}
}

// ---- run-snapshot projection ----------------------------------------------

// TestRunSnapshotFromRecords covers the adapter feeding core.BuildSupportBundle.
// Its contract is narrow but load-bearing: pass the RAW refs and errors through
// (core owns redaction) and never read the stored graph JSON.
func TestRunSnapshotFromRecords(t *testing.T) {
	enq := time.Unix(1_700_000_000, 0).UTC()
	started := enq.Add(time.Second)
	finished := enq.Add(2 * time.Second)

	runRec := core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "daily-invoice",
		Tenant: "acme", Status: core.JobStatusFailed,
		// Must be ignored: the bundle's structure is rebuilt from the redacted
		// core.Graph, never from this raw payload.
		GraphPayload: []byte(`{"nodes":[{"params":{"api_key":"sk_live_leak"}}]}`),
		Result:       &core.Result{Error: &core.JobError{Message: "boom", Details: "raw detail"}},
		EnqueuedAt:   enq, StartedAt: &started, FinishedAt: &finished,
	}
	nodeRecs := []core.JobRecord{
		{
			ID: "n-1", Kind: core.JobKindNode, GraphRunID: "run-1", NodeID: "charge",
			Status: core.JobStatusFailed, Attempt: 2,
			Result: &core.Result{
				Error:  &core.JobError{Message: "declined", Details: "card 4111"},
				Output: map[string]core.Ref{"out": {MIME: "application/json", Inline: map[string]any{"secret": "x"}}},
			},
			StartedAt: &started, FinishedAt: &finished,
		},
		// A node that never ran: nil Result must not panic or invent an error.
		{ID: "n-2", Kind: core.JobKindNode, GraphRunID: "run-1", NodeID: "notify", Status: core.JobStatusQueued},
	}

	rs := RunSnapshotFromRecords(runRec, nodeRecs)

	if rs.RunID != "run-1" || rs.Status != core.JobStatusFailed {
		t.Errorf("run identity wrong: %+v", rs)
	}
	if rs.EnqueuedAt == nil || !rs.EnqueuedAt.Equal(enq) {
		t.Errorf("EnqueuedAt = %v, want %v", rs.EnqueuedAt, enq)
	}
	// EnqueuedAt is a *time.Time taken from a value field — it must not alias
	// the record, or a later mutation would rewrite the snapshot.
	if rs.EnqueuedAt == &runRec.EnqueuedAt {
		t.Error("EnqueuedAt aliases the record's field")
	}
	if rs.StartedAt != runRec.StartedAt || rs.FinishedAt != runRec.FinishedAt {
		t.Error("Started/FinishedAt not carried through")
	}
	if rs.Error == nil || rs.Error.Message != "boom" || rs.Error.Details != "raw detail" {
		t.Errorf("run error = %+v, want the RAW error (core redacts later)", rs.Error)
	}

	if len(rs.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(rs.Nodes))
	}
	n1 := rs.Nodes[0]
	if n1.NodeID != "charge" || n1.Status != core.JobStatusFailed || n1.Attempt != 2 {
		t.Errorf("node 1 = %+v", n1)
	}
	if n1.Error == nil || n1.Error.Details != "card 4111" {
		t.Errorf("node error = %+v, want raw details passed through", n1.Error)
	}
	if inline, _ := n1.Output["out"].Inline.(map[string]any); len(n1.Output) != 1 || inline["secret"] != "x" {
		t.Errorf("node output = %+v, want the raw inline ref", n1.Output)
	}
	n2 := rs.Nodes[1]
	if n2.NodeID != "notify" || n2.Error != nil || n2.Output != nil {
		t.Errorf("node with nil Result = %+v, want zero error/output", n2)
	}

	// No node-records is a real case (a run that failed before dispatch): the
	// slice must be empty-non-nil so the bundle encodes [] rather than null.
	empty := RunSnapshotFromRecords(runRec, nil)
	if empty.Nodes == nil || len(empty.Nodes) != 0 {
		t.Errorf("Nodes = %v, want empty non-nil", empty.Nodes)
	}
}
