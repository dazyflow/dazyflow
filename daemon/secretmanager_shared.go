package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Shared scaffolding for the BYO secret-manager providers (vault://,
// aws://, gcp://). Each provider keeps its genuinely different parts —
// auth protocol, API client, ref syntax — and shares everything that
// must NOT drift between them: the value cache, the encrypted
// per-tenant config storage, and the HTTP config endpoints' bodies.

// secretCacheEntry is one cached resolved value.
type secretCacheEntry struct {
	value string
	exp   time.Time
}

// tenantSecretCache is the short-TTL value cache every BYO provider
// fronts its manager with, so a flow referencing a secret on every run
// doesn't hammer the upstream. Keys are provider-specific (tenant +
// ref); expired entries are dropped on read so the map doesn't
// accumulate dead keys. Uses nowFunc as its clock seam (tests).
type tenantSecretCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]secretCacheEntry
}

func newTenantSecretCache(ttl time.Duration) *tenantSecretCache {
	if ttl <= 0 {
		ttl = defaultVaultCacheTTL
	}
	return &tenantSecretCache{ttl: ttl, entries: map[string]secretCacheEntry{}}
}

func (c *tenantSecretCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if !e.exp.After(nowFunc()) {
		delete(c.entries, key)
		return "", false
	}
	return e.value, true
}

func (c *tenantSecretCache) put(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = secretCacheEntry{value: val, exp: nowFunc().Add(c.ttl)}
}

// providerConfig is what every per-tenant connection config can do:
// validate itself before it's persisted or used.
type providerConfig interface {
	validate() error
}

// saveProviderConfig validates and persists one provider's per-tenant
// config under its reserved "cfg:" key (encrypted, in the tenant's own
// store).
func saveProviderConfig[T providerConfig](ctx context.Context, es *EncryptedSecrets, tenant, secretName string, cfg T) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return es.Put(ctx, tenant, secretName, string(b))
}

// loadProviderConfig returns a tenant's config; ok=false when none is
// set (a normal state, not an error).
func loadProviderConfig[T any](ctx context.Context, es *EncryptedSecrets, tenant, secretName string) (T, bool, error) {
	var cfg T
	raw, err := es.Get(core.WithTenant(ctx, tenant), secretName)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return cfg, false, nil
		}
		return cfg, false, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, false, fmt.Errorf("decode secret-manager config: %w", err)
	}
	return cfg, true, nil
}

func deleteProviderConfig(ctx context.Context, es *EncryptedSecrets, tenant, secretName string) error {
	return es.Delete(ctx, tenant, secretName)
}

// putSecretManagerConfig is the shared PUT body for all three providers:
// gate → decode → validate → connection-test → save → audit → 204.
// label names the provider in error messages ("AWS Secrets Manager");
// audit returns the action plus the credential-free target/detail pair.
func putSecretManagerConfig[T providerConfig](
	h *HTTPGateway, rw http.ResponseWriter, r *http.Request, p core.Principal,
	label, secretName string,
	verify func(context.Context, T, time.Duration) error,
	audit func(T) (action, target, detail string),
) {
	if !h.secretManagerGate(rw, p, core.PermSecretWrite) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var cfg T
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if err := cfg.validate(); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Connection-test before persisting, so a bad address/credential
	// fails here rather than silently breaking every flow that
	// references one of this provider's secrets.
	ctx, cancel := context.WithTimeout(r.Context(), vaultVerifyTimeout)
	defer cancel()
	if err := verify(ctx, cfg, vaultVerifyTimeout); err != nil {
		writeJSONError(rw, http.StatusBadGateway, fmt.Sprintf("could not reach %s: %v", label, err))
		return
	}
	if err := saveProviderConfig(r.Context(), h.EncryptedSecrets, p.Tenant, secretName, cfg); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save %s config: %v", label, err))
		return
	}
	action, target, detail := audit(cfg)
	h.audit(r.Context(), p, action, target, detail)
	rw.WriteHeader(http.StatusNoContent)
}

// deleteSecretManagerConfig is the shared DELETE body: gate → delete →
// audit → 204. Deleting an absent config is a no-op success.
func deleteSecretManagerConfig(
	h *HTTPGateway, rw http.ResponseWriter, r *http.Request, p core.Principal,
	label, secretName, auditAction string,
) {
	if !h.secretManagerGate(rw, p, core.PermSecretWrite) {
		return
	}
	if err := deleteProviderConfig(r.Context(), h.EncryptedSecrets, p.Tenant, secretName); err != nil && !errors.Is(err, ErrSecretNotFound) {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete %s config: %v", label, err))
		return
	}
	h.audit(r.Context(), p, auditAction, "", "")
	rw.WriteHeader(http.StatusNoContent)
}
