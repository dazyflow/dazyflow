// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeMembershipStore is a map-backed auth.MembershipStore for handler
// tests (the only real implementation is Postgres-backed).
type fakeMembershipStore struct {
	// mu makes the fake safe to hammer from several goroutines, which the
	// seat-race test does on purpose, and backs the atomic seat write below.
	mu   sync.Mutex
	rows map[string]auth.Membership // email|tenant
}

func newFakeMembershipStore() *fakeMembershipStore {
	return &fakeMembershipStore{rows: map[string]auth.Membership{}}
}

func memKey(email, tenant string) string {
	return strings.ToLower(strings.TrimSpace(email)) + "|" + tenant
}

func (f *fakeMembershipStore) PutMembership(_ context.Context, m auth.Membership) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(m)
	return nil
}

func (f *fakeMembershipStore) put(m auth.Membership) {
	m.UserEmail = strings.ToLower(strings.TrimSpace(m.UserEmail))
	f.rows[memKey(m.UserEmail, m.Tenant)] = m
}

// PutMembershipWithinLimit implements auth.SeatLimitedMembershipStore, so the
// handler tests exercise the same atomic path production takes (where it is
// backed by a transaction and a per-tenant advisory lock) rather than the
// racy check-then-write fallback.
func (f *fakeMembershipStore) PutMembershipWithinLimit(_ context.Context, m auth.Membership, maxRows int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, already := f.rows[memKey(m.UserEmail, m.Tenant)]; !already {
		rows := 0
		for _, r := range f.rows {
			if r.Tenant == m.Tenant {
				rows++
			}
		}
		if rows >= maxRows {
			return false, nil
		}
	}
	f.put(m)
	return true, nil
}

func (f *fakeMembershipStore) DeleteMembership(_ context.Context, email, tenant string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, memKey(email, tenant))
	return nil
}

func (f *fakeMembershipStore) GetMembership(_ context.Context, email, tenant string) (auth.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.rows[memKey(email, tenant)]; ok {
		return m, nil
	}
	return auth.Membership{}, auth.ErrUnknownMembership
}

