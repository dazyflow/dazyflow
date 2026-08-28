// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"git.sr.ht/~klahr/dazyflow/engine/webapi"
)

// fakePlanStore is a PlanStore that also supports tenant erasure.
type fakePlanStore struct {
	plans map[string]TenantPlan
}

func (f *fakePlanStore) GetPlan(_ context.Context, tenant string) (TenantPlan, error) {
	p, ok := f.plans[tenant]
	if !ok {
		return TenantPlan{Tenant: tenant, Plan: PlanFree}, nil
	}
	return p, nil
}

func (f *fakePlanStore) SetPlan(_ context.Context, p TenantPlan) error {
	f.plans[p.Tenant] = p
	return nil
}

func (f *fakePlanStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	if _, ok := f.plans[tenant]; !ok {
		return 0, nil
	}
	delete(f.plans, tenant)
	return 1, nil
}

// ---- minimal fakes for the Postgres-only stores -----------------------

// DeleteByEmail/DeleteByTenant extend the shared fakeMembershipStore
// (declared in member_roles_test.go) with the GDPR erasure capabilities.
func (f *fakeMembershipStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	ident = strings.ToLower(strings.TrimSpace(ident))
	if ident == "" {
		return 0, nil
	}
	n := 0
	for k, m := range f.rows {
		if strings.ToLower(strings.TrimSpace(m.InvitedBy)) == ident {
			m.InvitedBy = core.ErasedIdentity
			f.rows[k] = m
			n++
		}
	}
	return n, nil
}

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

