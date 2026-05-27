package daemon

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// EncryptedSecrets is the built-in per-tenant secret store. It's the
// daemon's "we hold your tokens, you don't need any infrastructure"
// answer to the secrets problem — what an SMB customer expects from
// a Zapier-shaped product.
//
// Crypto: envelope encryption with two key tiers.
//
//	KEK (Key Encryption Key)  — 32-byte AES-256, held in daemon
//	                            memory, loaded from config at boot.
//	                            Never written to disk by us.
//	DEK (Data Encryption Key) — 32-byte AES-256, one per tenant.
//	                            Generated on first secret write per
//	                            tenant, then KEK-wrapped and stored.
//	                            On reads, fetched + unwrapped + cached
//	                            in process memory.
//
// Each secret has its own 12-byte nonce (NIST-recommended for GCM).
// AES-256-GCM is authenticated, so tampering with ciphertext on the
// way back from the store surfaces as a decryption error rather
// than silent corruption.
//
// Why this design over BYO cloud (Vault/AWS/GCP):
//
//   - SMB customers don't have those backends. The point is they
//     don't have to.
//   - One process boundary for secrets simplifies the operational
//     surface (one master key to manage, one DB to back up).
//   - Cloud BYO providers slot in later as different
//     `core.SecretProvider` implementations — same scheme registry,
//     different backend.
//
// What this DELIBERATELY isn't:
//
//   - HSM-backed: the KEK lives in process memory. An attacker with
//     memory dump access wins. HSM-backed KEK is a future option.
//   - Key-rotating: rotating the KEK means re-wrapping every DEK,
//     which needs an explicit rotation flow. v2.
//   - Audit-logged: secret reads aren't logged today. Add when
//     compliance asks.
type EncryptedSecrets struct {
	mu    sync.Mutex
	kek   cipher.AEAD
	store secretsStore
	deks  map[string]cipher.AEAD // tenant → AEAD; cached after first unwrap
}

// SecretsBackend is the exported alias for the persistence boundary so
// callers outside this package (cmd/hzd) can hold a variable of the
// store type and pass either backend to NewEncryptedSecrets. The
// methods stay unexported, so only this package's MemSecretsStore /
// PgSecretsStore can implement it.
type SecretsBackend = secretsStore

// secretsStore is the persistence boundary. We split storage out so
// tests can run against an in-memory map without spinning up
// Postgres, and so the same crypto wraps a Postgres backend in
// production.
type secretsStore interface {
	// putSecret writes (or overwrites) a tenant's secret. Idempotent
	// for the same (tenant, name); a re-write produces a fresh
	// (ciphertext, nonce) pair since the nonce is regenerated per
	// Put — never reuse a nonce with the same key.
	putSecret(ctx context.Context, tenant, name string, ciphertext, nonce []byte) error

	// getSecret returns the stored (ciphertext, nonce) for a secret,
	// or ErrSecretNotFound. Decryption happens in the provider.
	getSecret(ctx context.Context, tenant, name string) (ciphertext, nonce []byte, err error)

	// deleteSecret removes a secret. Returns nil even if absent (no
	// distinction between "wasn't there" and "now isn't there" —
	// idempotent delete is the friendlier API).
	deleteSecret(ctx context.Context, tenant, name string) error

	// listSecretNames returns the names of every secret stored for
	// tenant, alphabetically. Values are intentionally NOT returned
	// — the UI shows "Slack token: set", never the value back.
	listSecretNames(ctx context.Context, tenant string) ([]string, error)

	// getWrappedDEK returns the tenant's wrapped DEK or
	// ErrSecretNotFound if no DEK has been generated yet. Called on
	// first secret access per tenant per process; subsequent calls
	// hit the EncryptedSecrets in-memory cache.
	getWrappedDEK(ctx context.Context, tenant string) (wrapped, nonce []byte, err error)

	// setWrappedDEK persists a freshly-generated wrapped DEK.
	// Returns (true, nil) when THIS call persisted the DEK — the
	// caller can safely use its local wrappedDEK/nonce as the
	// authoritative store contents.
	// Returns (false, nil) when a concurrent writer's DEK was
	// already in the store; the caller MUST re-read via
	// getWrappedDEK to observe the winning DEK before encrypting
	// or decrypting (otherwise the local DEK is orphaned and any
	// ciphertext written with it can't be decrypted by future
	// readers, who will load the winner's DEK).
	// Returns (_, non-nil) on transient/structural failure (DB
	// connection, etc.); the caller propagates as a provisioning
	// error.
	setWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) (wrote bool, err error)
}

// ErrSecretNotFound is the sentinel for "no such secret/DEK exists"
// returned by secretsStore implementations. The provider translates
// it into a SecretProvider-shaped error before bubbling up.
var ErrSecretNotFound = errors.New("secret not found")

// NewEncryptedSecrets constructs the provider. masterKey must be
// exactly 32 bytes (AES-256). The caller owns key material — load
// it from a CLI flag, env var, sealed secret, KMS, whatever. We
// don't read it from anywhere ourselves to keep the bootstrap
// dependencies minimal.
func NewEncryptedSecrets(masterKey []byte, store secretsStore) (*EncryptedSecrets, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	if store == nil {
		return nil, fmt.Errorf("store must not be nil")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("kek cipher: %w", err)
	}
	kek, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("kek gcm: %w", err)
	}
	return &EncryptedSecrets{
		kek:   kek,
		store: store,
		deks:  make(map[string]cipher.AEAD),
	}, nil
}

