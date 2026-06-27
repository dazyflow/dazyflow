// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Shared scaffolding for the BYO secret-manager providers (vault://,
// aws://, gcp://). Each provider keeps its genuinely different parts —
// auth protocol, API client, ref syntax — and shares everything that
// must NOT drift between them: the value cache, the encrypted
// per-tenant config storage, and the HTTP config endpoints' bodies.

// ttlCacheEntry is one cached value with its expiry.
type ttlCacheEntry[V any] struct {
	value V
	exp   time.Time
}

// ttlCache is a concurrency-safe map keyed by string with a per-entry
// TTL: entries expire on read (so the map doesn't accumulate dead keys)
// against the nowFunc clock seam tests drive. It backs both the BYO
// secret value cache and the billing plan cache — same map+mutex+
// expire-on-read shape — leaving each only its own write-through policy.
type ttlCache[V any] struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]ttlCacheEntry[V]
}

func newTTLCache[V any](ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, entries: map[string]ttlCacheEntry[V]{}}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	if !e.exp.After(nowFunc()) {
		delete(c.entries, key)
		var zero V
		return zero, false
	}
	return e.value, true
}

func (c *ttlCache[V]) put(key string, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = ttlCacheEntry[V]{value: val, exp: nowFunc().Add(c.ttl)}
}

// tenantSecretCache is the short-TTL value cache every BYO provider
// fronts its manager with, so a flow referencing a secret on every run
// doesn't hammer the upstream. Keys are provider-specific (tenant +
// ref). Backed by ttlCache.
type tenantSecretCache = ttlCache[string]

func newTenantSecretCache(ttl time.Duration) *tenantSecretCache {
	if ttl <= 0 {
		ttl = defaultVaultCacheTTL
	}
	return newTTLCache[string](ttl)
}

// resolveCachedSecret is the shared Get path every BYO provider runs:
// tenant-from-context → parse the ref → cache lookup → load the tenant's
// config → fetch from the backend → cache the value. Only parse and fetch
// differ per provider, so each Get supplies those two closures and shares
// everything that must NOT drift (the tenant requirement, the cache key
// discipline, the "not configured" handling). scheme labels the provider
// in the tenant/not-configured errors.
//
// parse returns the cache key (provider-specific: vault keys on
// tenant+path+field, the cloud providers on tenant+ref) and a parsed
// value handed to fetch. fetch must already wrap its own errors.
func resolveCachedSecret[P any, C any](
	ctx context.Context,
	scheme, ref string,
	cache *tenantSecretCache,
	parse func(tenant, ref string) (cacheKey string, parsed P, err error),
	load func(ctx context.Context, tenant string) (cfg C, ok bool, err error),
	fetch func(ctx context.Context, cfg C, parsed P) (string, error),
) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("%s://%s: no tenant in context — BYO secrets are tenant-scoped", scheme, ref)
	}
	key, parsed, err := parse(tenant, ref)
	if err != nil {
		return "", err
	}
	if v, ok := cache.get(key); ok {
		return v, nil
	}
	cfg, ok, err := load(ctx, tenant)
	if err != nil {
		return "", fmt.Errorf("%s: loading this tenant's secret-manager config: %w", scheme, err)
	}
	if !ok {
		return "", notConfiguredError(scheme, ref)
	}
	val, err := fetch(ctx, cfg, parsed)
	if err != nil {
		return "", err
	}
	cache.put(key, val)
	return val, nil
}

// notConfiguredError renders the per-provider "tenant has no X configured"
// message. The wording differs per provider (it names the actual product),
// so it can't fold into resolveCachedSecret's generic body.
func notConfiguredError(scheme, ref string) error {
	switch scheme {
	case "aws":
		return fmt.Errorf("aws://%s: this tenant has no AWS Secrets Manager configured", ref)
	case "gcp":
		return fmt.Errorf("gcp://%s: this tenant has no GCP Secret Manager configured", ref)
	default:
		return fmt.Errorf("vault://%s: this tenant has no secret manager configured", ref)
	}
}

