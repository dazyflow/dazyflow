// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests against a real Postgres. Skipped unless
// DAZYFLOW_TEST_DB is set, e.g.
//
//	DAZYFLOW_TEST_DB=postgres://localhost/dazyflow_test go test ./auth/
//
// Mirrors the jobstore Postgres gate so CI exercises one DB for both.

func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := EnsurePgAuthSchema(ctx, pool); err != nil {
		t.Fatalf("EnsurePgAuthSchema: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE api_keys, sessions, users")
	return pool, ctx
}

func TestPgKeyStore_RoundTrip(t *testing.T) {
	pool, ctx := testPool(t)
	store, err := NewPgKeyStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgKeyStore: %v", err)
	}

	roles := []core.Role{{Name: "admin", Permissions: []core.Permission{core.PermOrganizationAdmin}}}
	k := APIKey{ID: "k1", Tenant: "acme", Workspace: "default", Subject: "alice", Roles: roles, Salt: []byte("salt"), Hash: []byte("hash")}
	if err := store.PutKey(ctx, k); err != nil {
		t.Fatalf("PutKey: %v", err)
	}

	got, err := store.GetKey(ctx, "k1")
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if got.Subject != "alice" || got.Tenant != "acme" || len(got.Roles) != 1 || got.Roles[0].Name != "admin" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Unknown key → ErrInvalidCredential (so the authenticator treats it
	// like a bad credential, not a server error).
	if _, err := store.GetKey(ctx, "nope"); err != ErrInvalidCredential {
		t.Errorf("GetKey(unknown) err = %v, want ErrInvalidCredential", err)
	}

	// ListByTenant / ListAll.
	if list, err := store.ListByTenant(ctx, "acme"); err != nil || len(list) != 1 {
		t.Errorf("ListByTenant = %v, %v", list, err)
	}
	if list, err := store.ListAll(ctx); err != nil || len(list) != 1 {
		t.Errorf("ListAll = %v, %v", list, err)
	}

	// Revoke flips revoked_at; the authenticator rejects revoked keys.
	if err := store.Revoke(ctx, "k1", time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got, _ = store.GetKey(ctx, "k1")
	if got.RevokedAt == nil {
		t.Error("RevokedAt not set after Revoke")
	}
	if err := store.Revoke(ctx, "nope", time.Now()); err != ErrInvalidCredential {
		t.Errorf("Revoke(unknown) err = %v, want ErrInvalidCredential", err)
	}
}

func TestPgSessionStore_RoundTrip(t *testing.T) {
	pool, ctx := testPool(t)
	store, err := NewPgSessionStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgSessionStore: %v", err)
	}

	sess := Session{
		ID:        "dzs_abc",
		Subject:   "bob",
		Tenant:    "acme",
		Workspace: "default",
		Roles:     []core.Role{{Name: "editor"}},
		CreatedAt: time.Now().Truncate(time.Second),
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second),
	}
	if err := store.PutSession(ctx, sess); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	got, err := store.GetSession(ctx, "dzs_abc")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Subject != "bob" || len(got.Roles) != 1 || got.Roles[0].Name != "editor" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if err := store.DeleteSession(ctx, "dzs_abc"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.GetSession(ctx, "dzs_abc"); err != ErrInvalidCredential {
		t.Errorf("GetSession(deleted) err = %v, want ErrInvalidCredential", err)
	}
}

func TestPgUserStore_RoundTrip(t *testing.T) {
	pool, ctx := testPool(t)
	store, err := NewPgUserStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgUserStore: %v", err)
	}

	noFailureEmail := false
	u := User{
		Email:        "Alice@Example.com", // intentionally mixed-case
		PasswordHash: []byte("bcrypt"),
		Subject:      "alice",
		Tenant:       "usr_abc",
		Workspace:    "default",
		Roles:        []core.Role{{Name: "tenant_owner"}},
		Notify:       NotifyPrefs{EmailOnFlowFailure: &noFailureEmail},
		UI:           UIPrefs{Theme: "light", Language: "sv"},
	}
	if err := store.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	// Lookup normalizes case.
	got, err := store.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got.Subject != "alice" || got.Email != "alice@example.com" || len(got.Roles) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	// Notification preference survives the JSONB round-trip as an
	// explicit (non-nil) choice.
	if got.Notify.EmailOnFlowFailure == nil {
		t.Errorf("Notify.EmailOnFlowFailure lost in round-trip (got nil)")
	} else if *got.Notify.EmailOnFlowFailure {
		t.Errorf("Notify.EmailOnFlowFailure = true, want false")
	}
	if got.Notify.EmailOnFlowFailureEnabled() {
		t.Errorf("EmailOnFlowFailureEnabled() = true, want false (explicit opt-out)")
	}
	// Interface prefs survive the JSONB round-trip too.
	if got.UI.Theme != "light" || got.UI.Language != "sv" {
		t.Errorf("UI prefs round-trip: got %+v, want {light sv}", got.UI)
	}
	if _, err := store.GetByEmail(ctx, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("GetByEmail(unknown) err = %v, want ErrUnknownUser", err)
	}
	if list, err := store.ListUsers(ctx); err != nil || len(list) != 1 {
		t.Errorf("ListUsers = %v, %v", list, err)
	}
}

