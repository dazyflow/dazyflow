package auth

import (
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestMigrateLegacyOrgAdminPerm(t *testing.T) {
	pool, ctx := testPool(t)
	// The migration also touches the memberships table, which testPool's
	// auth-only schema doesn't create — ensure it and start clean.
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		t.Fatalf("EnsurePgOrgsSchema: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE memberships")

	store, err := NewPgUserStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgUserStore: %v", err)
	}
	// A pre-rename account: its tenant_owner role carries the dead
	// "tenant:admin" string instead of "organization:admin".
	u := User{
		Email:        "owner@example.com",
		PasswordHash: []byte("x"),
		Subject:      "owner@example.com",
		Tenant:       "acme",
		Workspace:    "main",
		Roles:        []core.Role{{Name: "tenant_owner", Permissions: []core.Permission{core.Permission("tenant:admin")}}},
		CreatedAt:    time.Now().UTC(),
	}
	if err := store.PutUser(ctx, u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}

	n, err := MigrateLegacyOrgAdminPerm(ctx, pool)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least 1 row migrated, got %d", n)
	}

	got, err := store.GetByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if len(got.Roles) != 1 || !got.Roles[0].Has(core.PermOrganizationAdmin) {
		t.Errorf("role not migrated to organization:admin: %+v", got.Roles)
	}
	if got.Roles[0].Has(core.Permission("tenant:admin")) {
		t.Errorf("legacy tenant:admin still present: %+v", got.Roles)
	}

	// Idempotent: a second run rewrites nothing.
	n2, err := MigrateLegacyOrgAdminPerm(ctx, pool)
	if err != nil {
		t.Fatalf("migrate#2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second run affected %d rows, want 0 (idempotent)", n2)
	}
}
