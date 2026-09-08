// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func newAADTestSecrets(t *testing.T) (*EncryptedSecrets, *MemSecretsStore) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	store := NewMemSecretsStore()
	es, err := NewEncryptedSecrets(key, store)
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	return es, store
}

func TestEncryptedSecrets_CiphertextIsBoundToItsName(t *testing.T) {
	t.Parallel()
	es, store := newAADTestSecrets(t)
	ctx := context.Background()

	if err := es.Put(ctx, "acme", "conn.stripe.api_key", "sk_live_REAL"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := es.Put(ctx, "acme", "harmless", "nothing"); err != nil {
		t.Fatalf("put: %v", err)
	}

	ct, nonce, err := store.getSecret(ctx, "acme", "conn.stripe.api_key")
	if err != nil {
		t.Fatalf("getSecret: %v", err)
	}
	if err := store.putSecret(ctx, "acme", "harmless", ct, nonce); err != nil {
		t.Fatalf("putSecret: %v", err)
	}

	got, err := es.GetExact(ctx, "acme", "harmless")
	if err == nil {
		t.Fatalf("relocated ciphertext decrypted as %q; the (tenant, name) binding must reject it", got)
	}
}

func TestEncryptedSecrets_CiphertextIsBoundToItsTenant(t *testing.T) {
	t.Parallel()
	es, store := newAADTestSecrets(t)
	ctx := context.Background()

	if err := es.Put(ctx, "acme", "k", "acme-value"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := es.Put(ctx, "other", "k", "other-value"); err != nil {
		t.Fatalf("put: %v", err)
	}
	ct, nonce, err := store.getSecret(ctx, "acme", "k")
	if err != nil {
		t.Fatalf("getSecret: %v", err)
	}
	if err := store.putSecret(ctx, "other", "k", ct, nonce); err != nil {
		t.Fatalf("putSecret: %v", err)
	}
	if got, err := es.GetExact(ctx, "other", "k"); err == nil {
		t.Fatalf("cross-tenant ciphertext decrypted as %q; it must be rejected", got)
	}
}

func TestEncryptedSecrets_WrappedDEKIsBoundToItsTenant(t *testing.T) {
	t.Parallel()
	es, store := newAADTestSecrets(t)
	ctx := context.Background()

	if err := es.Put(ctx, "acme", "k", "v"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := es.Put(ctx, "victim", "k", "v"); err != nil {
		t.Fatalf("put: %v", err)
	}
	wrapped, nonce, err := store.getWrappedDEK(ctx, "acme")
	if err != nil {
		t.Fatalf("getWrappedDEK: %v", err)
	}
	if err := store.replaceWrappedDEK(ctx, "victim", wrapped, nonce); err != nil {
		t.Fatalf("replaceWrappedDEK: %v", err)
	}

	es.mu.Lock()
	delete(es.deks, "victim")
	es.mu.Unlock()

	if _, err := es.GetExact(ctx, "victim", "k"); err == nil {
		t.Fatal("a DEK moved into another tenant's row must fail to unwrap")
	}
}

// Ciphertext written before binding existed (nil AAD) must keep decrypting —
// otherwise the upgrade would strand every secret already on disk.
func TestEncryptedSecrets_LegacyUnboundCiphertextStillReads(t *testing.T) {
	t.Parallel()
	es, store := newAADTestSecrets(t)
	ctx := context.Background()

	if err := es.Put(ctx, "acme", "seed", "seed"); err != nil {
		t.Fatalf("put: %v", err)
	}
	dek, err := es.dekFor(ctx, "acme")
	if err != nil {
		t.Fatalf("dekFor: %v", err)
	}

	nonce := make([]byte, dek.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	legacy := dek.Seal(nil, nonce, []byte("legacy-value"), nil)
	if err := store.putSecret(ctx, "acme", "old", legacy, nonce); err != nil {
		t.Fatalf("putSecret: %v", err)
	}

	got, err := es.GetExact(ctx, "acme", "old")
	if err != nil {
		t.Fatalf("legacy ciphertext must still decrypt: %v", err)
	}
	if got != "legacy-value" {
		t.Errorf("got %q, want %q", got, "legacy-value")
	}

	if err := es.Put(ctx, "acme", "old", "new-value"); err != nil {
		t.Fatalf("put: %v", err)
	}
	ct, n, err := store.getSecret(ctx, "acme", "old")
	if err != nil {
		t.Fatalf("getSecret: %v", err)
	}
	if _, err := dek.Open(nil, n, ct, nil); err == nil {
		t.Error("rewritten record still opens with nil AAD; it should now be bound")
	}
	if _, err := dek.Open(nil, n, ct, secretAAD("acme", "old")); err != nil {
		t.Errorf("rewritten record should open with its binding: %v", err)
	}
}

func TestEncryptedSecrets_RewrapUpgradesLegacyDEKBinding(t *testing.T) {
	t.Parallel()
	es, store := newAADTestSecrets(t)
	ctx := context.Background()

	if err := es.Put(ctx, "acme", "k", "v"); err != nil {
		t.Fatalf("put: %v", err)
	}
	wrapped, nonce, err := store.getWrappedDEK(ctx, "acme")
	if err != nil {
		t.Fatalf("getWrappedDEK: %v", err)
	}
	dekBytes, err := openBound(es.kek, nonce, wrapped, dekAAD("acme"))
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	legacyNonce := make([]byte, es.kek.NonceSize())
	if _, err := rand.Read(legacyNonce); err != nil {
		t.Fatal(err)
	}
	if err := store.replaceWrappedDEK(ctx, "acme", es.kek.Seal(nil, legacyNonce, dekBytes, nil), legacyNonce); err != nil {
		t.Fatalf("replaceWrappedDEK: %v", err)
	}

	newKey := make([]byte, 32)
	if _, err := rand.Read(newKey); err != nil {
		t.Fatal(err)
	}
	rotated, _, err := es.RewrapDEKs(ctx, newKey)
	if err != nil {
		t.Fatalf("RewrapDEKs: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("rotated = %d, want 1", rotated)
	}

	// The re-wrapped DEK must be bound under the NEW key.
	block, err := aes.NewCipher(newKey)
	if err != nil {
		t.Fatal(err)
	}
	newKEK, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	w2, n2, err := store.getWrappedDEK(ctx, "acme")
	if err != nil {
		t.Fatalf("getWrappedDEK: %v", err)
	}
	if _, err := newKEK.Open(nil, n2, w2, nil); err == nil {
		t.Error("re-wrapped DEK still opens unbound; rotation should have bound it")
	}
	if _, err := newKEK.Open(nil, n2, w2, dekAAD("acme")); err != nil {
		t.Errorf("re-wrapped DEK should open with its tenant binding: %v", err)
	}
}

// The binding must not disturb ordinary use, including the flow→org cascade.
func TestEncryptedSecrets_BindingPreservesNormalResolution(t *testing.T) {
	t.Parallel()
	es, _ := newAADTestSecrets(t)
	ctx := core.WithTenant(context.Background(), "acme")

	if err := es.Put(ctx, "acme", "TOKEN", "org-value"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := es.Get(ctx, "TOKEN")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "org-value" {
		t.Errorf("got %q, want %q", got, "org-value")
	}

	flowCtx := core.WithFlow(ctx, "flow1")
	if err := es.Put(flowCtx, "acme", secretFlowPrefix+"flow1.TOKEN", "flow-value"); err != nil {
		t.Fatalf("put flow-scoped: %v", err)
	}
	got, err = es.Get(flowCtx, "TOKEN")
	if err != nil {
		t.Fatalf("get flow-scoped: %v", err)
	}
	if got != "flow-value" {
		t.Errorf("got %q, want %q", got, "flow-value")
	}
}
