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

	h := &HTTPGateway{
		svc:         &Service{AdminKeys: keys},
		Users:       users,
		Sessions:    sessions,
		Memberships: members,
		Invitations: invites,
		Audit:       audit,
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