func (f *fakeMembershipStore) ListByEmail(_ context.Context, email string) ([]auth.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Membership
	for _, m := range f.rows {
		if m.UserEmail == strings.ToLower(email) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeMembershipStore) ListByTenant(_ context.Context, tenant string) ([]auth.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Membership
	for _, m := range f.rows {
		if m.Tenant == tenant {
			out = append(out, m)
		}
	}
	return out, nil
}

func seedMember(t *testing.T, store *fakeMembershipStore, email, tenant string, roles ...core.Role) {
	t.Helper()
	if err := store.PutMembership(t.Context(), auth.Membership{
		UserEmail: email, Tenant: tenant, Workspace: "ws", Roles: roles,
	}); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

// teamAdminDo runs the request as a realistic org admin — the catalog
// admin role (editor perms + organization:admin) — unlike adminDo's
// org-admin-only token, which can't grant graph permissions it doesn't
// hold (the role-capping rule).
func teamAdminDo(t *testing.T, h *gatewayHarness, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-team-admin", "t", "ws", "boss",
		[]core.Role{core.TeamRoleAdmin()}, nil)
	if err != nil {
		t.Fatalf("issue team-admin key: %v", err)
	}
	var rdr *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewBuffer(b)
	} else {
		rdr = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

func TestUpdateMemberRoles(t *testing.T) {
	h := newGatewayHarness(t)
	store := newFakeMembershipStore()
	h.gw.Memberships = store
	seedMember(t, store, "member@example.com", "t", core.TeamRoleEditor())

	// Editor → viewer (demotion).
	rw := teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []core.Role{core.TeamRoleViewer()},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	m, err := store.GetMembership(t.Context(), "member@example.com", "t")
	if err != nil || len(m.Roles) != 1 || m.Roles[0].Name != "viewer" ||
		m.Roles[0].Has(core.PermGraphEdit) {
		t.Errorf("after demotion = %+v / %v, want single viewer role", m, err)
	}
	// Workspace survives the role change.
	if m.Workspace != "ws" {
		t.Errorf("workspace = %q, want preserved 'ws'", m.Workspace)
	}
	var resp struct {
		Roles []core.Role `json:"roles"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Roles) != 1 || resp.Roles[0].Name != "viewer" {
		t.Errorf("response roles = %+v", resp.Roles)
	}

	// Name-only roles resolve to the SERVER's catalog definition — the
	// client never has to (and shouldn't) ship permission lists.
	rw = teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []map[string]any{{"name": "editor"}},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("name-only status = %d, body %s", rw.Code, rw.Body.String())
	}
	m, _ = store.GetMembership(t.Context(), "member@example.com", "t")
	if len(m.Roles) != 1 || !m.Roles[0].Has(core.PermGraphEdit) || !m.Roles[0].Has(core.PermSecretWrite) {
		t.Errorf("name-only editor did not resolve to catalog perms: %+v", m.Roles)
	}
	// A name-only role outside the catalog is a 400, not an empty grant.
	rw = teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []map[string]any{{"name": "superuser"}},
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("unknown name-only role status = %d, want 400", rw.Code)
	}
}

func TestUpdateMemberRoles_Guards(t *testing.T) {
	h := newGatewayHarness(t)
	store := newFakeMembershipStore()
	h.gw.Memberships = store
	seedMember(t, store, "member@example.com", "t", core.TeamRoleEditor())

	// Unknown member → 404.
	rw := teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/ghost@example.com", map[string]any{
		"roles": []core.Role{core.TeamRoleViewer()},
	})
	if rw.Code != http.StatusNotFound {
		t.Errorf("ghost: status = %d, want 404", rw.Code)
	}

	// Empty roles → 400 (removal is DELETE's job).
	rw = teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []core.Role{},
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("empty roles: status = %d, want 400", rw.Code)
	}

	// Over-scoped grant → 403 and roles unchanged. (adminDo's token holds
	// only organization:admin, so secret:write exceeds it.)
	rw = h.adminDo(t, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []map[string]any{{"name": "x", "permissions": []string{"secret:write"}}},
	})
	if rw.Code != http.StatusForbidden {
		t.Errorf("over-scoped: status = %d, want 403 (body %s)", rw.Code, rw.Body.String())
	}
	// platform:admin always refused for non-platform admins.
	rw = h.adminDo(t, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []map[string]any{{"name": "x", "permissions": []string{"platform:admin"}}},
	})
	if rw.Code != http.StatusForbidden {
		t.Errorf("platform:admin: status = %d, want 403", rw.Code)
	}
	m, _ := store.GetMembership(t.Context(), "member@example.com", "t")
	if len(m.Roles) != 1 || m.Roles[0].Name != "editor" {
		t.Errorf("roles changed by refused requests: %+v", m.Roles)
	}

	// Non-admin caller (the plain editor token) → 403.
	rw = h.do(t, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []core.Role{core.TeamRoleViewer()},
	})
	if rw.Code != http.StatusForbidden {
		t.Errorf("non-admin: status = %d, want 403", rw.Code)
	}
}

// Changing or removing a membership sweeps the member's live sessions,
// so a demotion takes effect immediately instead of at session expiry.
func TestUpdateMemberRoles_SweepsSessions(t *testing.T) {
	h := newGatewayHarness(t)
	store := newFakeMembershipStore()
	h.gw.Memberships = store
	seedMember(t, store, "member@example.com", "t", core.TeamRoleEditor())

	users, err := auth.OpenJSONUserStore("") // empty path = in-memory
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	member := auth.User{Subject: "member-subj", Email: "member@example.com", Tenant: "home-org"}
	if err := users.PutUser(t.Context(), member); err != nil {
		t.Fatalf("put user: %v", err)
	}
	sessions := auth.NewMemSessionStore()
	h.gw.Users = users
	h.gw.Sessions = sessions
	if _, _, err := auth.IssueSession(t.Context(), sessions, member, time.Hour); err != nil {
		t.Fatalf("issue session: %v", err)
	}
	// A bystander's session must survive the sweep.
	other := auth.User{Subject: "other-subj", Email: "other@example.com", Tenant: "elsewhere"}
	_, otherToken, err := auth.IssueSession(t.Context(), sessions, other, time.Hour)
	if err != nil {
		t.Fatalf("issue other session: %v", err)
	}

	rw := teamAdminDo(t, h, "PATCH", "/api/v1/admin/members/member@example.com", map[string]any{
		"roles": []core.Role{core.TeamRoleViewer()},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}

	if n, _ := sessions.RevokeSubjectSessions(t.Context(), "member-subj"); n != 0 {
		t.Errorf("member still had %d live session(s) after the role change", n)
	}
	if _, err := sessions.GetSession(t.Context(), auth.SessionLookupKey(otherToken)); err != nil {
		t.Errorf("bystander session was swept too: %v", err)
	}

	// Removal sweeps as well: re-seed, re-issue, DELETE.
	seedMember(t, store, "member@example.com", "t", core.TeamRoleViewer())
	if _, _, err := auth.IssueSession(t.Context(), sessions, member, time.Hour); err != nil {
		t.Fatalf("re-issue session: %v", err)
	}
	rw = teamAdminDo(t, h, "DELETE", "/api/v1/admin/members/member@example.com", nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, body %s", rw.Code, rw.Body.String())
	}
	if n, _ := sessions.RevokeSubjectSessions(t.Context(), "member-subj"); n != 0 {
		t.Errorf("member still had %d live session(s) after removal", n)
	}
}
