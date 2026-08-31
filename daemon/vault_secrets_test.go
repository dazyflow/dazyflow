// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// newTestSecrets builds an in-memory EncryptedSecrets for tests.
func newTestSecrets(t *testing.T) *EncryptedSecrets {
	t.Helper()
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("encrypted secrets: %v", err)
	}
	return es
}

// fakeVaultClient records readKV calls and returns canned fields per (tenant is
// implicit in cfg.Address here) path.
type fakeVaultClient struct {
	data  map[string]map[string]string // path -> fields
	calls int
	err   error
}

func (f *fakeVaultClient) readKV(_ context.Context, _ VaultConfig, path string) (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	fields, ok := f.data[path]
	if !ok {
		return nil, fmt.Errorf("manager returned 404: no secret at %q", path)
	}
	return fields, nil
}

func (f *fakeVaultClient) verify(_ context.Context, _ VaultConfig) error { return f.err }

func newTestVaultProvider(client vaultClient, configs map[string]VaultConfig, ttl time.Duration) *VaultProvider {
	return NewVaultProvider(client, func(_ context.Context, tenant string) (VaultConfig, bool, error) {
		cfg, ok := configs[tenant]
		return cfg, ok, nil
	}, ttl)
}

func acmeCfg() VaultConfig {
	return VaultConfig{Address: "https://vault.acme.test", Mount: "secret", Auth: VaultAuth{Method: "token", Token: "t"}}
}

func TestVaultProvider_ResolvesField(t *testing.T) {
	client := &fakeVaultClient{data: map[string]map[string]string{
		"stripe": {"api_key": "sk_live_123", "publishable": "pk_1"},
	}}
	p := newTestVaultProvider(client, map[string]VaultConfig{"acme": acmeCfg()}, time.Minute)

	ctx := core.WithTenant(context.Background(), "acme")
	got, err := p.Get(ctx, "stripe#api_key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "sk_live_123" {
		t.Errorf("value = %q, want sk_live_123", got)
	}
}

func TestVaultProvider_CachesWithinTTL(t *testing.T) {
	client := &fakeVaultClient{data: map[string]map[string]string{"db": {"password": "p"}}}
	p := newTestVaultProvider(client, map[string]VaultConfig{"acme": acmeCfg()}, time.Minute)
	ctx := core.WithTenant(context.Background(), "acme")

	for i := 0; i < 3; i++ {
		if _, err := p.Get(ctx, "db#password"); err != nil {
			t.Fatalf("Get: %v", err)
		}
	}
	if client.calls != 1 {
		t.Errorf("readKV called %d times, want 1 (cached)", client.calls)
	}
}

func TestVaultProvider_CacheExpires(t *testing.T) {
	fake := time.Unix(1_700_000_000, 0)
	nowFunc = func() time.Time { return fake }
	defer func() { nowFunc = time.Now }()

	client := &fakeVaultClient{data: map[string]map[string]string{"db": {"password": "p"}}}
	p := newTestVaultProvider(client, map[string]VaultConfig{"acme": acmeCfg()}, 30*time.Second)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := p.Get(ctx, "db#password"); err != nil {
		t.Fatal(err)
	}
	fake = fake.Add(31 * time.Second) // past the TTL
	if _, err := p.Get(ctx, "db#password"); err != nil {
		t.Fatal(err)
	}
	if client.calls != 2 {
		t.Errorf("readKV called %d times, want 2 (cache should have expired)", client.calls)
	}
}

func TestVaultProvider_TenantScoped(t *testing.T) {
	client := &fakeVaultClient{data: map[string]map[string]string{"x": {"v": "secret"}}}
	// Only acme has a config; globex doesn't.
	p := newTestVaultProvider(client, map[string]VaultConfig{"acme": acmeCfg()}, time.Minute)

	// No tenant in context → refused (BYO secrets are tenant-scoped).
	if _, err := p.Get(context.Background(), "x#v"); err == nil {
		t.Error("expected error with no tenant in context")
	}
	// A tenant without a configured manager → clear "not configured".
	if _, err := p.Get(core.WithTenant(context.Background(), "globex"), "x#v"); err == nil {
		t.Error("expected 'not configured' error for a tenant with no manager")
	}
	// The configured tenant resolves.
	if _, err := p.Get(core.WithTenant(context.Background(), "acme"), "x#v"); err != nil {
		t.Errorf("acme should resolve: %v", err)
	}
}