// Scheme returns "tenant" — the scheme reflects the SCOPE of the
// store (per-tenant) rather than its implementation (encrypted).
// Graphs reference secrets as `tenant://slack_token`; tenant comes
// from the job's principal, never from the path itself.
func (e *EncryptedSecrets) Scheme() string { return "tenant" }

// Get implements core.SecretProvider. Reads the tenant from ctx
// (set by Engine.RunNode via core.WithTenant), fetches the
// ciphertext, decrypts with the cached/loaded DEK, and returns the
// plaintext. Missing tenant in ctx is a hard error — refusing to
// silently fall through to a global namespace is the whole point
// of tenant scoping.
func (e *EncryptedSecrets) Get(ctx context.Context, name string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("tenant secret %q: no tenant in context", name)
	}
	ct, nonce, err := e.store.getSecret(ctx, tenant, name)
	if err != nil {
		return "", fmt.Errorf("tenant://%s: %w", name, err)
	}
	dek, err := e.dekFor(ctx, tenant)
	if err != nil {
		return "", err
	}
	pt, err := dek.Open(nil, nonce, ct, nil)
	if err != nil {
		// Auth-failure on Open means either tampered ciphertext OR
		// a KEK rotation we don't support yet OR a DB row from a
		// different KEK. All three are operator-fixable, not user-
		// fixable, so the error is structured but doesn't leak
		// crypto internals.
		return "", fmt.Errorf("tenant://%s: decryption failed", name)
	}
	return string(pt), nil
}

// Put writes (or overwrites) a tenant's secret. Called from the
// daemon's HTTP CRUD endpoints and (later) from the OAuth callback
// when a connector authorizes a new account.
func (e *EncryptedSecrets) Put(ctx context.Context, tenant, name, value string) error {
	if tenant == "" {
		return fmt.Errorf("put secret %q: tenant required", name)
	}
	if name == "" {
		return fmt.Errorf("put secret: name required")
	}
	dek, err := e.dekFor(ctx, tenant)
	if err != nil {
		return err
	}
	nonce := make([]byte, dek.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	ct := dek.Seal(nil, nonce, []byte(value), nil)
	return e.store.putSecret(ctx, tenant, name, ct, nonce)
}

// Delete removes a secret. Idempotent — "delete a secret that's
// already gone" succeeds, matching the friendlier REST convention.
func (e *EncryptedSecrets) Delete(ctx context.Context, tenant, name string) error {
	if tenant == "" {
		return fmt.Errorf("delete secret %q: tenant required", name)
	}
	return e.store.deleteSecret(ctx, tenant, name)
}

// List returns secret names for a tenant. The values are
// intentionally never returned by this API — the UI shows that a
// secret is "set", not what it is.
func (e *EncryptedSecrets) List(ctx context.Context, tenant string) ([]string, error) {
	if tenant == "" {
		return nil, fmt.Errorf("list secrets: tenant required")
	}
	return e.store.listSecretNames(ctx, tenant)
}

// dekFor returns the AEAD for a tenant's DEK, lazily provisioning
// one on first call (per process). The three states:
//
//  1. Cache hit             → return immediately, no I/O.
//  2. DEK exists in store   → fetch wrapped form, unwrap with KEK,
//     cache, return.
//  3. DEK doesn't exist     → generate, wrap, persist, cache, return.
//     Race-safe: if another caller wrote
//     first, we observe the existing one
//     on the next pass.
func (e *EncryptedSecrets) dekFor(ctx context.Context, tenant string) (cipher.AEAD, error) {
	e.mu.Lock()
	cached, ok := e.deks[tenant]
	e.mu.Unlock()
	if ok {
		return cached, nil
	}

	wrapped, nonce, err := e.store.getWrappedDEK(ctx, tenant)
	switch {
	case errors.Is(err, ErrSecretNotFound):
		// First-write path: provision a new DEK.
		dekBytes := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, dekBytes); err != nil {
			return nil, fmt.Errorf("generate DEK: %w", err)
		}
		wrapNonce := make([]byte, e.kek.NonceSize())
		if _, err := io.ReadFull(rand.Reader, wrapNonce); err != nil {
			return nil, fmt.Errorf("dek nonce: %w", err)
		}
		wrappedDEK := e.kek.Seal(nil, wrapNonce, dekBytes, nil)
		wrote, err := e.store.setWrappedDEK(ctx, tenant, wrappedDEK, wrapNonce)
		if err != nil {
			return nil, fmt.Errorf("provision DEK for %q: %w", tenant, err)
		}
		if wrote {
			wrapped, nonce = wrappedDEK, wrapNonce
		} else {
			// A concurrent writer beat us; the local wrappedDEK is now
			// orphaned. Re-read the store so we encrypt under the
			// winner's DEK — otherwise our Put would write ciphertext
			// future readers can't decrypt.
			w, n, getErr := e.store.getWrappedDEK(ctx, tenant)
			if getErr != nil {
				return nil, fmt.Errorf("re-read winning DEK for %q after lost race: %w", tenant, getErr)
			}
			wrapped, nonce = w, n
		}
	case err != nil:
		return nil, fmt.Errorf("load DEK for %q: %w", tenant, err)
	}

	dekBytes, err := e.kek.Open(nil, nonce, wrapped, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap DEK for %q (wrong master key?): %w", tenant, err)
	}
	block, err := aes.NewCipher(dekBytes)
	if err != nil {
		return nil, fmt.Errorf("dek cipher: %w", err)
	}
	dek, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("dek gcm: %w", err)
	}

	e.mu.Lock()
	e.deks[tenant] = dek
	e.mu.Unlock()
	return dek, nil
}

// Compile-time interface check.
var _ core.SecretProvider = (*EncryptedSecrets)(nil)
