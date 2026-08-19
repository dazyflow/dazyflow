// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
)

// ---- minimal fakes for the Postgres-only stores -----------------------

// DeleteByEmail/DeleteByTenant extend the shared fakeMembershipStore
// (declared in member_roles_test.go) with the GDPR erasure capabilities.
func (f *fakeMembershipStore) DeleteByEmail(_ context.Context, email string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	n := 0
	for k, m := range f.rows {
		if strings.ToLower(m.UserEmail) == email {
			delete(f.rows, k)
			n++
		}
	}
	return n, nil
}
func (f *fakeMembershipStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	n := 0
	for k, m := range f.rows {
		if m.Tenant == tenant {
			delete(f.rows, k)
			n++
		}
	}
	return n, nil
}

type fakeAuditLog struct{ events []core.AuditEvent }

func (f *fakeAuditLog) Append(_ context.Context, e core.AuditEvent) error {
	f.events = append(f.events, e)
	return nil
}
func (f *fakeAuditLog) List(_ context.Context, _ core.AuditQuery) ([]core.AuditEvent, error) {
	return f.events, nil
}
func (f *fakeAuditLog) AnonymizeActor(_ context.Context, actor string) (int, error) {
	n := 0
	for i := range f.events {
		if f.events[i].Actor == actor {
			f.events[i].Actor = "[erased]"
			f.events[i].Detail = ""
			n++
		}
	}
	return n, nil
}
func (f *fakeAuditLog) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	n, kept := 0, f.events[:0]
	for _, e := range f.events {
		if e.Tenant == tenant {
			n++
		} else {
			kept = append(kept, e)
		}
	}
	f.events = kept
	return n, nil
}

type fakeRunLogStore struct{ deletedTenant string }

func (f *fakeRunLogStore) AppendRunLog(context.Context, RunLogEntry) error { return nil }
func (f *fakeRunLogStore) ListRunLogs(context.Context, string, int64, int) ([]RunLogEntry, error) {
	return nil, nil
}
func (f *fakeRunLogStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	f.deletedTenant = tenant
	return 3, nil
}

type fakeOrgAuth struct{ deleted bool }

func (f *fakeOrgAuth) GetOrgAuth(context.Context, string) (auth.OrgAuthConfig, error) {
	return auth.OrgAuthConfig{}, nil
}
func (f *fakeOrgAuth) PutOrgAuth(context.Context, auth.OrgAuthConfig) error { return nil }
func (f *fakeOrgAuth) DeleteOrgAuth(context.Context, string) error          { f.deleted = true; return nil }

type fakeOrgProfiles struct{ deleted bool }

func (f *fakeOrgProfiles) GetOrgProfile(context.Context, string) (auth.OrgProfile, error) {
	return auth.OrgProfile{}, nil
}
func (f *fakeOrgProfiles) PutOrgProfile(context.Context, auth.OrgProfile) error { return nil }
func (f *fakeOrgProfiles) GetOrgProfileBySubdomain(context.Context, string) (auth.OrgProfile, error) {
	return auth.OrgProfile{}, auth.ErrUnknownOrgProfile
}
func (f *fakeOrgProfiles) ListOrgProfiles(context.Context, []string) (map[string]auth.OrgProfile, error) {
	return map[string]auth.OrgProfile{}, nil
}
func (f *fakeOrgProfiles) DeleteOrgProfile(context.Context, string) error {
	f.deleted = true
	return nil
}

// ---- tests ------------------------------------------------------------