// List deliberately IGNORES AuditQuery.Actor, modelling a store written before
// that field existed. The export must still not hand one person another's
// events, so this double is what proves assembleExport's own actor re-check is
// load-bearing rather than redundant with the SQL filter.
func (f *fakeAuditLog) List(_ context.Context, _ core.AuditQuery) ([]core.AuditEvent, error) {
	return f.events, nil
}
func (f *fakeAuditLog) AnonymizeActor(_ context.Context, actor string) (int, error) {
	n := 0
	for i := range f.events {
		if f.events[i].Actor == actor {
			f.events[i].Actor = core.ErasedIdentity
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

// TestDeleteOrgData_ErasesSecrets covers the secrets half of the erasure
// cascade. Before DeleteByTenant was wired in, org deletion left every row in
// encrypted_secrets — connector credentials and OAuth tokens belonging to a
// deleted org — sitting in the database with its DEK, still decryptable.
func TestDeleteOrgData_ErasesSecrets(t *testing.T) {
	ctx := context.Background()
	const tenant = "acme"

	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	for _, name := range []string{"slack_token", "gmail_oauth"} {
		if err := es.Put(ctx, tenant, name, "s3cret-"+name); err != nil {
			t.Fatalf("put %s: %v", name, err)
		}
	}
	// A second tenant proves the wipe is scoped, not a table truncate.
	if err := es.Put(ctx, "other", "slack_token", "keep-me"); err != nil {
		t.Fatalf("put other: %v", err)
	}

	h := &HTTPGateway{svc: &Service{}, EncryptedSecrets: es}
	rep, err := h.deleteOrgData(ctx, tenant)
	if err != nil {
		t.Fatalf("deleteOrgData: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}
	if rep.Secrets != 2 {
		t.Errorf("Secrets = %d, want 2", rep.Secrets)
	}

	names, err := es.List(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("secrets survived org erasure: %v", names)
	}
	// Every value is unreachable, not merely unlisted.
	for _, name := range []string{"slack_token", "gmail_oauth"} {
		if v, err := es.GetExact(ctx, tenant, name); err == nil {
			t.Errorf("%s still readable after erasure: %q", name, v)
		}
	}
	// The untouched tenant still resolves — including through the DEK, which
	// is per-tenant and must not have been collateral damage.
	v, err := es.GetExact(ctx, "other", "slack_token")
	if err != nil || v != "keep-me" {
		t.Errorf("other tenant's secret = %q / %v, want %q", v, err, "keep-me")
	}
}

// TestSecretsDeleteByTenant_DropsDEK pins the crypto-shredding half: the
// tenant's wrapped DEK goes with its secrets, and the in-process cache of that
// DEK is evicted. A surviving cached DEK would let a later Put seal a value
// under a key whose wrapped form is gone from the store — ciphertext that no
// restart of this process, and no other process, could ever open.
func TestSecretsDeleteByTenant_DropsDEK(t *testing.T) {
	ctx := context.Background()
	const tenant = "acme"

	store := NewMemSecretsStore()
	es, err := NewEncryptedSecrets(make([]byte, 32), store)
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	if err := es.Put(ctx, tenant, "token", "v1"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := store.getWrappedDEK(ctx, tenant); err != nil {
		t.Fatalf("precondition: tenant should have a DEK: %v", err)
	}

	n, err := es.DeleteByTenant(ctx, tenant)
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	if _, _, err := store.getWrappedDEK(ctx, tenant); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("wrapped DEK survived erasure: %v", err)
	}
	es.mu.Lock()
	_, cached := es.deks[tenant]
	es.mu.Unlock()
	if cached {
		t.Error("DEK still cached in process after erasure")
	}

	// Writing again provisions a fresh DEK and round-trips under it, so the
	// erased tenant id is reusable rather than poisoned.
	if err := es.Put(ctx, tenant, "token", "v2"); err != nil {
		t.Fatalf("put after erasure: %v", err)
	}
	if v, err := es.GetExact(ctx, tenant, "token"); err != nil || v != "v2" {
		t.Errorf("re-put = %q / %v, want %q", v, err, "v2")
	}
}

// TestSecretsDeleteByTenant_Idempotent — erasure reruns (a retried request, a
// cascade re-invoked after a partial failure) must not error.
func TestSecretsDeleteByTenant_Idempotent(t *testing.T) {
	ctx := context.Background()
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	for i := 0; i < 2; i++ {
		n, err := es.DeleteByTenant(ctx, "never-existed")
		if err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
		if n != 0 {
			t.Errorf("pass %d: n = %d, want 0", i, n)
		}
	}
	if _, err := es.DeleteByTenant(ctx, ""); err == nil {
		t.Error("empty tenant should be rejected, not treated as a wildcard")
	}
}

// TestDeleteOrgData_ErasesTenantIntegrations covers the tenant-scoped stores
// added to the cascade after it was first written: MCP servers, web APIs,
// runners (and their tokens), runner tasks, git mirrors, drop switches,
// billing, entitlements and usage counters.
//
// Each of these shipped without erasure and left the org's rows behind. The
// assertions are deliberately per-store rather than a single count, so a
// regression names the store that broke.
func TestDeleteOrgData_ErasesTenantIntegrations(t *testing.T) {
	ctx := context.Background()
	const tenant, other = "acme", "keeper"

	mcpStore := NewMemMCPServerStore()
	webStore := NewMemWebAPIStore()
	runnerStore := NewMemRunnerStore()
	taskStore := NewMemRunnerTaskStore()
	mirrorStore := newMemGitMirrorStore()
	switches := newCovMemDropSwitch()

	// Seed both tenants everywhere, so every assertion doubles as a scoping
	// check: erasure must take acme's rows and leave keeper's.
	for _, tn := range []string{tenant, other} {
		if err := mcpStore.Put(ctx, MCPServer{Tenant: tn, Name: "vendor", URL: "https://mcp.test"}, []byte("sealed")); err != nil {
			t.Fatalf("seed mcp %s: %v", tn, err)
		}
		if err := webStore.Put(ctx, WebAPI{Tenant: tn, Name: "billing", BaseURL: "https://api.test"}); err != nil {
			t.Fatalf("seed webapi %s: %v", tn, err)
		}
		// One registered machine (mint a token, then spend it) plus one token
		// still outstanding — erasure has to take both.
		if err := runnerStore.MintToken(ctx, tn, "admin@"+tn, "box", []byte("spent-"+tn), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("seed token %s: %v", tn, err)
		}
		if _, err := runnerStore.RedeemToken(ctx, []byte("spent-"+tn),
			Runner{Tenant: tn, Name: "box"}, []byte("cred-"+tn)); err != nil {
			t.Fatalf("register runner %s: %v", tn, err)
		}
		if err := runnerStore.MintToken(ctx, tn, "admin@"+tn, "box2", []byte("hash-"+tn), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("seed unspent token %s: %v", tn, err)
		}
		if err := taskStore.Enqueue(ctx, RunnerTask{
			ID: "task-" + tn, Tenant: tn, Script: "echo secret", Tags: []string{"box"}, State: TaskQueued,
		}); err != nil {
			t.Fatalf("seed task %s: %v", tn, err)
		}
		if err := mirrorStore.Upsert(ctx, GitMirror{
			Tenant: tn, Workspace: "default", RemoteURL: "git@host:" + tn + ".git", UpdatedBy: "admin@" + tn,
		}); err != nil {
			t.Fatalf("seed mirror %s: %v", tn, err)
		}
		if err := switches.Disable(ctx, DropSwitch{DropID: "http", Tenant: tn, DisabledBy: "ops@platform.test"}); err != nil {
			t.Fatalf("seed switch %s: %v", tn, err)
		}
	}
	// A global drop switch, which erasure must NOT touch: it is the platform's
	// kill-switch on a broken drop, not any org's data.
	if err := switches.Disable(ctx, DropSwitch{DropID: "smtp", Tenant: "", DisabledBy: "ops@platform.test"}); err != nil {
		t.Fatalf("seed global switch: %v", err)
	}

	h := &HTTPGateway{
		svc:          &Service{},
		MCPServers:   &MCPServers{Store: mcpStore, Catalog: mcp.NewCatalog()},
		WebAPIs:      &WebAPIs{Store: webStore, Catalog: webapi.NewCatalog()},
		Runners:      &Runners{Store: runnerStore},
		RunnerTasks:  taskStore,
		GitMirrors:   mirrorStore,
		DropSwitches: switches,
	}
	t.Cleanup(func() { _ = h.MCPServers.Catalog.Close() })

	rep, err := h.deleteOrgData(ctx, tenant)
	if err != nil {
		t.Fatalf("deleteOrgData: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}

	// Counts reported.
	for _, c := range []struct {
		name string
		got  int
	}{
		{"MCPServers", rep.MCPServers}, {"WebAPIs", rep.WebAPIs},
		{"Runners", rep.Runners}, {"RunnerTasks", rep.RunnerTasks},
		{"GitMirrors", rep.GitMirrors}, {"DropSwitches", rep.DropSwitches},
	} {
		if c.got != 1 {
			t.Errorf("rep.%s = %d, want 1", c.name, c.got)
		}
	}

	// Erased tenant is empty everywhere.
	if got, _ := mcpStore.List(ctx, tenant); len(got) != 0 {
		t.Errorf("mcp servers survived: %v", got)
	}
	if got, _ := webStore.List(ctx, tenant); len(got) != 0 {
		t.Errorf("web apis survived: %v", got)
	}
	if got, _ := runnerStore.List(ctx, tenant); len(got) != 0 {
		t.Errorf("runners survived: %v", got)
	}
	if _, err := taskStore.Get(ctx, tenant, "task-"+tenant); err == nil {
		t.Error("runner task survived — its script and env are still readable")
	}
	if _, err := mirrorStore.Get(ctx, tenant, "default"); err == nil {
		t.Error("git mirror survived")
	}
	if switches.Disabled("http", tenant) != switches.Disabled("http", "nobody") {
		t.Error("per-tenant drop switch survived")
	}

	// The unspent registration token went with the fleet: it is a live
	// credential for an org that no longer exists.
	if _, err := runnerStore.RedeemToken(ctx, []byte("hash-"+tenant),
		Runner{Tenant: tenant, Name: "box2"}, []byte("cred")); err == nil {
		t.Error("registration token survived erasure and is still redeemable")
	}
	// The registered machine's credential no longer resolves, which is what
	// actually stops an agent still running somewhere from claiming work.
	if _, err := runnerStore.RunnerByCredential(ctx, []byte("cred-"+tenant), time.Now()); err == nil {
		t.Error("erased runner's credential still authenticates")
	}
	if _, err := runnerStore.RunnerByCredential(ctx, []byte("cred-"+other), time.Now()); err != nil {
		t.Errorf("other tenant's runner credential stopped working: %v", err)
	}

	// The neighbouring tenant is untouched, in every store.
	if got, _ := mcpStore.List(ctx, other); len(got) != 1 {
		t.Errorf("other tenant's mcp servers = %d, want 1", len(got))
	}
	if got, _ := webStore.List(ctx, other); len(got) != 1 {
		t.Errorf("other tenant's web apis = %d, want 1", len(got))
	}
	if _, err := taskStore.Get(ctx, other, "task-"+other); err != nil {
		t.Errorf("other tenant's task was collateral damage: %v", err)
	}
	if _, err := mirrorStore.Get(ctx, other, "default"); err != nil {
		t.Errorf("other tenant's mirror was collateral damage: %v", err)
	}
	if !switches.Disabled("http", other) {
		t.Error("other tenant's drop switch was collateral damage")
	}
	// And the global switch is still in force.
	if !switches.Disabled("smtp", "anyone") {
		t.Error("erasing one org cleared a GLOBAL drop switch — the platform's " +
			"kill-switch on a broken drop is not any org's data")
	}
}

// TestDeleteOrgData_WarnsOnLiveSubscription — erasure drops the local Stripe
// mapping, but the subscription keeps billing in Stripe and nothing here can
// map it back afterwards. The operator has to be told while the pointer is
// still readable.
func TestDeleteOrgData_WarnsOnLiveSubscription(t *testing.T) {
	ctx := context.Background()
	plans := &fakePlanStore{plans: map[string]TenantPlan{
		"acme": {
			Tenant: "acme", Plan: PlanPro,
			StripeCustomerID: "cus_123", StripeSubscriptionID: "sub_456",
			SubscriptionStatus: "active",
		},
		"lapsed": {
			Tenant: "lapsed", Plan: PlanFree,
			StripeSubscriptionID: "sub_old", SubscriptionStatus: "canceled",
		},
	}}
	h := &HTTPGateway{svc: &Service{Plans: plans}}

	rep, err := h.deleteOrgData(ctx, "acme")
	if err != nil {
		t.Fatalf("deleteOrgData: %v", err)
	}
	var warned bool
	for _, w := range rep.Warnings {
		if strings.Contains(w, "sub_456") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning naming the live subscription: %v", rep.Warnings)
	}
	if rep.Plans != 1 {
		t.Errorf("rep.Plans = %d, want 1 (the local row still goes)", rep.Plans)
	}
	if _, ok := plans.plans["acme"]; ok {
		t.Error("billing row survived erasure")
	}

	// A lapsed subscription is not worth warning about: there is nothing left
	// to cancel, and a warning nobody needs trains people to ignore them.
	rep, err = h.deleteOrgData(ctx, "lapsed")
	if err != nil {
		t.Fatalf("deleteOrgData(lapsed): %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warned about a canceled subscription: %v", rep.Warnings)
	}
}

// TestEraseUserIdentity_RevokesRolesAndScrubsGranters covers the three identity
// tables. Each keys on the email itself, so erasing an account used to leave the
// address behind as a live role holder — and as the granter on every role that
// person had handed to someone else.
func TestEraseUserIdentity_RevokesRolesAndScrubsGranters(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"
	const colleague = "bob@example.com"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{Email: email, Subject: email, Tenant: "usr_alice"})

	admins := newMemPlatformAdmins()
	_ = admins.Grant(ctx, email, "root@platform.test")
	// Alice granted Bob his role: her address is in HIS row, which survives her.
	_ = admins.Grant(ctx, colleague, email)

	agents := NewMemSupportAgentStore()
	_ = agents.Grant(ctx, email, "root@platform.test")
	_ = agents.Grant(ctx, colleague, email)

	blocklist := newCovBlocklist()
	// Alice banned a spammer. Her email is the ADMIN on that row.
	_ = blocklist.Block(ctx, auth.Blocked{
		Value: "spammer@bad.test", Kind: "email", Reason: "abuse", CreatedBy: email,
	})
	// And someone banned Alice. That entry must SURVIVE her erasure.
	_ = blocklist.Block(ctx, auth.Blocked{
		Value: email, Kind: "email", Reason: "abuse", CreatedBy: "root@platform.test",
	})

	h := &HTTPGateway{
		svc:                 &Service{},
		Users:               users,
		PlatformAdminGrants: admins,
		SupportAgents:       agents,
		Blocklist:           blocklist,
	}

	rep, err := h.eraseUserIdentity(ctx, email)
	if err != nil {
		t.Fatalf("eraseUserIdentity: %v", err)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", rep.Warnings)
	}

	// Her own grants are gone — she is no longer a platform admin or an agent.
	if admins.Granted(email) {
		t.Error("erased account still holds the platform-admin role")
	}
	if agents.Granted(email) {
		t.Error("erased account still holds the support-agent role")
	}
	if rep.RoleGrants != 2 {
		t.Errorf("rep.RoleGrants = %d, want 2", rep.RoleGrants)
	}

	// Bob keeps his roles, but her address is no longer recorded as the granter.
	if !admins.Granted(colleague) {
		t.Error("colleague lost their role — erasure took someone else's grant")
	}
	adminRows, _ := admins.List(ctx)
	for _, g := range adminRows {
		if strings.Contains(strings.ToLower(g.GrantedBy), "alice") {
			t.Errorf("erased email survives as granted_by on %s: %q", g.Email, g.GrantedBy)
		}
	}
	agentRows, _ := agents.List(ctx)
	for _, g := range agentRows {
		if strings.Contains(strings.ToLower(g.GrantedBy), "alice") {
			t.Errorf("erased email survives as granted_by on %s: %q", g.Email, g.GrantedBy)
		}
	}

	// The blocklist: her name is scrubbed as the blocking admin...
	blocked, _ := blocklist.List(ctx)
	var banOnAliceFound bool
	for _, b := range blocked {
		if b.Value == "spammer@bad.test" && strings.Contains(strings.ToLower(b.CreatedBy), "alice") {
			t.Errorf("erased email survives as blocklist created_by: %q", b.CreatedBy)
		}
		if b.Value == email {
			banOnAliceFound = true
		}
	}
	// ...but the ban ON her stands. A block liftable by asking to be forgotten
	// is not a block; it is kept under legitimate interest (Art. 17(1)(c)).
	if !banOnAliceFound {
		t.Error("erasure lifted the ban on the erased account — a deletion request " +
			"must not be a way to clear your own blocklist entry")
	}
	if rep.GrantedByRefs != 3 {
		t.Errorf("rep.GrantedByRefs = %d, want 3 (2 granters + 1 blocklist admin)", rep.GrantedByRefs)
	}
}

// TestEraseUserIdentity_WarnsOnEnvPlatformAdmin — platform-admin status also
// comes from $DAZYFLOW_PLATFORM_ADMINS, which is deployment config this process
// cannot rewrite. Erasing the account without saying so would leave an address
// that silently re-elevates if the person ever signs up again.
func TestEraseUserIdentity_WarnsOnEnvPlatformAdmin(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{Email: email, Subject: email, Tenant: "usr_alice"})

	h := &HTTPGateway{
		svc:            &Service{},
		Users:          users,
		PlatformAdmins: []string{"root@platform.test", "Alice@Example.com"},
	}
	rep, err := h.eraseUserIdentity(ctx, email)
	if err != nil {
		t.Fatalf("eraseUserIdentity: %v", err)
	}
	var warned bool
	for _, w := range rep.Warnings {
		if strings.Contains(w, "DAZYFLOW_PLATFORM_ADMINS") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning about the env allowlist (matching is case-insensitive): %v", rep.Warnings)
	}
}

// TestEraseUserIdentity_ScrubsAuthorshipInSharedOrg is the shared-org path: a
// member erases their account and the ORG CARRIES ON. Every row they authored
// belongs to that org and survives — and used to survive still carrying their
// email in created_by / updated_by / invited_by / disabled_by.
//
// Org deletion never covered this, because the org is not being deleted. This
// is the most common erasure there is.
func TestEraseUserIdentity_ScrubsAuthorshipInSharedOrg(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"
	const tenant = "acme" // a SHARED org that outlives her
	const colleague = "bob@example.com"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{Email: email, Subject: email, Tenant: tenant})

	mcpStore := NewMemMCPServerStore()
	_ = mcpStore.Put(ctx, MCPServer{Tenant: tenant, Name: "vendor", URL: "https://mcp.test", CreatedBy: email}, nil)
	webStore := NewMemWebAPIStore()
	_ = webStore.Put(ctx, WebAPI{Tenant: tenant, Name: "billing", BaseURL: "https://api.test", CreatedBy: email})
	runnerStore := NewMemRunnerStore()
	_ = runnerStore.MintToken(ctx, tenant, email, "box", []byte("tok"), time.Now().Add(time.Hour))
	_, _ = runnerStore.RedeemToken(ctx, []byte("tok"), Runner{Tenant: tenant, Name: "box"}, []byte("cred"))
	mirrors := newMemGitMirrorStore()
	_ = mirrors.Upsert(ctx, GitMirror{Tenant: tenant, Workspace: "default", RemoteURL: "git@h:a.git", UpdatedBy: email})
	switches := newCovMemDropSwitch()
	_ = switches.Disable(ctx, DropSwitch{DropID: "http", Tenant: tenant, DisabledBy: email})
	bundles := NewMemBundleStore()
	_ = bundles.Create(ctx, core.SupportBundleRecord{
		ID: "b1", Tenant: tenant, FlowID: "f", CreatedBy: email, CreatedAt: time.Now(),
	})
	// Alice invited Bob: her address is on HIS membership and on a pending invite.
	members := newFakeMembershipStore()
	_ = members.PutMembership(ctx, auth.Membership{UserEmail: colleague, Tenant: tenant, InvitedBy: email})
	invites, _ := auth.OpenJSONInvitationStore("")
	_ = invites.PutInvitation(ctx, auth.Invitation{
		Token: "inv1", Email: "carol@example.com", Tenant: tenant,
		InvitedBy: email, ExpiresAt: time.Now().Add(time.Hour),
	})

	h := &HTTPGateway{
		svc:          &Service{},
		Users:        users,
		MCPServers:   &MCPServers{Store: mcpStore, Catalog: mcp.NewCatalog()},
		WebAPIs:      &WebAPIs{Store: webStore, Catalog: webapi.NewCatalog()},
		Runners:      &Runners{Store: runnerStore},
		GitMirrors:   mirrors,
		DropSwitches: switches,
		Bundles:      bundles,
		Memberships:  members,
		Invitations:  invites,
	}
	t.Cleanup(func() { _ = h.MCPServers.Catalog.Close() })

	rep, err := h.eraseUserIdentity(ctx, email)
	if err != nil {
		t.Fatalf("eraseUserIdentity: %v", err)
	}

	// Nothing was deleted: these rows belong to the org, which is still here.
	if got, _ := mcpStore.List(ctx, tenant); len(got) != 1 {
		t.Fatalf("erasure DELETED an org's mcp server; want it kept and scrubbed: %v", got)
	}

	// The address is gone from every one of them.
	if got, _ := mcpStore.List(ctx, tenant); got[0].CreatedBy != core.ErasedIdentity {
		t.Errorf("tenant_mcp_servers.created_by = %q", got[0].CreatedBy)
	}
	if got, _ := webStore.List(ctx, tenant); len(got) != 1 || got[0].CreatedBy != core.ErasedIdentity {
		t.Errorf("tenant_web_apis.created_by = %+v", got)
	}
	if got, _ := runnerStore.List(ctx, tenant); len(got) != 1 || got[0].CreatedBy != core.ErasedIdentity {
		t.Errorf("tenant_runners.created_by = %+v", got)
	}
	if got, err := mirrors.Get(ctx, tenant, "default"); err != nil || got.UpdatedBy != core.ErasedIdentity {
		t.Errorf("git_mirrors.updated_by = %q / %v", got.UpdatedBy, err)
	}
	if got, _ := switches.List(ctx); len(got) != 1 || got[0].DisabledBy != core.ErasedIdentity {
		t.Errorf("drop_switches.disabled_by = %+v", got)
	}
	if got, err := bundles.Get(ctx, "b1"); err != nil || got.CreatedBy != core.ErasedIdentity {
		t.Errorf("support_bundles.created_by = %q / %v", got.CreatedBy, err)
	}
	got, err := members.GetMembership(ctx, colleague, tenant)
	if err != nil {
		t.Fatalf("colleague's membership was deleted, not scrubbed: %v", err)
	}
	if got.InvitedBy != core.ErasedIdentity {
		t.Errorf("memberships.invited_by = %q", got.InvitedBy)
	}
	inv, err := invites.GetByToken(ctx, "inv1")
	if err != nil {
		t.Fatalf("someone else's pending invite was deleted: %v", err)
	}
	if inv.InvitedBy != core.ErasedIdentity {
		t.Errorf("invitations.invited_by = %q", inv.InvitedBy)
	}
	if rep.AuthoredRefs == 0 {
		t.Error("rep.AuthoredRefs = 0 — the scrub is not being counted")
	}
}
