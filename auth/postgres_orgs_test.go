// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Real-DB tests for the four org-level Pg stores. Gated on
// DAZYFLOW_TEST_DB so `go test ./auth/...` stays green without a
// running Postgres, but CI (.build.yml) wires one up and these run.

func orgsPool(t *testing.T) (*pgxpool.Pool, context.Context) {
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
	if err := EnsurePgOrgsSchema(ctx, pool); err != nil {
		t.Fatalf("EnsurePgOrgsSchema: %v", err)
	}
	_, _ = pool.Exec(ctx, "TRUNCATE memberships, invitations, org_auth, org_profiles")
	return pool, ctx
}

func TestPgMembershipStore_RoundTrip(t *testing.T) {
	pool, ctx := orgsPool(t)
	store, err := NewPgMembershipStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgMembershipStore: %v", err)
	}
	roles := []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}}
	m := Membership{
		UserEmail: "Alice@ACME.com",
		Tenant:    "acme",
		Workspace: "ws1",
		Roles:     roles,
		InvitedBy: "mallory@acme.com",
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := store.PutMembership(ctx, m); err != nil {
		t.Fatalf("PutMembership: %v", err)
	}

	// Email is normalized to lowercase on write; Get with mixed case
	// should still find the row.
	got, err := store.GetMembership(ctx, "alice@acme.com", "acme")
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if got.UserEmail != "alice@acme.com" || got.Tenant != "acme" || got.InvitedBy != "mallory@acme.com" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if len(got.Roles) != 1 || got.Roles[0].Name != "editor" {
		t.Errorf("roles round-trip mismatch: %+v", got.Roles)
	}

	// Upsert: same (email, tenant) updates instead of inserting.
	m2 := m
	m2.Workspace = "ws2"
	if err := store.PutMembership(ctx, m2); err != nil {
		t.Fatalf("PutMembership update: %v", err)
	}
	got, _ = store.GetMembership(ctx, "alice@acme.com", "acme")
	if got.Workspace != "ws2" {
		t.Errorf("upsert workspace = %q, want ws2", got.Workspace)
	}

	// List by email — adding a second org for Alice and verifying both
	// come back, sorted by tenant.
	if err := store.PutMembership(ctx, Membership{
		UserEmail: "alice@acme.com", Tenant: "globex", Workspace: "ws1", Roles: roles,
	}); err != nil {
		t.Fatalf("PutMembership 2: %v", err)
	}
	byEmail, err := store.ListByEmail(ctx, "alice@acme.com")
	if err != nil {
		t.Fatalf("ListByEmail: %v", err)
	}
	if len(byEmail) != 2 || byEmail[0].Tenant != "acme" || byEmail[1].Tenant != "globex" {
		t.Errorf("ListByEmail = %+v", byEmail)
	}

	// List by tenant: only Alice in acme so far.
	byTenant, _ := store.ListByTenant(ctx, "acme")
	if len(byTenant) != 1 || byTenant[0].UserEmail != "alice@acme.com" {
		t.Errorf("ListByTenant = %+v", byTenant)
	}

	// Delete + reread → ErrUnknownMembership.
	if err := store.DeleteMembership(ctx, "alice@acme.com", "acme"); err != nil {
		t.Fatalf("DeleteMembership: %v", err)
	}
	_, err = store.GetMembership(ctx, "alice@acme.com", "acme")
	if !errors.Is(err, ErrUnknownMembership) {
		t.Errorf("expected ErrUnknownMembership, got %v", err)
	}
}