// covPool returns a pool with all auth + orgs + blocklist schemas ensured
// and every table truncated. Gated on DAZYFLOW_TEST_DB like the others.
func covPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := EnsurePgAuthSchema(ctx, pool); err != nil {
		t.Fatalf("EnsurePgAuthSchema: %v", err)
	}
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		t.Fatalf("EnsurePgOrgsSchema: %v", err)
	}
	if err := EnsurePgBlocklistSchema(ctx, pool); err != nil {
		t.Fatalf("EnsurePgBlocklistSchema: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE api_keys, sessions, users, memberships, invitations, org_auth, org_profiles, blocked_identities")
	return pool, ctx
}

func TestPgKeyStore_GDPRPaths(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgKeyStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgKeyStore: %v", err)
	}
	mk := func(id, tenant, subject string) {
		t.Helper()
		if err := store.PutKey(ctx, APIKey{ID: id, Tenant: tenant, Subject: subject, Salt: []byte("s"), Hash: []byte("h")}); err != nil {
			t.Fatalf("PutKey: %v", err)
		}
	}
	mk("a1", "acme", "alice")
	mk("a2", "acme", "bob")
	mk("g1", "globex", "alice")

	if list, err := store.ListBySubject(ctx, "alice"); err != nil || len(list) != 2 {
		t.Errorf("ListBySubject = %v, %v", list, err)
	}
	if n, err := store.DeleteBySubject(ctx, "alice"); err != nil || n != 2 {
		t.Errorf("DeleteBySubject = %d, %v", n, err)
	}
	if n, err := store.DeleteByTenant(ctx, "acme"); err != nil || n != 1 {
		t.Errorf("DeleteByTenant = %d, %v", n, err)
	}
	if all, err := store.ListAll(ctx); err != nil || len(all) != 0 {
		t.Errorf("ListAll = %v, %v", all, err)
	}
}

