// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	"github.com/dazyflow/dazyflow/core"
)

// Envelope encryption: a per-tenant DEK, itself wrapped under the deployment's
// master KEK. Plaintext exists only inside a call.
type EncryptedSecrets struct {
	mu    sync.Mutex
	kek   cipher.AEAD
	store secretsStore
	deks  map[string]cipher.AEAD // tenant → AEAD; cached after first unwrap

	// nil means crypto/rand; tests override it.
	randReader io.Reader

	// Best-effort: a failed audit write must never fail the read.
	readAudit core.AuditLog
}

func (e *EncryptedSecrets) EnableReadAudit(a core.AuditLog) {
	e.readAudit = a
}

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

func (e *EncryptedSecrets) rng() io.Reader {
	if e.randReader != nil {
		return e.randReader
	}
	return rand.Reader
}

type SecretsBackend = secretsStore

type secretsStore interface {
	putSecret(ctx context.Context, tenant, name string, ciphertext, nonce []byte) error

	getSecret(ctx context.Context, tenant, name string) (ciphertext, nonce []byte, err error)

	deleteSecret(ctx context.Context, tenant, name string) error

	listSecretNames(ctx context.Context, tenant string) ([]string, error)

	// The DEK goes too, or the rows are unreadable but still present.
	deleteTenant(ctx context.Context, tenant string) (int, error)

	getWrappedDEK(ctx context.Context, tenant string) (wrapped, nonce []byte, err error)

	listDEKTenants(ctx context.Context) ([]string, error)

	replaceWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) error

	// Must not overwrite an existing DEK, or every secret under it is lost.
	setWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) (wrote bool, err error)
}

var ErrSecretNotFound = errors.New("secret not found")

// AES-GCM authenticates its additional data without encrypting it, so binding
// the tenant and name into the AAD makes a ciphertext undecryptable anywhere but
// the row it was written to — a swapped row fails to open rather than decrypting
// to another tenant's value.
func secretAAD(tenant, name string) []byte {
	return []byte("dazyflow/secret/v1\x00" + tenant + "\x00" + name)
}

func dekAAD(tenant string) []byte {
	return []byte("dazyflow/dek/v1\x00" + tenant)
}

func openBound(aead cipher.AEAD, nonce, ct, aad []byte) ([]byte, error) {
	if pt, err := aead.Open(nil, nonce, ct, aad); err == nil {
		return pt, nil
	}
	return aead.Open(nil, nonce, ct, nil)
}

// masterKey must be exactly 32 bytes.
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

func (e *EncryptedSecrets) Scheme() string { return "secret" }

// Resolves NAME by scope precedence: flow before organization.
func (e *EncryptedSecrets) Get(ctx context.Context, name string) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("secret %q: no tenant in context", name)
	}
	flow, _ := core.FlowFromContext(ctx)

	candidates := make([]string, 0, 2)
	// Organization-authoritative: a flow-scoped value must not be able to shadow a
	// connection's credential.
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

// No scope cascade: the caller has already resolved the name.
func (e *EncryptedSecrets) GetExact(ctx context.Context, tenant, name string) (string, error) {
	if tenant == "" {
		return "", fmt.Errorf("get secret %q: tenant required", name)
	}
	return e.getRaw(ctx, tenant, name)
}

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
	ct := dek.Seal(nil, nonce, []byte(value), secretAAD(tenant, name))
	return e.store.putSecret(ctx, tenant, name, ct, nonce)
}

// Bound to (tenant, name) through the AAD like a secret, so a sealed blob cannot
// be moved between rows or tenants.
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

// Deliberately no unbound fallback: that would defeat the AAD binding.
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

func payloadAAD(tenant, domain, id string) []byte {
	return []byte("dazyflow/payload/v1\x00" + tenant + "\x00" + domain + "\x00" + id)
}

// Rotates the KEK by re-wrapping every tenant DEK. The DEKs themselves are
// unchanged, so no secret is re-encrypted and the operation is resumable.
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
		newWrapped := newKEK.Seal(nil, newNonce, dekBytes, aad)
		if err := e.store.replaceWrappedDEK(ctx, tenant, newWrapped, newNonce); err != nil {
			return rotated, skipped, fmt.Errorf("persist re-wrapped DEK for %q: %w", tenant, err)
		}
		rotated++
	}
	return rotated, skipped, nil
}

func (e *EncryptedSecrets) Delete(ctx context.Context, tenant, name string) error {
	if tenant == "" {
		return fmt.Errorf("delete secret %q: tenant required", name)
	}
	return e.store.deleteSecret(ctx, tenant, name)
}

// Backs the GDPR erasure cascade.
func (e *EncryptedSecrets) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	if tenant == "" {
		return 0, fmt.Errorf("delete tenant secrets: tenant required")
	}
	n, err := e.store.deleteTenant(ctx, tenant)
	// Even on error: the store may have committed part of the delete.
	e.mu.Lock()
	delete(e.deks, tenant)
	e.mu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("delete secrets for %q: %w", tenant, err)
	}
	return n, nil
}

func (e *EncryptedSecrets) List(ctx context.Context, tenant string) ([]string, error) {
	if tenant == "" {
		return nil, fmt.Errorf("list secrets: tenant required")
	}
	return e.store.listSecretNames(ctx, tenant)
}

// Lazily provisions a DEK on first use, and caches the AEAD.
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

var _ core.SecretProvider = (*EncryptedSecrets)(nil)
