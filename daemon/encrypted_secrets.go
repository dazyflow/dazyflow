// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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
//   - Key-rotating in place: rotating the KEK re-wraps every tenant DEK
//     under the new key. RewrapDEKs does this (dzd's
//     --rotate-master-key); the DEK plaintexts — and so every stored
//     ciphertext — are untouched, so no secret is re-entered.
//   - Audit-logged by default: secret reads aren't logged unless
//     DAZYFLOW_AUDIT_SECRET_READS is set (high-volume — resolution runs on
//     every node execution). When on, Get emits a "secret.read" audit event
//     (name + actor, never the value). See EnableReadAudit.
type EncryptedSecrets struct {
	mu    sync.Mutex
	kek   cipher.AEAD
	store secretsStore
	deks  map[string]cipher.AEAD // tenant → AEAD; cached after first unwrap

	// randReader sources the GCM nonces and freshly-generated DEKs. nil
	// means crypto/rand.Reader (the only production value); tests inject a
	// failing reader to exercise the must-halt-on-rand-failure paths —
	// proceeding with a zero/partial nonce would risk catastrophic AES-GCM
	// nonce reuse, so every read of it is checked.
	randReader io.Reader

	// readAudit, when non-nil, makes Get emit a best-effort "secret.read"
	// audit event (name + actor, never the value). Disabled by default —
	// secret resolution runs on every node execution, so it's high-volume —
	// and turned on via DAZYFLOW_AUDIT_SECRET_READS (see EnableReadAudit).
	readAudit core.AuditLog
}

// EnableReadAudit turns on best-effort auditing of secret reads, writing a
// "secret.read" event for every successful Get. Off by default; cmd/dzd calls
// this only when DAZYFLOW_AUDIT_SECRET_READS is set.
func (e *EncryptedSecrets) EnableReadAudit(a core.AuditLog) {
	e.readAudit = a
}

// auditRead records a best-effort "secret.read" event when read-auditing is
// enabled. It logs the tenant, actor, and secret NAME only — never the value
// (per the core.AuditEvent contract). A failed append is logged, never
// surfaced to the caller: auditing must not break secret resolution.
func (e *EncryptedSecrets) auditRead(ctx context.Context, tenant, name string) {
	if e.readAudit == nil {
		return
	}
	actor := "system"
	if p, ok := PrincipalFromContext(ctx); ok && p.Subject != "" {
		actor = p.Subject
	}
	var detail string
	if flow, ok := core.FlowFromContext(ctx); ok && flow != "" {
		detail = "flow=" + flow
	}
	if err := e.readAudit.Append(ctx, core.AuditEvent{
		Time:   time.Now(),
		Tenant: tenant,
		Actor:  actor,
		Action: "secret.read",
		Target: name,
		Detail: detail,
	}); err != nil {
		log.Printf("audit secret.read (%s/%s): %v", tenant, name, err)
	}
}

// rng returns the entropy source for nonces and DEK material, defaulting to
// crypto/rand.Reader when unset so struct-literal construction stays valid.
func (e *EncryptedSecrets) rng() io.Reader {
	if e.randReader != nil {
		return e.randReader
	}
	return rand.Reader
}

// SecretsBackend is the exported alias for the persistence boundary so
// callers outside this package (cmd/dzd) can hold a variable of the
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

	// listDEKTenants returns every tenant that has a wrapped DEK. Used
	// only by the KEK rotation path (RewrapDEKs); ordinary reads never
	// enumerate tenants.
	listDEKTenants(ctx context.Context) ([]string, error)

	// replaceWrappedDEK overwrites a tenant's wrapped DEK
	// unconditionally — the rotation re-wrap. Unlike setWrappedDEK it
	// always writes (the caller already holds the authoritative DEK
	// plaintext and is deliberately replacing the wrapping). Returns
	// ErrSecretNotFound if the tenant's DEK vanished between listing
	// and the update.
	replaceWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) error

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