func TestPgSessionStore_RevokeSubject(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgSessionStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgSessionStore: %v", err)
	}
	now := time.Now()
	for _, id := range []string{"s1", "s2"} {
		if err := store.PutSession(ctx, Session{ID: id, Subject: "bob", Tenant: "acme", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("PutSession: %v", err)
		}
	}
	if err := store.PutSession(ctx, Session{ID: "s3", Subject: "alice", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("PutSession alice: %v", err)
	}
	n, err := store.RevokeSubjectSessions(ctx, "bob")
	if err != nil || n != 2 {
		t.Errorf("RevokeSubjectSessions = %d, %v", n, err)
	}
	if _, err := store.GetSession(ctx, "s1"); err != ErrInvalidCredential {
		t.Errorf("revoked session still present: %v", err)
	}
	if _, err := store.GetSession(ctx, "s3"); err != nil {
		t.Errorf("alice session wrongly revoked: %v", err)
	}
}

func TestPgUserStore_DeleteUser(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgUserStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgUserStore: %v", err)
	}
	if err := store.PutUser(ctx, User{Email: "Del@Example.com", Subject: "del", PasswordHash: []byte("h")}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	// Empty email rejected.
	if err := store.PutUser(ctx, User{Email: "   "}); err == nil {
		t.Error("empty email should be rejected")
	}
	// Delete (case-insensitive) then confirm gone; idempotent re-delete.
	if err := store.DeleteUser(ctx, "del@example.com"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if err := store.DeleteUser(ctx, "del@example.com"); err != nil {
		t.Fatalf("DeleteUser idempotent: %v", err)
	}
	if _, err := store.GetByEmail(ctx, "del@example.com"); err != ErrUnknownUser {
		t.Errorf("after delete err = %v", err)
	}
}

func TestPgBlocklistStore_Cov(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgBlocklistStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgBlocklistStore: %v", err)
	}

	// Empty email → not blocked, no query.
	if blocked, _, err := store.IsBlocked(ctx, "  "); err != nil || blocked {
		t.Errorf("empty IsBlocked = %v, %v", blocked, err)
	}
	// Nothing blocked yet.
	if blocked, _, err := store.IsBlocked(ctx, "alice@acme.test"); err != nil || blocked {
		t.Errorf("IsBlocked before any block = %v, %v", blocked, err)
	}

	// Block an exact email (default kind).
	if err := store.Block(ctx, Blocked{Value: "Banned@Acme.test", Reason: "spam", CreatedBy: "admin"}); err != nil {
		t.Fatalf("Block email: %v", err)
	}
	// Empty value rejected.
	if err := store.Block(ctx, Blocked{Value: "  "}); err == nil {
		t.Error("empty value should be rejected")
	}
	blocked, b, err := store.IsBlocked(ctx, "banned@acme.test")
	if err != nil || !blocked || b.Kind != BlockEmail {
		t.Errorf("exact block IsBlocked = %v, %+v, %v", blocked, b, err)
	}

	// Block a whole domain.
	if err := store.Block(ctx, Blocked{Value: "evil.test", Kind: BlockDomain, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Block domain: %v", err)
	}
	blocked, b, err = store.IsBlocked(ctx, "anyone@evil.test")
	if err != nil || !blocked || b.Kind != BlockDomain {
		t.Errorf("domain block IsBlocked = %v, %+v, %v", blocked, b, err)
	}

	// List returns both, newest first.
	list, err := store.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v, %v", list, err)
	}

	// Unblock the email; re-check.
	if err := store.Unblock(ctx, "banned@acme.test"); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	if blocked, _, _ := store.IsBlocked(ctx, "banned@acme.test"); blocked {
		t.Error("email still blocked after Unblock")
	}
}

func TestPgBlocklistStore_NilPoolSchema(t *testing.T) {
	if err := EnsurePgBlocklistSchema(context.Background(), nil); err == nil {
		t.Error("nil pool should error")
	}
}

func TestMigrateLegacyOrgAdminPerm_Cov(t *testing.T) {
	pool, ctx := covPool(t)
	ustore, err := NewPgUserStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgUserStore: %v", err)
	}
	mstore, err := NewPgMembershipStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgMembershipStore: %v", err)
	}

	// Seed rows carrying the legacy permission string directly via JSONB.
	legacyRoles := []core.Role{{Name: "owner", Permissions: []core.Permission{core.Permission("tenant:admin")}}}
	if err := ustore.PutUser(ctx, User{Email: "owner@acme.test", Subject: "o", Tenant: "acme", Roles: legacyRoles, PasswordHash: []byte("h")}); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	if err := mstore.PutMembership(ctx, Membership{UserEmail: "owner@acme.test", Tenant: "acme", Roles: legacyRoles}); err != nil {
		t.Fatalf("PutMembership: %v", err)
	}

	n, err := MigrateLegacyOrgAdminPerm(ctx, pool)
	if err != nil {
		t.Fatalf("MigrateLegacyOrgAdminPerm: %v", err)
	}
	if n != 2 {
		t.Errorf("migrated rows = %d, want 2", n)
	}

	// Re-running is a no-op now that the legacy string is gone.
	if n2, err := MigrateLegacyOrgAdminPerm(ctx, pool); err != nil || n2 != 0 {
		t.Errorf("re-run = %d, %v, want 0", n2, err)
	}

	// Confirm the user's role now carries the renamed permission.
	got, err := ustore.GetByEmail(ctx, "owner@acme.test")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0].Permissions[0] != core.PermOrganizationAdmin {
		t.Errorf("permission not migrated: %+v", got.Roles)
	}
}

func TestMigrateLegacyOrgAdminPerm_NilPool(t *testing.T) {
	if _, err := MigrateLegacyOrgAdminPerm(context.Background(), nil); err == nil {
		t.Error("nil pool should error")
	}
}

func TestEnsureSchemas_NilPool(t *testing.T) {
	if err := EnsurePgAuthSchema(context.Background(), nil); err == nil {
		t.Error("EnsurePgAuthSchema(nil) should error")
	}
	if err := EnsurePgOrgsSchema(context.Background(), nil); err == nil {
		t.Error("EnsurePgOrgsSchema(nil) should error")
	}
}

func TestPgMembershipStore_GDPRPaths(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgMembershipStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgMembershipStore: %v", err)
	}
	put := func(email, tenant string) {
		t.Helper()
		if err := store.PutMembership(ctx, Membership{UserEmail: email, Tenant: tenant, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatalf("PutMembership: %v", err)
		}
	}
	put("alice@acme.test", "acme")
	put("alice@acme.test", "globex")
	put("bob@acme.test", "acme")

	if n, err := store.DeleteByEmail(ctx, "alice@acme.test"); err != nil || n != 2 {
		t.Errorf("DeleteByEmail = %d, %v", n, err)
	}
	if n, err := store.DeleteByTenant(ctx, "acme"); err != nil || n != 1 {
		t.Errorf("DeleteByTenant = %d, %v", n, err)
	}
}