func TestPgInvitationStore_RoundTrip(t *testing.T) {
	pool, ctx := orgsPool(t)
	store, err := NewPgInvitationStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgInvitationStore: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	inv := Invitation{
		Token:     "inv_abc",
		Email:     "Bob@example.com",
		Tenant:    "acme",
		Workspace: "ws1",
		Roles:     []core.Role{{Name: "editor", Permissions: []core.Permission{core.PermGraphRun}}},
		InvitedBy: "mallory@acme.com",
		CreatedAt: now,
		ExpiresAt: now.Add(7 * 24 * time.Hour),
	}
	if err := store.PutInvitation(ctx, inv); err != nil {
		t.Fatalf("PutInvitation: %v", err)
	}

	got, err := store.GetByToken(ctx, "inv_abc")
	if err != nil {
		t.Fatalf("GetByToken: %v", err)
	}
	// Email is lowercased on write.
	if got.Email != "bob@example.com" || got.Tenant != "acme" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.AcceptedAt != nil || got.RevokedAt != nil {
		t.Errorf("pending invitation has accepted/revoked = %+v", got)
	}
	if !got.IsPending(now) {
		t.Errorf("freshly-put invitation not pending: %+v", got)
	}

	// MarkAccepted updates the row.
	acceptedAt := now.Add(1 * time.Hour)
	if err := store.MarkAccepted(ctx, "inv_abc", acceptedAt); err != nil {
		t.Fatalf("MarkAccepted: %v", err)
	}
	got, _ = store.GetByToken(ctx, "inv_abc")
	if got.AcceptedAt == nil || !got.AcceptedAt.Equal(acceptedAt) {
		t.Errorf("accepted_at = %v, want %v", got.AcceptedAt, acceptedAt)
	}

	// MarkAccepted on a missing token returns ErrUnknownInvitation.
	err = store.MarkAccepted(ctx, "nope", now)
	if !errors.Is(err, ErrUnknownInvitation) {
		t.Errorf("expected ErrUnknownInvitation, got %v", err)
	}

	// Same for MarkRevoked.
	err = store.MarkRevoked(ctx, "nope", now)
	if !errors.Is(err, ErrUnknownInvitation) {
		t.Errorf("expected ErrUnknownInvitation, got %v", err)
	}

	// List by tenant: should include the accepted invitation, ordered
	// by created_at DESC.
	list, err := store.ListByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 1 || list[0].Token != "inv_abc" {
		t.Errorf("ListByTenant = %+v", list)
	}
}

func TestPgOrgAuthStore_RoundTrip(t *testing.T) {
	pool, ctx := orgsPool(t)
	store, err := NewPgOrgAuthStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgOrgAuthStore: %v", err)
	}
	cfg := OrgAuthConfig{
		Tenant:                "acme",
		GoogleClientID:        "1234-abc.apps.googleusercontent.com",
		GoogleClientSecret:    "secret",
		GoogleWorkspaceDomain: "acme.com",
	}
	if err := store.PutOrgAuth(ctx, cfg); err != nil {
		t.Fatalf("PutOrgAuth: %v", err)
	}
	got, err := store.GetOrgAuth(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrgAuth: %v", err)
	}
	if !got.GoogleEnabled() || got.GoogleClientID != cfg.GoogleClientID {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Upsert: new client_id replaces the old one.
	cfg.GoogleClientID = "5678-def.apps.googleusercontent.com"
	if err := store.PutOrgAuth(ctx, cfg); err != nil {
		t.Fatalf("PutOrgAuth upsert: %v", err)
	}
	got, _ = store.GetOrgAuth(ctx, "acme")
	if got.GoogleClientID != cfg.GoogleClientID {
		t.Errorf("upsert client_id = %q, want %q", got.GoogleClientID, cfg.GoogleClientID)
	}

	// Delete + reread → ErrUnknownOrgAuth.
	if err := store.DeleteOrgAuth(ctx, "acme"); err != nil {
		t.Fatalf("DeleteOrgAuth: %v", err)
	}
	_, err = store.GetOrgAuth(ctx, "acme")
	if !errors.Is(err, ErrUnknownOrgAuth) {
		t.Errorf("expected ErrUnknownOrgAuth, got %v", err)
	}
}

func TestPgOrgProfileStore_RoundTrip(t *testing.T) {
	pool, ctx := orgsPool(t)
	store, err := NewPgOrgProfileStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgOrgProfileStore: %v", err)
	}
	if err := store.PutOrgProfile(ctx, OrgProfile{Tenant: "acme", DisplayName: "Acme Inc."}); err != nil {
		t.Fatalf("PutOrgProfile: %v", err)
	}
	if err := store.PutOrgProfile(ctx, OrgProfile{Tenant: "globex", DisplayName: "Globex Corp."}); err != nil {
		t.Fatalf("PutOrgProfile globex: %v", err)
	}

	got, err := store.GetOrgProfile(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrgProfile: %v", err)
	}
	if got.DisplayName != "Acme Inc." {
		t.Errorf("DisplayName = %q, want Acme Inc.", got.DisplayName)
	}

	_, err = store.GetOrgProfile(ctx, "unknown")
	if !errors.Is(err, ErrUnknownOrgProfile) {
		t.Errorf("expected ErrUnknownOrgProfile, got %v", err)
	}

	// Bulk list returns only the requested tenants, silently dropping
	// the unknown ones (the UI falls back to the raw ID).
	bulk, err := store.ListOrgProfiles(ctx, []string{"acme", "globex", "unknown"})
	if err != nil {
		t.Fatalf("ListOrgProfiles: %v", err)
	}
	if len(bulk) != 2 || bulk["acme"].DisplayName != "Acme Inc." || bulk["globex"].DisplayName != "Globex Corp." {
		t.Errorf("ListOrgProfiles = %+v", bulk)
	}
	if _, ok := bulk["unknown"]; ok {
		t.Error("unknown tenant should not appear in bulk result")
	}
}