// AES-GCM authenticates its additional data (AAD) without encrypting it, which
// lets us bind a ciphertext to the ROW it belongs in. Without that binding, GCM
// only proves a blob was sealed under the tenant's DEK — not that it was sealed
// under this NAME. An attacker with write access to the secrets table could
// therefore relocate ciphertext: copy the blob stored for conn.stripe.api_key
// into the row for some low-value secret their flow is allowed to read, and get
// the plaintext back out through an ordinary ${secret.…} reference. The same
// argument applies to a wrapped DEK moved between tenants.
//
// Binding (tenant, name) as AAD makes that relocation fail to authenticate. It
// costs nothing — AAD is hashed, not stored — and it is defense in depth: it
// only matters once someone already has database write access.
//
// The NUL separators plus the versioned prefix keep the encoding unambiguous,
// so no (tenant, name) pair can be spelled two ways.
func secretAAD(tenant, name string) []byte {
	return []byte("dazyflow/secret/v1\x00" + tenant + "\x00" + name)
}

// dekAAD binds a wrapped DEK to the tenant whose secrets it protects.
func dekAAD(tenant string) []byte {
	return []byte("dazyflow/dek/v1\x00" + tenant)
}

// openBound decrypts data that SHOULD carry the AAD binding above, falling back
// to an unbound open for ciphertext written before binding existed.
//
// The fallback is what makes this change deployable against a live store: every
// secret and wrapped DEK already on disk was sealed with nil AAD, and without
// it an upgrade would render all of them undecryptable.
//
// Residual risk, stated plainly: a legacy (unbound) ciphertext is still
// relocatable, because we must accept it. Values upgrade to the bound form as
// they are rewritten — every Put re-seals, so an OAuth refresh or a re-saved
// connection upgrades itself, and RewrapDEKs upgrades every DEK it touches. A
// deployment that wants the guarantee everywhere today can re-save its secrets;
// a future release can drop the fallback once no legacy ciphertext remains.
func openBound(aead cipher.AEAD, nonce, ct, aad []byte) ([]byte, error) {
	if pt, err := aead.Open(nil, nonce, ct, aad); err == nil {
		return pt, nil
	}
	return aead.Open(nil, nonce, ct, nil)
}

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

// Scheme returns "secret" — the one and only reference scheme for the store.
// Graphs reference any secret, at any scope, as ${secret.NAME}; the scope a
// value lives at is chosen when it's saved, and Get resolves by precedence.
func (e *EncryptedSecrets) Scheme() string { return "secret" }

// Get implements core.SecretProvider for the "secret" scheme. It resolves NAME
// with flow → organization precedence (GitHub-Actions style: the
// nearest-scoped value of that name wins). The flow ID is read from ctx (set
// by the engine via WithFlow); an empty one (e.g. the in-process Run path)
// drops out, degrading the cascade to the organization. Missing tenant in ctx
// is a hard error — refusing to fall through to a global namespace is the
// whole point of organization scoping.
func (e *EncryptedSecrets) Get(ctx context.Context, name string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("secret %q: no tenant in context", name)
	}
	flow, _ := core.FlowFromContext(ctx)

	candidates := make([]string, 0, 2)
	// Connection/OAuth namespaces are organization-authoritative: skip the
	// flow tier so a flow-scoped value can't shadow (and silently override) the
	// org credential. Genuine user secrets still get flow→org precedence. This
	// is defense in depth alongside validUserSecretName, which already blocks
	// writing a reserved-prefix name through the secret CRUD endpoint.
	if flow != "" && !orgAuthoritativeSecretName(name) {
		candidates = append(candidates, secretFlowPrefix+flow+"."+name)
	}
	candidates = append(candidates, name) // organization scope (bare name)

	var lastErr error
	for _, key := range candidates {
		v, err := e.getRaw(ctx, tenant, key)
		if err == nil {
			e.auditRead(ctx, tenant, name)
			return v, nil
		}
		if errors.Is(err, ErrSecretNotFound) {
			lastErr = err
			continue // try the next, broader scope
		}
		return "", err // decryption/store failure — operator-fixable, fail hard
	}
	return "", fmt.Errorf("secret://%s: %w", name, lastErr)
}