func TestPgInvitationStore_GDPRPaths(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgInvitationStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgInvitationStore: %v", err)
	}
	now := time.Now().UTC()
	mk := func(token, email, tenant string) {
		t.Helper()
		if err := store.PutInvitation(ctx, Invitation{Token: token, Email: email, Tenant: tenant, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("PutInvitation: %v", err)
		}
	}
	mk("t1", "alice@acme.test", "acme")
	mk("t2", "alice@acme.test", "globex")
	mk("t3", "bob@acme.test", "acme")

	// Validation paths.
	if err := store.PutInvitation(ctx, Invitation{Tenant: "x"}); err == nil {
		t.Error("missing token should be rejected")
	}
	if err := store.PutInvitation(ctx, Invitation{Token: "x"}); err == nil {
		t.Error("missing tenant should be rejected")
	}

	if list, err := store.ListByEmail(ctx, "alice@acme.test"); err != nil || len(list) != 2 {
		t.Errorf("ListByEmail = %v, %v", list, err)
	}
	if n, err := store.DeleteByEmail(ctx, "alice@acme.test"); err != nil || n != 2 {
		t.Errorf("DeleteByEmail = %d, %v", n, err)
	}
	if n, err := store.DeleteByTenant(ctx, "acme"); err != nil || n != 1 {
		t.Errorf("DeleteByTenant = %d, %v", n, err)
	}

	// MarkRevoked round-trip on a fresh token + unknown path.
	mk("t4", "carol@acme.test", "delta")
	if err := store.MarkRevoked(ctx, "t4", now); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}
	got, _ := store.GetByToken(ctx, "t4")
	if got.RevokedAt == nil {
		t.Error("RevokedAt not set")
	}
}

func TestPgOrgProfileStore_SubdomainAndDelete(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgOrgProfileStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgOrgProfileStore: %v", err)
	}
	if err := store.PutOrgProfile(ctx, OrgProfile{Tenant: "acme", DisplayName: "Acme", Subdomain: "acme"}); err != nil {
		t.Fatalf("PutOrgProfile: %v", err)
	}
	// Lookup by subdomain (case-insensitive).
	got, err := store.GetOrgProfileBySubdomain(ctx, "ACME")
	if err != nil || got.Tenant != "acme" {
		t.Errorf("GetOrgProfileBySubdomain = %+v, %v", got, err)
	}
	// Empty subdomain → ErrUnknownOrgProfile.
	if _, err := store.GetOrgProfileBySubdomain(ctx, "  "); !errors.Is(err, ErrUnknownOrgProfile) {
		t.Errorf("empty subdomain err = %v", err)
	}
	// Unknown subdomain.
	if _, err := store.GetOrgProfileBySubdomain(ctx, "nobody"); !errors.Is(err, ErrUnknownOrgProfile) {
		t.Errorf("unknown subdomain err = %v", err)
	}
	// Tenant required on Put.
	if err := store.PutOrgProfile(ctx, OrgProfile{}); err == nil {
		t.Error("empty tenant should be rejected")
	}
	// A second org claiming the same subdomain → ErrSubdomainTaken.
	if err := store.PutOrgProfile(ctx, OrgProfile{Tenant: "globex", DisplayName: "Globex", Subdomain: "acme"}); !errors.Is(err, ErrSubdomainTaken) {
		t.Errorf("duplicate subdomain err = %v, want ErrSubdomainTaken", err)
	}
	// ListAllOrgProfiles + ListOrgProfiles(empty).
	if all, err := store.ListAllOrgProfiles(ctx); err != nil || len(all) == 0 {
		t.Errorf("ListAllOrgProfiles = %v, %v", all, err)
	}
	if m, err := store.ListOrgProfiles(ctx, nil); err != nil || len(m) != 0 {
		t.Errorf("ListOrgProfiles(nil) = %v, %v", m, err)
	}
	// Delete.
	if err := store.DeleteOrgProfile(ctx, "acme"); err != nil {
		t.Fatalf("DeleteOrgProfile: %v", err)
	}
	if _, err := store.GetOrgProfile(ctx, "acme"); !errors.Is(err, ErrUnknownOrgProfile) {
		t.Errorf("after delete err = %v", err)
	}
}

func TestPgOrgAuthStore_TenantRequired(t *testing.T) {
	pool, ctx := covPool(t)
	store, err := NewPgOrgAuthStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgOrgAuthStore: %v", err)
	}
	if err := store.PutOrgAuth(ctx, OrgAuthConfig{}); err == nil {
		t.Error("empty tenant should be rejected")
	}
}