// splitCloudSecretRef parses "NAME" / "NAME#field" for the aws/gcp providers.
// Unlike Vault (where a KV secret is always a field map), a cloud secret is
// often a single opaque string, so the field is optional.
func splitCloudSecretRef(ref string) (name, field string) {
	if i := strings.LastIndexByte(ref, '#'); i > 0 && i < len(ref)-1 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// pluckJSONField returns raw verbatim when field is empty; otherwise raw must
// be a JSON object and the named key is returned (strings pass through,
// anything else re-encodes as JSON — same policy as stringifyVaultValue).
func pluckJSONField(raw, field string) (string, error) {
	if field == "" {
		return raw, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", fmt.Errorf("value is not a JSON object, cannot pluck field %q", field)
	}
	v, ok := obj[field]
	if !ok {
		return "", fmt.Errorf("no field %q", field)
	}
	return stringifyVaultValue(v), nil
}

// getCloudSecret implements the aws/gcp SecretProvider.Get: parse a
// "NAME" / "NAME#field" ref, fetch NAME from the tenant's cloud secret
// manager (with caching via resolveCachedSecret), then pluck #field. The
// per-provider parts are the scheme label, the config loader, and fetch —
// the single client call that reads NAME's raw value.
func getCloudSecret[C any](
	ctx context.Context,
	scheme, ref string,
	cache *tenantSecretCache,
	load func(ctx context.Context, tenant string) (C, bool, error),
	fetch func(ctx context.Context, cfg C, name string) (string, error),
) (string, error) {
	type parsed struct{ name, field string }
	return resolveCachedSecret(ctx, scheme, ref, cache,
		func(tenant, ref string) (string, parsed, error) {
			name, field := splitCloudSecretRef(ref)
			if name == "" {
				return "", parsed{}, fmt.Errorf("%s reference %q must be NAME or NAME#field", scheme, ref)
			}
			return tenant + "\x00" + ref, parsed{name: name, field: field}, nil
		},
		load,
		func(ctx context.Context, cfg C, pr parsed) (string, error) {
			raw, err := fetch(ctx, cfg, pr.name)
			if err != nil {
				return "", fmt.Errorf("%s: reading %q: %w", scheme, pr.name, err)
			}
			val, err := pluckJSONField(raw, pr.field)
			if err != nil {
				return "", fmt.Errorf("%s: secret %q: %w", scheme, pr.name, err)
			}
			return val, nil
		})
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
	// organization:admin, not secret:write: configuring the org's BYO
	// secret-manager backend (its address/endpoint) is an infrastructure
	// action, and the PUT connection-tests that tenant-supplied address. An
	// editor must not be able to point the org at — or probe via — an arbitrary
	// host. The SSRF guard on the verify client is the second layer.
	if !h.secretManagerGate(rw, p, core.PermOrganizationAdmin) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	cfg, ok := decodeRequestJSON[T](rw, r)
	if !ok {
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

// getSecretManagerConfig is the shared GET body for all three providers:
// gate → load → render the redacted view. When no config is stored it
// returns toView's zero-value view (each provider's zero view is
// {Configured:false}). label names the provider in the load error.
func getSecretManagerConfig[T any](
	h *HTTPGateway, rw http.ResponseWriter, r *http.Request, p core.Principal,
	label, secretName string,
	toView func(cfg T, configured bool) any,
) {
	if !h.secretManagerGate(rw, p, core.PermSecretRead) {
		return
	}
	cfg, ok, err := loadProviderConfig[T](r.Context(), h.EncryptedSecrets, p.Tenant, secretName)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("load %s config: %v", label, err))
		return
	}
	if !ok {
		var zero T
		writeJSON(rw, http.StatusOK, toView(zero, false))
		return
	}
	writeJSON(rw, http.StatusOK, toView(cfg, true))
}

// deleteSecretManagerConfig is the shared DELETE body: gate → delete →
// audit → 204. Deleting an absent config is a no-op success.
func deleteSecretManagerConfig(
	h *HTTPGateway, rw http.ResponseWriter, r *http.Request, p core.Principal,
	label, secretName, auditAction string,
) {
	// organization:admin, not secret:write — see putSecretManagerConfig:
	// removing the org's secret-manager backend is an infrastructure action.
	if !h.secretManagerGate(rw, p, core.PermOrganizationAdmin) {
		return
	}
	if err := deleteProviderConfig(r.Context(), h.EncryptedSecrets, p.Tenant, secretName); err != nil && !errors.Is(err, ErrSecretNotFound) {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete %s config: %v", label, err))
		return
	}
	h.audit(r.Context(), p, auditAction, "", "")
	rw.WriteHeader(http.StatusNoContent)
}
