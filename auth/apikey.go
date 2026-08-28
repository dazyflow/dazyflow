// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// API key wire format: "dzk_<key-id>_<secret-hex>".
// We hash the secret with SHA-256 + per-key salt before storing.
const apiKeyPrefix = "dzk_"

// APIKey is the on-disk record. Hashed secret only — the cleartext is
// returned exactly once at issue time and never persisted.
type APIKey struct {
	ID        string
	Tenant    string
	Workspace string
	Subject   string
	Roles     []core.Role
	Salt      []byte
	Hash      []byte
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// APIKeyStore is the lookup boundary the Authenticator uses — read-only
// from its perspective. Kept minimal so test mocks for Authenticate
// don't have to implement admin write methods.
type APIKeyStore interface {
	GetKey(ctx context.Context, id string) (APIKey, error)
}

// AdminKeyStore is the richer interface admin tooling needs: read,
// list (tenant-scoped), write, and revoke. MemKeyStore satisfies it;
// production Postgres-backed implementations should as well. Daemon
// admin endpoints wire to this — without it those endpoints 501.
type AdminKeyStore interface {
	APIKeyStore
	PutKey(ctx context.Context, k APIKey) error
	Revoke(ctx context.Context, id string, at time.Time) error
	ListByTenant(ctx context.Context, tenant string) ([]APIKey, error)
	// ListAll returns every key in the store regardless of tenant.
	// Used by platform-admin paths that need to enumerate tenants
	// (the only durable "set of tenants" today is the union of
	// tenants represented in active keys).
	ListAll(ctx context.Context) ([]APIKey, error)
}

type APIKeyAuthenticator struct {
	Store APIKeyStore
	Clock func() time.Time
}

func (a *APIKeyAuthenticator) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if !strings.HasPrefix(credential, apiKeyPrefix) {
		return core.Principal{}, ErrInvalidCredential
	}
	rest := credential[len(apiKeyPrefix):]
	parts := strings.SplitN(rest, "_", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return core.Principal{}, ErrInvalidCredential
	}
	id, secretHex := parts[0], parts[1]

	key, err := a.Store.GetKey(ctx, id)
	if err != nil {
		return core.Principal{}, ErrInvalidCredential
	}
	if key.RevokedAt != nil {
		return core.Principal{}, fmt.Errorf("%w: revoked", ErrInvalidCredential)
	}
	if key.ExpiresAt != nil && a.now().After(*key.ExpiresAt) {
		return core.Principal{}, fmt.Errorf("%w: expired", ErrInvalidCredential)
	}
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		return core.Principal{}, ErrInvalidCredential
	}
	candidate := sha256Salted(key.Salt, secret)
	if subtle.ConstantTimeCompare(candidate, key.Hash) != 1 {
		return core.Principal{}, ErrInvalidCredential
	}
	return core.Principal{
		Subject:   key.Subject,
		Tenant:    key.Tenant,
		Workspace: key.Workspace,
		Roles:     key.Roles,
	}, nil
}

// IsAPIKeyCredential reports whether a credential is an API key (the
// "dzk_<id>_<secret>" wire format) rather than a session token. Callers use it
// where policy differs by credential KIND rather than by permission — a
// password reauth, for instance, only means something for a session, because
// an API-key holder (a script, dzctl, the MCP server) has no password to
// re-supply. Prefix-only on purpose: a malformed key still reads as "an API
// key" so it takes the key policy path and is rejected there by
// Authenticate, never silently routed down the session path.
func IsAPIKeyCredential(credential string) bool {
	return strings.HasPrefix(credential, apiKeyPrefix)
}

func (a *APIKeyAuthenticator) now() time.Time {
	if a.Clock != nil {
		return a.Clock()
	}
	return time.Now()
}

