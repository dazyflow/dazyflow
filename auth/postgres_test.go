package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Integration tests against a real Postgres. Skipped unless
// HAZYFLOW_TEST_DB is set, e.g.
//
//	HAZYFLOW_TEST_DB=postgres://localhost/hazyflow_test go test ./auth/
//
// Mirrors the jobstore Postgres gate so CI exercises one DB for both.

func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("HAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
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

	roles := []core.Role{{Name: "admin", Permissions: []core.Permission{core.PermTenantAdmin}}}
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
		ID:        "hzs_abc",
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
	got, err := store.GetSession(ctx, "hzs_abc")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Subject != "bob" || len(got.Roles) != 1 || got.Roles[0].Name != "editor" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if err := store.DeleteSession(ctx, "hzs_abc"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.GetSession(ctx, "hzs_abc"); err != ErrInvalidCredential {
		t.Errorf("GetSession(deleted) err = %v, want ErrInvalidCredential", err)
	}
}

func TestPgUserStore_RoundTrip(t *testing.T) {
	pool, ctx := testPool(t)
	store, err := NewPgUserStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgUserStore: %v", err)
	}

	u := User{
		Email:        "Alice@Example.com", // intentionally mixed-case
		PasswordHash: []byte("bcrypt"),
		Subject:      "alice",
		Tenant:       "usr_abc",
		Workspace:    "default",
		Roles:        []core.Role{{Name: "tenant_owner"}},
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
	if _, err := store.GetByEmail(ctx, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("GetByEmail(unknown) err = %v, want ErrUnknownUser", err)
	}
	if list, err := store.ListUsers(ctx); err != nil || len(list) != 1 {
		t.Errorf("ListUsers = %v, %v", list, err)
	}
}
