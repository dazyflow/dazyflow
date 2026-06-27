// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
)

// TestPgSecretsStore_DEKRotationMethods covers the key-rotation helpers
// listDEKTenants and replaceWrappedDEK (including the not-found leg).
func TestPgSecretsStore_DEKRotationMethods(t *testing.T) {
	pool, ctx := covPGPool(t)
	store, err := NewPgSecretsStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgSecretsStore: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE encrypted_secrets, encrypted_secret_deks"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// No DEKs yet.
	if tenants, err := store.listDEKTenants(ctx); err != nil || len(tenants) != 0 {
		t.Fatalf("listDEKTenants(empty) = %v / %v", tenants, err)
	}

	// Seed two tenants' wrapped DEKs.
	if wrote, err := store.setWrappedDEK(ctx, "acme", []byte("wrap-a"), []byte("nonce-a")); err != nil || !wrote {
		t.Fatalf("setWrappedDEK acme = %v / %v", wrote, err)
	}
	if wrote, err := store.setWrappedDEK(ctx, "beta", []byte("wrap-b"), []byte("nonce-b")); err != nil || !wrote {
		t.Fatalf("setWrappedDEK beta = %v / %v", wrote, err)
	}
	// A second write for the same tenant is a no-op (ON CONFLICT DO NOTHING).
	if wrote, err := store.setWrappedDEK(ctx, "acme", []byte("ignored"), []byte("ignored")); err != nil || wrote {
		t.Fatalf("setWrappedDEK acme (dup) = %v / %v, want false", wrote, err)
	}

	// listDEKTenants returns both, ordered.
	tenants, err := store.listDEKTenants(ctx)
	if err != nil || len(tenants) != 2 || tenants[0] != "acme" || tenants[1] != "beta" {
		t.Fatalf("listDEKTenants = %v / %v, want [acme beta]", tenants, err)
	}

	// replaceWrappedDEK rewraps an existing tenant's DEK.
	if err := store.replaceWrappedDEK(ctx, "acme", []byte("wrap-a2"), []byte("nonce-a2")); err != nil {
		t.Fatalf("replaceWrappedDEK: %v", err)
	}
	w, n, err := store.getWrappedDEK(ctx, "acme")
	if err != nil || string(w) != "wrap-a2" || string(n) != "nonce-a2" {
		t.Fatalf("getWrappedDEK after replace = %q/%q / %v", w, n, err)
	}

	// replaceWrappedDEK on an unknown tenant -> ErrSecretNotFound.
	if err := store.replaceWrappedDEK(ctx, "ghost", []byte("x"), []byte("y")); err != ErrSecretNotFound {
		t.Fatalf("replaceWrappedDEK(ghost) = %v, want ErrSecretNotFound", err)
	}
}
