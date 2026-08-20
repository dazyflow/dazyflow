// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
)

func newOrgAuthSecretsFixture(t *testing.T) (*memOrgAuth, auth.OrgAuthStore) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	inner := newMemOrgAuth()
	return inner, NewEncryptedOrgAuthStore(inner, es)
}

// TestEncryptedOrgAuthStore_SecretNeverHitsTheRow is the point of the whole
// decorator: whatever an admin saves, the org_auth row must not carry the
// client secret, because a database dump is the exposure being closed.
func TestEncryptedOrgAuthStore_SecretNeverHitsTheRow(t *testing.T) {
	inner, store := newOrgAuthSecretsFixture(t)
	ctx := context.Background()

	const secret = "GOCSPX-super-secret-value"
	if err := store.PutOrgAuth(ctx, auth.OrgAuthConfig{
		Tenant: "acme", GoogleClientID: "cid.apps.googleusercontent.com",
		GoogleClientSecret: secret, GoogleWorkspaceDomain: "acme.test",
	}); err != nil {
		t.Fatalf("PutOrgAuth: %v", err)
	}

	if got := inner.m["acme"].GoogleClientSecret; got != "" {
		t.Fatalf("plaintext secret reached the row: %q", got)
	}
	// The rest of the config still round-trips through the row.
	if inner.m["acme"].GoogleClientID != "cid.apps.googleusercontent.com" {
		t.Errorf("client_id = %q", inner.m["acme"].GoogleClientID)
	}
	if inner.m["acme"].GoogleWorkspaceDomain != "acme.test" {
		t.Errorf("domain = %q", inner.m["acme"].GoogleWorkspaceDomain)
	}

	// And the caller-visible view is unchanged: GetOrgAuth still yields the
	// plaintext, so every existing call site keeps working.
	got, err := store.GetOrgAuth(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrgAuth: %v", err)
	}
	if got.GoogleClientSecret != secret {
		t.Fatalf("secret = %q, want %q", got.GoogleClientSecret, secret)
	}
	if !got.GoogleEnabled() {
		t.Error("GoogleEnabled() = false — SSO would be silently disabled")
	}
}

// TestEncryptedOrgAuthStore_MigratesLegacyPlaintext covers the upgrade path:
// a row written before encryption existed is moved across on first read and
// the column is blanked, with no operator step.
func TestEncryptedOrgAuthStore_MigratesLegacyPlaintext(t *testing.T) {
	inner, store := newOrgAuthSecretsFixture(t)
	ctx := context.Background()

	// Seed the way the old code did — straight into the row.
	const legacy = "legacy-plaintext-secret"
	_ = inner.PutOrgAuth(ctx, auth.OrgAuthConfig{
		Tenant: "old", GoogleClientID: "cid", GoogleClientSecret: legacy,
	})

	got, err := store.GetOrgAuth(ctx, "old")
	if err != nil {
		t.Fatalf("GetOrgAuth: %v", err)
	}
	if got.GoogleClientSecret != legacy {
		t.Fatalf("secret = %q, want the legacy value %q — SSO must not break on upgrade", got.GoogleClientSecret, legacy)
	}
	if row := inner.m["old"].GoogleClientSecret; row != "" {
		t.Fatalf("row still holds plaintext after migration: %q", row)
	}
	// Second read comes from the encrypted store and still works.
	again, err := store.GetOrgAuth(ctx, "old")
	if err != nil || again.GoogleClientSecret != legacy {
		t.Fatalf("post-migration read = %q err=%v", again.GoogleClientSecret, err)
	}
}

// TestEncryptedOrgAuthStore_ClearAndDelete pins that clearing the secret and
// deleting the org both remove the ciphertext — a leftover would outlive the
// org it belonged to, which matters on the GDPR erasure path.
func TestEncryptedOrgAuthStore_ClearAndDelete(t *testing.T) {
	inner, store := newOrgAuthSecretsFixture(t)
	ctx := context.Background()

	put := func(secret string) {
		t.Helper()
		if err := store.PutOrgAuth(ctx, auth.OrgAuthConfig{
			Tenant: "acme", GoogleClientID: "cid", GoogleClientSecret: secret,
		}); err != nil {
			t.Fatalf("PutOrgAuth(%q): %v", secret, err)
		}
	}

	put("first-secret")
	put("") // explicit clear
	got, err := store.GetOrgAuth(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrgAuth: %v", err)
	}
	if got.GoogleClientSecret != "" {
		t.Fatalf("cleared secret came back as %q", got.GoogleClientSecret)
	}

	put("second-secret")
	if err := store.DeleteOrgAuth(ctx, "acme"); err != nil {
		t.Fatalf("DeleteOrgAuth: %v", err)
	}
	if _, err := store.GetOrgAuth(ctx, "acme"); !errors.Is(err, auth.ErrUnknownOrgAuth) {
		t.Fatalf("GetOrgAuth after delete = %v, want ErrUnknownOrgAuth", err)
	}
	// Re-create the row alone; the old ciphertext must not resurface.
	_ = inner.PutOrgAuth(ctx, auth.OrgAuthConfig{Tenant: "acme", GoogleClientID: "cid"})
	revived, err := store.GetOrgAuth(ctx, "acme")
	if err != nil {
		t.Fatalf("GetOrgAuth: %v", err)
	}
	if revived.GoogleClientSecret != "" {
		t.Fatalf("deleted secret resurfaced: %q", revived.GoogleClientSecret)
	}
}

// TestEncryptedOrgAuthStore_NoSecretStoreIsPassThrough documents the
// deliberate escape hatch: an install with no master key has nowhere to put
// the ciphertext, so wrapping is a no-op rather than a hard failure.
func TestEncryptedOrgAuthStore_NoSecretStoreIsPassThrough(t *testing.T) {
	inner := newMemOrgAuth()
	if got := NewEncryptedOrgAuthStore(inner, nil); got != auth.OrgAuthStore(inner) {
		t.Fatal("nil EncryptedSecrets should return the inner store unchanged")
	}
}