func TestEraseUserIdentity_NoResidual(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{Email: email, Subject: email, Tenant: "usr_alice", Workspace: "default"})

	sessions := auth.NewMemSessionStore()
	_ = sessions.PutSession(ctx, auth.Session{ID: "sess1", Subject: email, ExpiresAt: time.Now().Add(time.Hour)})

	keys := auth.NewMemKeyStore()
	_ = keys.PutKey(ctx, auth.APIKey{ID: "k1", Subject: email, Tenant: "usr_alice"})
	_ = keys.PutKey(ctx, auth.APIKey{ID: "k2", Subject: email, Tenant: "usr_alice"})

	invites, _ := auth.OpenJSONInvitationStore("")
	_ = invites.PutInvitation(ctx, auth.Invitation{Token: "inv1", Email: email, Tenant: "acme", ExpiresAt: time.Now().Add(time.Hour)})

	members := newFakeMembershipStore()
	_ = members.PutMembership(ctx, auth.Membership{UserEmail: email, Tenant: "acme"})

	audit := &fakeAuditLog{}
	_ = audit.Append(ctx, core.AuditEvent{Tenant: "usr_alice", Actor: email, Action: "graph.run", Detail: "ip=1.2.3.4"})

	// Support history: a ticket Alice filed in a SHARED org, with a reply from
	// someone else. The thread belongs to that org and must survive her
	// erasure — carrying neither her address nor her words.
	tickets := NewMemTicketStore()
	_ = tickets.Create(ctx, core.Ticket{
		ID: "t1", Tenant: "acme", CreatedBy: email, AssignedTo: "agent@vendor.test",
		Subject: "Flow broke", Status: core.TicketAwaitingSupport,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	_ = tickets.AppendMessage(ctx, core.TicketMessage{
		ID: "m1", TicketID: "t1", Author: email, AuthorKind: core.AuthorUser,
		Body: "my card number is 4242 and my flow is broken", CreatedAt: time.Now(),
	})
	_ = tickets.AppendMessage(ctx, core.TicketMessage{
		ID: "m2", TicketID: "t1", Author: "agent@vendor.test", AuthorKind: core.AuthorSupport,
		Body: "looking into it", CreatedAt: time.Now(),
	})
	grants := NewMemGrantStore()
	_ = grants.Create(ctx, core.AccessGrant{
		ID: "g1", Tenant: "acme", FlowID: "f", AgentSubject: "agent@vendor.test",
		Status: core.GrantApproved, RequestedAt: time.Now(), RequestedBy: "agent@vendor.test",
		DecidedBy: email, ExpiresAt: time.Now().Add(time.Hour),
	})

	h := &HTTPGateway{
		svc:         &Service{AdminKeys: keys},
		Users:       users,
		Sessions:    sessions,
		Memberships: members,
		Invitations: invites,
		Audit:       audit,
		Tickets:     tickets,
		Grants:      grants,
	}

	rep, err := h.eraseUserIdentity(ctx, email)
	if err != nil {
		t.Fatalf("eraseUserIdentity: %v", err)
	}

	// Report counts.
	if !rep.UserDeleted || rep.Sessions != 1 || rep.APIKeys != 2 || rep.Memberships != 1 || rep.Invitations != 1 || rep.AuditEvents != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}

	// No residual: every store empty for this subject.
	if _, err := users.GetByEmail(ctx, email); err == nil {
		t.Error("user row survived erasure")
	}
	if _, err := sessions.GetSession(ctx, "sess1"); err == nil {
		t.Error("session survived erasure")
	}
	if ks, _ := keys.ListBySubject(ctx, email); len(ks) != 0 {
		t.Errorf("api keys survived: %d", len(ks))
	}
	if ms, _ := members.ListByEmail(ctx, email); len(ms) != 0 {
		t.Errorf("memberships survived: %d", len(ms))
	}
	if il, _ := invites.ListByEmail(ctx, email); len(il) != 0 {
		t.Errorf("invitations survived: %d", len(il))
	}
	// Audit pseudonymised, not deleted: the row stays but carries no PII.
	for _, e := range audit.events {
		if e.Actor == email {
			t.Error("audit actor not anonymised")
		}
		if e.Detail != "" {
			t.Errorf("audit detail (with IP) not cleared: %q", e.Detail)
		}
	}

	// Support history: same treatment. The ticket and both messages are still
	// there for the org, with her identity and her words gone and the agent's
	// side untouched. Before this, the erase report said "done" while her
	// address sat in created_by and her message body sat in the thread.
	tkt, err := tickets.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("the org's ticket was deleted along with the user: %v", err)
	}
	if tkt.CreatedBy == email {
		t.Error("ticket created_by still carries the erased address")
	}
	if tkt.AssignedTo != "agent@vendor.test" {
		t.Errorf("assignee was scrubbed but isn't the erased user: %q", tkt.AssignedTo)
	}
	msgs, _ := tickets.ListMessages(ctx, "t1")
	if len(msgs) != 2 {
		t.Fatalf("thread lost messages: %d of 2", len(msgs))
	}
	for _, m := range msgs {
		if m.Author == email {
			t.Error("message author still carries the erased address")
		}
		if m.ID == "m1" && m.Body != "" {
			t.Errorf("erased user's message body survived: %q", m.Body)
		}
		if m.ID == "m2" && m.Body != "looking into it" {
			t.Errorf("someone else's message was scrubbed: %q", m.Body)
		}
	}
	g, err := grants.Get(ctx, "g1")
	if err != nil {
		t.Fatalf("access grant deleted rather than anonymised: %v", err)
	}
	if g.DecidedBy == email {
		t.Error("grant decided_by still carries the erased address")
	}
	if g.AgentSubject != "agent@vendor.test" {
		t.Errorf("the agent's own subject was scrubbed: %q", g.AgentSubject)
	}
	if rep.Tickets == 0 || rep.Grants == 0 {
		t.Errorf("support rows not counted in the report: %+v", rep)
	}
}