func TestVaultProvider_MissingFieldAndBadRef(t *testing.T) {
	client := &fakeVaultClient{data: map[string]map[string]string{"only": {"a": "1"}}}
	p := newTestVaultProvider(client, map[string]VaultConfig{"acme": acmeCfg()}, time.Minute)
	ctx := core.WithTenant(context.Background(), "acme")

	if _, err := p.Get(ctx, "only#missing"); err == nil {
		t.Error("expected error for a field the secret doesn't have")
	}
	for _, bad := range []string{"nohash", "#field", "path#"} {
		if _, _, err := splitVaultRef(bad); err == nil {
			t.Errorf("splitVaultRef(%q) should error", bad)
		}
	}
	path, field, err := splitVaultRef("kv/data/app#token")
	if err != nil || path != "kv/data/app" || field != "token" {
		t.Errorf("splitVaultRef = (%q,%q,%v)", path, field, err)
	}
}

// The per-tenant config round-trips through the encrypted store, stays
// tenant-scoped, and is hidden from the user-facing secret listing.
func TestVaultConfig_StorageRoundTrip(t *testing.T) {
	es := newTestSecrets(t)
	ctx := context.Background()
	cfg := VaultConfig{
		Address:   "https://vault.acme.internal:8200",
		Namespace: "team-a",
		Mount:     "secret",
		Auth:      VaultAuth{Method: "approle", RoleID: "r", SecretID: "s"},
	}
	if err := saveProviderConfig(ctx, es, "acme", vaultConfigSecretName, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok, err := loadProviderConfig[VaultConfig](ctx, es, "acme", vaultConfigSecretName)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got != cfg {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}

	// A different tenant has no config (not an error).
	if _, ok, err := loadProviderConfig[VaultConfig](ctx, es, "globex", vaultConfigSecretName); ok || err != nil {
		t.Errorf("globex: ok=%v err=%v, want false/nil", ok, err)
	}

	// Saving an invalid config is refused.
	if err := saveProviderConfig(ctx, es, "acme", vaultConfigSecretName, VaultConfig{Address: "https://x"}); err == nil {
		t.Error("invalid config should not save")
	}

	// The reserved config name is hidden from the user secret listing.
	names, err := es.List(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if !isReservedSecretName(n) && n == vaultConfigSecretName {
			t.Errorf("reserved config name leaked into the user listing")
		}
	}
}

func TestVaultConfig_Validate(t *testing.T) {
	ok := []VaultConfig{
		{Address: "https://v", Mount: "secret", Auth: VaultAuth{Method: "token", Token: "t"}},
		{Address: "http://v", Mount: "kv", Auth: VaultAuth{Method: "approle", RoleID: "r", SecretID: "s"}},
	}
	for i, c := range ok {
		if err := c.validate(); err != nil {
			t.Errorf("ok[%d] should validate: %v", i, err)
		}
	}
	bad := []VaultConfig{
		{Mount: "secret", Auth: VaultAuth{Method: "token", Token: "t"}},                          // no address
		{Address: "ftp://v", Mount: "secret", Auth: VaultAuth{Method: "token", Token: "t"}},      // bad scheme
		{Address: "https://v", Auth: VaultAuth{Method: "token", Token: "t"}},                     // no mount
		{Address: "https://v", Mount: "secret", Auth: VaultAuth{Method: "token"}},                // token method, no token
		{Address: "https://v", Mount: "secret", Auth: VaultAuth{Method: "approle", RoleID: "r"}}, // approle, no secret_id
		{Address: "https://v", Mount: "secret", Auth: VaultAuth{Method: "nope"}},                 // bad method
	}
	for i, c := range bad {
		if err := c.validate(); err == nil {
			t.Errorf("bad[%d] should fail validation: %+v", i, c)
		}
	}
}

// fakeVaultServer stands in for an OpenBao/Vault HTTP API so the real
// vaultAPIClient (OpenBao Go SDK) exercises its readKV/token/authedClient/
// appRoleLogin/verify paths over httptest, with no live server.
//
//   - POST auth/approle/login          -> {auth:{client_token, lease_duration}}
//   - GET  auth/token/lookup-self      -> {data:{}}  (token self-lookup verify)
//   - GET  {mount}/data/{path}         -> {data:{data:{...fields...}}}
type fakeVaultKVServer struct {
	srv         *httptest.Server
	loginCalls  int64
	lookupCalls int64
	secrets     map[string]map[string]any // path -> fields
	denyLogin   bool
}

func newFakeVaultKVServer(t *testing.T, secrets map[string]map[string]any) *fakeVaultKVServer {
	t.Helper()
	f := &fakeVaultKVServer{secrets: secrets}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/v1/")
		switch {
		case path == "auth/approle/login":
			atomic.AddInt64(&f.loginCalls, 1)
			if f.denyLogin {
				rw.WriteHeader(http.StatusForbidden)
				_, _ = rw.Write([]byte(`{"errors":["invalid role or secret id"]}`))
				return
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"auth": map[string]any{
					"client_token":   "s.approle-token",
					"lease_duration": 30,
				},
			})
		case path == "auth/token/lookup-self":
			atomic.AddInt64(&f.lookupCalls, 1)
			if r.Header.Get("X-Vault-Token") == "" {
				rw.WriteHeader(http.StatusForbidden)
				_, _ = rw.Write([]byte(`{"errors":["missing client token"]}`))
				return
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{"data": map[string]any{"id": "tok"}})
		case strings.HasPrefix(path, "secret/data/"):
			name := strings.TrimPrefix(path, "secret/data/")
			fields, ok := f.secrets[name]
			if !ok {
				rw.WriteHeader(http.StatusNotFound)
				_, _ = rw.Write([]byte(`{"errors":[]}`))
				return
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"data": map[string]any{"data": fields, "metadata": map[string]any{"version": 1}},
			})
		default:
			rw.WriteHeader(http.StatusNotFound)
			_, _ = rw.Write([]byte(`{"errors":["no such path"]}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// TestVaultAPIClient_ReadKV_TokenAuth drives the real vaultAPIClient against a
// fake server with static-token auth: readKV -> authedClient -> token (static)
// -> KVv2 Get, plus stringifyVaultValue on a non-string field.
func TestVaultAPIClient_ReadKV_TokenAuth(t *testing.T) {
	srv := newFakeVaultKVServer(t, map[string]map[string]any{
		"stripe": {"api_key": "sk_live", "rotations": 3, "tags": []string{"a", "b"}},
	})
	c := newVaultAPIClient(5 * time.Second)
	cfg := VaultConfig{Address: srv.srv.URL, Mount: "secret", Auth: VaultAuth{Method: "token", Token: "root"}}

	fields, err := c.readKV(context.Background(), cfg, "stripe")
	if err != nil {
		t.Fatalf("readKV: %v", err)
	}
	if fields["api_key"] != "sk_live" {
		t.Errorf("api_key = %q", fields["api_key"])
	}
	// Non-string values are JSON-encoded by stringifyVaultValue.
	if fields["rotations"] != "3" {
		t.Errorf("rotations = %q, want 3", fields["rotations"])
	}
	if fields["tags"] != `["a","b"]` {
		t.Errorf("tags = %q", fields["tags"])
	}

	// Missing secret surfaces an error.
	if _, err := c.readKV(context.Background(), cfg, "ghost"); err == nil {
		t.Error("expected error reading a missing secret")
	}
}

// TestVaultAPIClient_AppRoleLoginAndCache covers token()'s AppRole branch:
// appRoleLogin mints a token, caches it (second read doesn't re-login), and
// authedClient/readKV use it.
func TestVaultAPIClient_AppRoleLoginAndCache(t *testing.T) {
	srv := newFakeVaultKVServer(t, map[string]map[string]any{"db": {"password": "p"}})
	c := newVaultAPIClient(5 * time.Second)
	cfg := VaultConfig{
		Address: srv.srv.URL, Mount: "secret",
		Auth: VaultAuth{Method: "approle", RoleID: "r", SecretID: "s"},
	}

	for i := 0; i < 3; i++ {
		fields, err := c.readKV(context.Background(), cfg, "db")
		if err != nil {
			t.Fatalf("readKV[%d]: %v", i, err)
		}
		if fields["password"] != "p" {
			t.Errorf("password = %q", fields["password"])
		}
	}
	if n := atomic.LoadInt64(&srv.loginCalls); n != 1 {
		t.Errorf("approle logins = %d, want 1 (token cached)", n)
	}
}

// TestVaultAPIClient_AppRoleLoginRejected covers appRoleLogin's error path
// (server returns 403) propagated through token -> authedClient -> readKV.
func TestVaultAPIClient_AppRoleLoginRejected(t *testing.T) {
	srv := newFakeVaultKVServer(t, nil)
	srv.denyLogin = true
	c := newVaultAPIClient(5 * time.Second)
	cfg := VaultConfig{
		Address: srv.srv.URL, Mount: "secret",
		Auth: VaultAuth{Method: "approle", RoleID: "r", SecretID: "bad"},
	}
	if _, _, err := c.appRoleLogin(context.Background(), cfg); err == nil {
		t.Fatal("expected appRoleLogin to fail on 403")
	}
	if _, err := c.readKV(context.Background(), cfg, "db"); err == nil {
		t.Error("readKV should fail when login fails")
	}
}

// TestVaultAPIClient_Verify covers verify()'s two legs: a token self-lookup
// and an AppRole login, plus the bad-address (newClient/connect) failure.
func TestVaultAPIClient_Verify(t *testing.T) {
	srv := newFakeVaultKVServer(t, nil)
	c := newVaultAPIClient(5 * time.Second)

	tokenCfg := VaultConfig{Address: srv.srv.URL, Mount: "secret", Auth: VaultAuth{Method: "token", Token: "root"}}
	if err := c.verify(context.Background(), tokenCfg); err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if atomic.LoadInt64(&srv.lookupCalls) == 0 {
		t.Error("token verify did not perform a self-lookup")
	}

	approleCfg := VaultConfig{Address: srv.srv.URL, Mount: "secret", Auth: VaultAuth{Method: "approle", RoleID: "r", SecretID: "s"}}
	if err := c.verify(context.Background(), approleCfg); err != nil {
		t.Fatalf("verify approle: %v", err)
	}

	// An unreachable address fails verify.
	dead := VaultConfig{Address: "http://127.0.0.1:1", Mount: "secret", Auth: VaultAuth{Method: "token", Token: "x"}}
	if err := c.verify(context.Background(), dead); err == nil {
		t.Error("verify against a dead address should fail")
	}
}

// TestVerifyVaultConfig_EndToEnd exercises the exported save-endpoint helper
// against the fake server (validate + connect+auth), plus its validate-first
// short-circuit.
func TestVerifyVaultConfig_EndToEnd(t *testing.T) {
	srv := newFakeVaultKVServer(t, nil)
	good := VaultConfig{Address: srv.srv.URL, Mount: "secret", Auth: VaultAuth{Method: "approle", RoleID: "r", SecretID: "s"}}
	if err := VerifyVaultConfig(context.Background(), good, 5*time.Second); err != nil {
		t.Fatalf("verify good config: %v", err)
	}
	// Invalid config fails before any network call.
	if err := VerifyVaultConfig(context.Background(), VaultConfig{Address: "ftp://x", Mount: "secret", Auth: VaultAuth{Method: "token", Token: "t"}}, time.Second); err == nil {
		t.Error("invalid scheme should fail validation")
	}
}

// TestNewVaultProviderForStore_Wired covers the production constructor's
// loadConfig closure: a tenant with no stored config reads as not-configured.
func TestNewVaultProviderForStore_Wired(t *testing.T) {
	es := newTestSecrets(t)
	p := NewVaultProviderForStore(es, 2*time.Second)
	if p.Scheme() != "vault" {
		t.Fatalf("scheme = %q", p.Scheme())
	}
	_, err := p.Get(core.WithTenant(context.Background(), "acme"), "x#y")
	if err == nil || !strings.Contains(err.Error(), "no secret manager configured") {
		t.Errorf("unconfigured tenant err = %v, want not-configured", err)
	}
}