// GetExact reads a single value by its exact storage name within an
// explicit tenant — no flow→organization cascade, no tenant-from-context.
// It's the read counterpart of Put, used for daemon-internal bookkeeping
// stored under reserved name prefixes (e.g. a trigger's poll cursor under
// "cursor.…", which Get's cascade would mishandle). Returns
// ErrSecretNotFound when absent so callers can treat "never written" as a
// first-run rather than an error.
func (e *EncryptedSecrets) GetExact(ctx context.Context, tenant, name string) (string, error) {
	if tenant == "" {
		return "", fmt.Errorf("get secret %q: tenant required", name)
	}
	return e.getRaw(ctx, tenant, name)
}

// getRaw fetches and decrypts a single secret by its exact storage name
// within a tenant. Returns ErrSecretNotFound (from the store) when absent so
// callers can implement scope cascades; a decryption failure is surfaced as a
// structured error that doesn't leak crypto internals.
func (e *EncryptedSecrets) getRaw(ctx context.Context, tenant, storageName string) (string, error) {
	ct, nonce, err := e.store.getSecret(ctx, tenant, storageName)
	if err != nil {
		return "", err
	}
	dek, err := e.dekFor(ctx, tenant)
	if err != nil {
		return "", err
	}
	pt, err := openBound(dek, nonce, ct, secretAAD(tenant, storageName))
	if err != nil {
		return "", fmt.Errorf("tenant secret %q: decryption failed", storageName)
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
	if _, err := io.ReadFull(e.rng(), nonce); err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	// Bound to (tenant, name) so the ciphertext can't be relocated to another
	// row and read back through a reference the attacker is allowed to make.
	ct := dek.Seal(nil, nonce, []byte(value), secretAAD(tenant, name))
	return e.store.putSecret(ctx, tenant, name, ct, nonce)
}

// SealPayload encrypts an arbitrary blob under the tenant's DEK, for data that
// is not a named secret but must not sit in the database in cleartext — the
// runner task queue's script and stdin being the case this exists for.
//
// The queue is a transport that happens to be durable. engine/secrets.go's
// contract is that a resolved secret "exists only in the transport.Execute
// call"; a script the author wrote as `./sync.sh --key ${secret.STRIPE_KEY}`
// arrives here already expanded, so the row would otherwise carry the tenant's
// live credential until retention elapsed, readable from any dump, replica or
// backup with no Dazyflow permission at all.
//
// domain and id bind the ciphertext to the row it belongs in, exactly as
// secretAAD binds a secret to its name: without it, GCM only proves the blob
// was sealed under this tenant's DEK, so someone with write access could
// relocate a sealed script into a row they are allowed to read back.
//
// The returned blob is nonce||ciphertext; OpenPayload is its inverse.
func (e *EncryptedSecrets) SealPayload(ctx context.Context, tenant, domain, id string, plaintext []byte) ([]byte, error) {
	if tenant == "" {
		return nil, fmt.Errorf("seal payload: tenant required")
	}
	dek, err := e.dekFor(ctx, tenant)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, dek.NonceSize())
	if _, err := io.ReadFull(e.rng(), nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return append(nonce, dek.Seal(nil, nonce, plaintext, payloadAAD(tenant, domain, id))...), nil
}

// OpenPayload reverses SealPayload. It does NOT fall back to an unbound open
// the way openBound does: payload sealing is new, so there is no legacy
// ciphertext to accept, and accepting one would reopen the relocation hole.
func (e *EncryptedSecrets) OpenPayload(ctx context.Context, tenant, domain, id string, blob []byte) ([]byte, error) {
	if tenant == "" {
		return nil, fmt.Errorf("open payload: tenant required")
	}
	dek, err := e.dekFor(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if len(blob) < dek.NonceSize() {
		return nil, fmt.Errorf("open payload: ciphertext is too short to carry a nonce")
	}
	nonce, ct := blob[:dek.NonceSize()], blob[dek.NonceSize():]
	pt, err := dek.Open(nil, nonce, ct, payloadAAD(tenant, domain, id))
	if err != nil {
		return nil, fmt.Errorf("open payload (wrong DAZYFLOW_MASTER_KEY?): %w", err)
	}
	return pt, nil
}

// payloadAAD binds a sealed payload to (tenant, domain, row id). Same encoding
// discipline as secretAAD: a versioned prefix and NUL separators, so no triple
// can be spelled two ways.
func payloadAAD(tenant, domain, id string) []byte {
	return []byte("dazyflow/payload/v1\x00" + tenant + "\x00" + domain + "\x00" + id)
}

// RewrapDEKs rotates the KEK: it unwraps every tenant DEK with this
// store's current KEK and re-wraps it under newMasterKey, persisting the
// new wrapped form. The DEK plaintexts — and therefore every stored
// secret ciphertext — are unchanged; only the key that protects the DEKs
// rotates, so no secret has to be re-entered. Returns the count of DEKs
// re-wrapped and the count already on the new key.
//
// Re-runnable: a DEK that no longer unwraps under the current KEK but
// does under newMasterKey is counted as already-rotated and skipped, so
// a rotation interrupted partway can simply be run again with the same
// new key. A DEK that unwraps under neither key is a hard error (wrong
// current key) and stops the rotation, leaving the rest untouched.
//
// Operational flow (see SECURITY.md): run with the daemon's --master-key
// set to the CURRENT key and this new key, then restart the daemon with
// --master-key set to the new key. Keep the old key until that restart
// succeeds.
func (e *EncryptedSecrets) RewrapDEKs(ctx context.Context, newMasterKey []byte) (rotated, skipped int, err error) {
	if len(newMasterKey) != 32 {
		return 0, 0, fmt.Errorf("new master key must be 32 bytes, got %d", len(newMasterKey))
	}
	newBlock, err := aes.NewCipher(newMasterKey)
	if err != nil {
		return 0, 0, fmt.Errorf("new kek cipher: %w", err)
	}
	newKEK, err := cipher.NewGCM(newBlock)
	if err != nil {
		return 0, 0, fmt.Errorf("new kek gcm: %w", err)
	}

	tenants, err := e.store.listDEKTenants(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list tenant DEKs: %w", err)
	}
	for _, tenant := range tenants {
		wrapped, nonce, err := e.store.getWrappedDEK(ctx, tenant)
		if errors.Is(err, ErrSecretNotFound) {
			continue // raced delete between listing and read
		}
		if err != nil {
			return rotated, skipped, fmt.Errorf("read DEK for %q: %w", tenant, err)
		}

		aad := dekAAD(tenant)
		dekBytes, openErr := openBound(e.kek, nonce, wrapped, aad)
		if openErr != nil {
			// Doesn't unwrap under the current key — maybe a prior run
			// already rotated it. If the new key opens it, it's done.
			if _, newErr := openBound(newKEK, nonce, wrapped, aad); newErr == nil {
				skipped++
				continue
			}
			return rotated, skipped, fmt.Errorf("unwrap DEK for %q with current key (wrong DAZYFLOW_MASTER_KEY?): %w", tenant, openErr)
		}

		newNonce := make([]byte, newKEK.NonceSize())
		if _, err := io.ReadFull(e.rng(), newNonce); err != nil {
			return rotated, skipped, fmt.Errorf("new wrap nonce for %q: %w", tenant, err)
		}
		// Re-wrapping is also the upgrade path: a legacy unbound DEK comes out
		// through openBound's fallback and goes back in bound to its tenant.
		newWrapped := newKEK.Seal(nil, newNonce, dekBytes, aad)
		if err := e.store.replaceWrappedDEK(ctx, tenant, newWrapped, newNonce); err != nil {
			return rotated, skipped, fmt.Errorf("persist re-wrapped DEK for %q: %w", tenant, err)
		}
		rotated++
	}
	return rotated, skipped, nil
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
		if _, err := io.ReadFull(e.rng(), dekBytes); err != nil {
			return nil, fmt.Errorf("generate DEK: %w", err)
		}
		wrapNonce := make([]byte, e.kek.NonceSize())
		if _, err := io.ReadFull(e.rng(), wrapNonce); err != nil {
			return nil, fmt.Errorf("dek nonce: %w", err)
		}
		wrappedDEK := e.kek.Seal(nil, wrapNonce, dekBytes, dekAAD(tenant))
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

	dekBytes, err := openBound(e.kek, nonce, wrapped, dekAAD(tenant))
	if err != nil {
		return nil, fmt.Errorf("unwrap DEK for %q (wrong DAZYFLOW_MASTER_KEY?): %w", tenant, err)
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