func TestDeleteOrgData_NoResidual(t *testing.T) {
	ctx := context.Background()
	const tenant = "acme"

	base := t.TempDir()
	wsBase := filepath.Join(base, "workspace")
	sbBase := filepath.Join(base, "sandbox")

	ws := NewAutoFSWorkspaces(wsBase)
	if _, err := ws.Open(tenant, "default"); err != nil { // materialise the dir
		t.Fatalf("open ws: %v", err)
	}
	sb, err := NewFSSandbox(sbBase)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	if _, err := sb.Root(tenant, "default"); err != nil {
		t.Fatalf("sandbox root: %v", err)
	}

	jobs := jobstore.NewMemory()
	_ = jobs.Enqueue(ctx, core.JobRecord{ID: "r1", Tenant: tenant, GraphID: "g1", Status: core.JobStatusSucceeded})
	_ = jobs.Enqueue(ctx, core.JobRecord{ID: "r2", Tenant: "other", GraphID: "g2", Status: core.JobStatusSucceeded})

	keys := auth.NewMemKeyStore()
	_ = keys.PutKey(ctx, auth.APIKey{ID: "k1", Subject: "bob@x.com", Tenant: tenant})

	members := newFakeMembershipStore()
	_ = members.PutMembership(ctx, auth.Membership{UserEmail: "bob@x.com", Tenant: tenant})
	invites, _ := auth.OpenJSONInvitationStore("")
	_ = invites.PutInvitation(ctx, auth.Invitation{Token: "inv1", Email: "c@x.com", Tenant: tenant, ExpiresAt: time.Now().Add(time.Hour)})
	runlogs := &fakeRunLogStore{}
	orgAuth := &fakeOrgAuth{}
	profiles := &fakeOrgProfiles{}
	audit := &fakeAuditLog{}
	_ = audit.Append(ctx, core.AuditEvent{Tenant: tenant, Actor: "bob@x.com", Action: "graph.run"})

	h := &HTTPGateway{
		svc: &Service{
			Workspaces: ws,
			Jobs:       jobs,
			AdminKeys:  keys,
			RunLogs:    runlogs,
			Engine:     &engine.Engine{Sandbox: sb},
		},
		Memberships: members,
		Invitations: invites,
		OrgAuth:     orgAuth,
		Profiles:    profiles,
		Audit:       audit,
	}

	rep, err := h.deleteOrgData(ctx, tenant)
	if err != nil {
		t.Fatalf("deleteOrgData: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}

	// Directories gone.
	if _, err := os.Stat(filepath.Join(wsBase, tenant)); !os.IsNotExist(err) {
		t.Error("workspace dir survived")
	}
	if _, err := os.Stat(filepath.Join(sbBase, tenant)); !os.IsNotExist(err) {
		t.Error("sandbox dir survived")
	}
	if !rep.WorkspaceWiped || !rep.SandboxWiped {
		t.Errorf("dir wipe flags: %+v", rep)
	}

	// Tenant's jobs gone, other tenant's untouched.
	if r, _ := jobs.DeleteByTenant(ctx, tenant); r != 0 {
		t.Errorf("tenant jobs survived: %d", r)
	}
	if _, err := jobs.Get(ctx, "r2"); err != nil {
		t.Error("other tenant's job was wrongly deleted")
	}

	// Scoped stores cleared.
	if ks, _ := keys.ListByTenant(ctx, tenant); len(ks) != 0 {
		t.Errorf("api keys survived: %d", len(ks))
	}
	if ms, _ := members.ListByTenant(ctx, tenant); len(ms) != 0 {
		t.Errorf("memberships survived: %d", len(ms))
	}
	if il, _ := invites.ListByTenant(ctx, tenant); len(il) != 0 {
		t.Errorf("invitations survived: %d", len(il))
	}
	if !orgAuth.deleted || !profiles.deleted {
		t.Error("org auth/profile not deleted")
	}
	if runlogs.deletedTenant != tenant {
		t.Errorf("run logs not deleted for tenant: %q", runlogs.deletedTenant)
	}
	if len(audit.events) != 0 {
		t.Errorf("audit events survived: %d", len(audit.events))
	}
	if rep.Jobs != 1 || rep.APIKeys != 1 || rep.Memberships != 1 || rep.Invitations != 1 {
		t.Errorf("report counts: %+v", rep)
	}
}

// TestMergeErase_Cov covers mergeErase: counts sum, booleans OR, warnings
// concatenate.
func TestMergeErase_Cov(t *testing.T) {
	a := EraseReport{
		Sessions: 1, APIKeys: 2, Memberships: 3, Invitations: 4,
		AuditEvents: 5, Jobs: 6, RunLogs: 7, BusEvents: 8,
		WorkspaceWiped: true, Warnings: []string{"a"},
	}
	b := EraseReport{
		Sessions: 10, APIKeys: 20, Memberships: 30, Invitations: 40,
		AuditEvents: 50, Jobs: 60, RunLogs: 70, BusEvents: 80,
		SandboxWiped: true, OrgAuthDeleted: true, OrgProfileGone: true,
		Warnings: []string{"b"},
	}
	got := mergeErase(a, b)
	if got.Sessions != 11 || got.APIKeys != 22 || got.Memberships != 33 ||
		got.Invitations != 44 || got.AuditEvents != 55 || got.Jobs != 66 ||
		got.RunLogs != 77 || got.BusEvents != 88 {
		t.Fatalf("counts merged wrong: %+v", got)
	}
	if !got.WorkspaceWiped || !got.SandboxWiped || !got.OrgAuthDeleted || !got.OrgProfileGone {
		t.Fatalf("booleans not OR'd: %+v", got)
	}
	if len(got.Warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", got.Warnings)
	}
}

// TestEraseReport_Warnf covers EraseReport.warnf.
func TestEraseReport_Warnf(t *testing.T) {
	var r EraseReport
	r.warnf("failed %s: %d", "thing", 42)
	if len(r.Warnings) != 1 || r.Warnings[0] != "failed thing: 42" {
		t.Fatalf("warnings = %v", r.Warnings)
	}
}

// TestTenantHasOtherMembers_Cov covers the helper's three legs: nil store,
// sole occupant, and a shared org.
func TestTenantHasOtherMembers_Cov(t *testing.T) {
	h := newGatewayHarness(t)

	// Nil Memberships store -> false (no others known).
	if h.gw.tenantHasOtherMembers(context.Background(), "acme", "a@acme.test") {
		t.Fatal("nil store should report no other members")
	}

	mem := newFakeMembershipStore()
	h.gw.Memberships = mem
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "a@acme.test", Tenant: "acme", Roles: []core.Role{core.TeamRoleEditor()},
	})
	// Sole occupant -> false.
	if h.gw.tenantHasOtherMembers(context.Background(), "acme", "A@Acme.test") {
		t.Fatal("sole occupant should report no other members")
	}
	// Add a second member -> true.
	_ = mem.PutMembership(context.Background(), auth.Membership{
		UserEmail: "b@acme.test", Tenant: "acme", Roles: []core.Role{core.TeamRoleEditor()},
	})
	if !h.gw.tenantHasOtherMembers(context.Background(), "acme", "a@acme.test") {
		t.Fatal("shared org should report other members")
	}
}