// IssueAPIKey generates a fresh key for the given identity, persists the
// hashed record in store, and returns both the record (for further
// metadata writes) and the cleartext credential — show that exactly once
// to the user, then forget it. expiresAt is optional: nil = never
// expires (operator-issued, long-lived); a non-nil value stamps the
// record so the authenticator rejects the key after that time.
func IssueAPIKey(store interface {
	PutKey(context.Context, APIKey) error
}, ctx context.Context, id, tenant, workspace, subject string, roles []core.Role, expiresAt *time.Time) (APIKey, string, error) {
	if err := validateKeyID(id); err != nil {
		return APIKey{}, "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return APIKey{}, "", fmt.Errorf("generate salt: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return APIKey{}, "", fmt.Errorf("generate secret: %w", err)
	}
	key := APIKey{
		ID:        id,
		Tenant:    tenant,
		Workspace: workspace,
		Subject:   subject,
		Roles:     roles,
		Salt:      salt,
		Hash:      sha256Salted(salt, secret),
		ExpiresAt: expiresAt,
	}
	if err := store.PutKey(ctx, key); err != nil {
		return APIKey{}, "", err
	}
	cleartext := fmt.Sprintf("%s%s_%s", apiKeyPrefix, id, hex.EncodeToString(secret))
	return key, cleartext, nil
}

// validateKeyID guards a caller-supplied key ID. The cleartext wire
// format is dzk_<id>_<secret>, and Authenticate recovers the id with
// strings.SplitN(rest, "_", 2) — so an id containing "_" would parse
// back as a different (id, secret) split and the key could never
// authenticate. Constrain to a charset that round-trips cleanly rather
// than mint a silently-broken key. Server-generated IDs ("k"+hex) pass.
func validateKeyID(id string) error {
	if id == "" {
		return fmt.Errorf("id required")
	}
	if len(id) > 64 {
		return fmt.Errorf("id %q too long (max 64)", id)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("id %q has invalid character %q (allowed: letters, digits, '-')", id, string(r))
		}
	}
	return nil
}

func sha256Salted(salt, secret []byte) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write(secret)
	return h.Sum(nil)
}

// MemKeyStore is an in-memory APIKeyStore for tests.
type MemKeyStore struct {
	mu   sync.RWMutex
	keys map[string]APIKey
}

func NewMemKeyStore() *MemKeyStore {
	return &MemKeyStore{keys: make(map[string]APIKey)}
}

func (m *MemKeyStore) PutKey(_ context.Context, k APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys[k.ID] = k
	return nil
}

func (m *MemKeyStore) GetKey(_ context.Context, id string) (APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.keys[id]
	if !ok {
		return APIKey{}, ErrInvalidCredential
	}
	return k, nil
}

func (m *MemKeyStore) Revoke(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[id]
	if !ok {
		return ErrInvalidCredential
	}
	k.RevokedAt = &at
	m.keys[id] = k
	return nil
}

// filterKeys returns every key matching pred, sorted by ID for
// deterministic output. Holds the read lock for the snapshot.
func (m *MemKeyStore) filterKeys(pred func(APIKey) bool) []APIKey {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]APIKey, 0)
	for _, k := range m.keys {
		if pred(k) {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// deleteKeys removes every key matching pred and returns the count.
// Holds the write lock.
func (m *MemKeyStore) deleteKeys(pred func(APIKey) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, k := range m.keys {
		if pred(k) {
			delete(m.keys, id)
			n++
		}
	}
	return n
}

// ListAll returns every key in the store. Used by platform admins to
// derive the tenant catalog (no separate tenants table exists today).
func (m *MemKeyStore) ListAll(_ context.Context) ([]APIKey, error) {
	return m.filterKeys(func(APIKey) bool { return true }), nil
}

// ListByTenant returns every key whose Tenant matches. Keys are
// returned with their hash + salt intact — callers that don't need
// those (the admin UI) should redact them on the way out. Sorted by ID
// for deterministic test output.
func (m *MemKeyStore) ListByTenant(_ context.Context, tenant string) ([]APIKey, error) {
	return m.filterKeys(func(k APIKey) bool { return k.Tenant == tenant }), nil
}

// ListBySubject returns every key issued to a subject (GDPR export).
func (m *MemKeyStore) ListBySubject(_ context.Context, subject string) ([]APIKey, error) {
	return m.filterKeys(func(k APIKey) bool { return k.Subject == subject }), nil
}

// DeleteBySubject hard-deletes every key for a subject (erasure).
func (m *MemKeyStore) DeleteBySubject(_ context.Context, subject string) (int, error) {
	return m.deleteKeys(func(k APIKey) bool { return k.Subject == subject }), nil
}

// DeleteByTenant hard-deletes every key in a tenant (org deletion).
func (m *MemKeyStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	return m.deleteKeys(func(k APIKey) bool { return k.Tenant == tenant }), nil
}
