// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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
	for _, n := range filterReservedSecretNames(names) {
		if n == vaultConfigSecretName {
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
