// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// newAdminOAuthHarness builds a gateway with EncryptedSecrets + an
// OAuthRegistry that already knows about every default provider but
// has no credentials set — the realistic "fresh install" starting
// point the admin endpoint serves.
func newAdminOAuthHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	h := newGatewayHarness(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 11)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	h.gw.EncryptedSecrets = es
	h.gw.OAuth = NewOAuthRegistry("https://example.test", es)
	return h
}

// ---- Persistence layer ----------------------------------------------

func TestProviderStore_RoundTrip(t *testing.T) {
	t.Parallel()
	es := newMemSecrets(t)
	ctx := t.Context()
	if c, err := loadProviderCreds(ctx, es, "google"); err != nil || c != nil {
		t.Fatalf("load on empty store should return (nil, nil); got (%+v, %v)", c, err)
	}
	in := providerCreds{ClientID: "abc.apps.googleusercontent.com", ClientSecret: "GOCSPX-secret"}
	if err := saveProviderCreds(ctx, es, "google", in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadProviderCreds(ctx, es, "google")
	if err != nil || got == nil {
		t.Fatalf("load after save: (%+v, %v)", got, err)
	}
	if got.ClientID != in.ClientID || got.ClientSecret != in.ClientSecret {
		t.Errorf("round-trip lost data: %+v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Errorf("UpdatedAt should auto-populate on save")
	}
}

func TestProviderStore_ListConfigured(t *testing.T) {
	t.Parallel()
	es := newMemSecrets(t)
	ctx := t.Context()
	_ = saveProviderCreds(ctx, es, "google", providerCreds{ClientID: "g", ClientSecret: "gs"})
	_ = saveProviderCreds(ctx, es, "slack", providerCreds{ClientID: "s", ClientSecret: "ss"})
	got, err := listConfiguredProviders(ctx, es)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Sorted output — deterministic boot order.
	want := []string{"google", "slack"}
	if !equalStrings(got, want) {
		t.Errorf("listConfiguredProviders = %v, want %v", got, want)
	}
}

func TestProviderStore_Delete(t *testing.T) {
	t.Parallel()
	es := newMemSecrets(t)
	ctx := t.Context()
	_ = saveProviderCreds(ctx, es, "google", providerCreds{ClientID: "g", ClientSecret: "gs"})
	if err := deleteProviderCreds(ctx, es, "google"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := loadProviderCreds(ctx, es, "google")
	if err != nil || got != nil {
		t.Errorf("after delete: (%+v, %v) — want (nil, nil)", got, err)
	}
}

// ---- Hydrate ---------------------------------------------------------

func TestHydrate_PersistedOverridesEnv(t *testing.T) {
	t.Parallel()
	// Simulate: env wired one client_id; admin pasted a different
	// one via the UI. After Hydrate, the registry holds the
	// admin-pasted value.
	es := newMemSecrets(t)
	r := NewOAuthRegistry("https://example.test", es)
	// "env-supplied"
	r.Register(providerDefault("google").toProvider("env-client", "env-secret"))
	// "admin pasted"
	_ = saveProviderCreds(t.Context(), es, "google", providerCreds{
		ClientID: "ui-client", ClientSecret: "ui-secret",
	})

	hydrated, errs := HydrateOAuthProvidersFromStore(t.Context(), r, es)
	if len(errs) != 0 {
		t.Fatalf("hydrate errs: %v", errs)
	}
	if !contains(hydrated, "google") {
		t.Fatalf("hydrate didn't report google: %v", hydrated)
	}
	got, _ := r.Provider("google")
	if got.ClientID != "ui-client" || got.ClientSecret != "ui-secret" {
		t.Errorf("registry not overridden: %+v", got)
	}
}

func TestHydrate_UnknownProviderSkipped(t *testing.T) {
	t.Parallel()
	// A persisted entry for a provider we no longer recognise must
	// be skipped with an error, not break the whole hydrate pass.
	es := newMemSecrets(t)
	r := NewOAuthRegistry("https://example.test", es)
	_ = saveProviderCreds(t.Context(), es, "discord", providerCreds{ClientID: "d", ClientSecret: "ds"})
	_ = saveProviderCreds(t.Context(), es, "google", providerCreds{ClientID: "g", ClientSecret: "gs"})

	hydrated, errs := HydrateOAuthProvidersFromStore(t.Context(), r, es)
	if !contains(hydrated, "google") {
		t.Errorf("google should be hydrated even when discord errors")
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "discord") {
		t.Errorf("expected one error mentioning discord; got %v", errs)
	}
}

// ---- Endpoints -------------------------------------------------------

// OAuth provider credentials are an instance-wide setting shared by
// every tenant, so only a platform admin may read or change them. An
// editor and even a tenant admin (which every signup gets for its own
// org) must be rejected — otherwise any customer could break or hijack
// the shared OAuth apps for all the other orgs on the instance.
func TestAdminOAuth_ProviderConfigRequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t)

	// Editor (no admin at all) — rejected.
	if rw := h.do(t, "GET", "/api/v1/admin/oauth-providers", nil); rw.Code != 403 {
		t.Errorf("editor GET should be 403; got %d body=%s", rw.Code, rw.Body.String())
	}
	// Tenant admin (org owner) — also rejected now: this is instance-wide config.
	if rw := h.adminDo(t, "GET", "/api/v1/admin/oauth-providers", nil); rw.Code != 403 {
		t.Errorf("tenant admin GET should be 403 (instance-wide config); got %d body=%s", rw.Code, rw.Body.String())
	}
	if rw := h.adminDo(t, "PUT", "/api/v1/admin/oauth-providers/google", map[string]any{
		"client_id": "x", "client_secret": "y",
	}); rw.Code != 403 {
		t.Errorf("tenant admin PUT should be 403; got %d body=%s", rw.Code, rw.Body.String())
	}
	if rw := h.adminDo(t, "DELETE", "/api/v1/admin/oauth-providers/google", nil); rw.Code != 403 {
		t.Errorf("tenant admin DELETE should be 403; got %d body=%s", rw.Code, rw.Body.String())
	}
	// Platform admin (the operator) — allowed.
	if rw := h.platformDo(t, "GET", "/api/v1/admin/oauth-providers", nil); rw.Code != 200 {
		t.Errorf("platform admin GET should be 200; got %d body=%s", rw.Code, rw.Body.String())
	}
}

func TestAdminOAuth_ListReturnsAllDefaults(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t)
	rw := h.platformDo(t, "GET", "/api/v1/admin/oauth-providers", nil)
	if rw.Code != 200 {
		t.Fatalf("code = %d, body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Providers []adminProviderRow `json:"providers"`
	}
	if err := json.NewDecoder(rw.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Providers) != len(KnownOAuthProviderDefaults) {
		t.Errorf("got %d providers, want %d", len(out.Providers), len(KnownOAuthProviderDefaults))
	}
	for _, p := range out.Providers {
		if p.Configured {
			t.Errorf("fresh install should have %q unconfigured", p.Name)
		}
		if p.SetupHelp == "" {
			t.Errorf("%q missing setup help", p.Name)
		}
		if !strings.Contains(p.RedirectURI, "/api/v1/oauth/"+p.Name+"/callback") {
			t.Errorf("%q redirect_uri = %q", p.Name, p.RedirectURI)
		}
	}
}

func TestAdminOAuth_UpsertRegistersLiveAndPersists(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t)
	rw := h.platformDo(t, "PUT", "/api/v1/admin/oauth-providers/google", map[string]any{
		"client_id":     "555.apps.googleusercontent.com",
		"client_secret": "GOCSPX-pasted-in-ui",
	})
	if rw.Code != 200 {
		t.Fatalf("code = %d body=%s", rw.Code, rw.Body.String())
	}
	// In-memory registry sees it immediately.
	if _, ok := h.gw.OAuth.Provider("google"); !ok {
		t.Fatalf("registry should have google after PUT")
	}
	// Persisted on disk too — survives "restart" (hydrate).
	c, err := loadProviderCreds(t.Context(), h.gw.EncryptedSecrets, "google")
	if err != nil || c == nil {
		t.Fatalf("load after PUT: (%+v, %v)", c, err)
	}
	if c.ClientID != "555.apps.googleusercontent.com" {
		t.Errorf("persisted client_id = %q", c.ClientID)
	}
}

func TestAdminOAuth_UpsertRejectsEmptyCreds(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t)
	rw := h.platformDo(t, "PUT", "/api/v1/admin/oauth-providers/google", map[string]any{
		"client_id":     "abc",
		"client_secret": "",
	})
	if rw.Code != 400 {
		t.Errorf("empty secret should 400; got %d", rw.Code)
	}
}

func TestAdminOAuth_UpsertUnknownProvider(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t)
	rw := h.platformDo(t, "PUT", "/api/v1/admin/oauth-providers/discord", map[string]any{
		"client_id":     "abc",
		"client_secret": "xyz",
	})
	if rw.Code != 404 {
		t.Errorf("unknown provider should 404; got %d body=%s", rw.Code, rw.Body.String())
	}
}

func TestAdminOAuth_DeleteUnregistersAndClearsStore(t *testing.T) {
	t.Parallel()
	h := newAdminOAuthHarness(t)
	// Set first.
	_ = h.platformDo(t, "PUT", "/api/v1/admin/oauth-providers/google", map[string]any{
		"client_id": "abc", "client_secret": "xyz",
	})
	rw := h.platformDo(t, "DELETE", "/api/v1/admin/oauth-providers/google", nil)
	if rw.Code != 204 {
		t.Fatalf("delete code = %d body=%s", rw.Code, rw.Body.String())
	}
	if _, ok := h.gw.OAuth.Provider("google"); ok {
		t.Errorf("registry should NOT have google after DELETE")
	}
	if c, _ := loadProviderCreds(t.Context(), h.gw.EncryptedSecrets, "google"); c != nil {
		t.Errorf("persisted creds should be gone after DELETE; got %+v", c)
	}
}

// ---- scope_mismatch on user-facing listing -------------------------

func TestStaleAccounts_MissingScopeFlagged(t *testing.T) {
	t.Parallel()
	// Token was granted only gmail.send, but the (explicit) required set
	// also needs drive.readonly. The account should be flagged stale.
	// Required scopes are passed in rather than read from the default so
	// this test stays valid regardless of the default scope set.
	es := newMemSecrets(t)
	r := NewOAuthRegistry("https://example.test", es)
	r.Register(providerDefault("google").toProvider("c", "s"))
	tok := &StoredOAuthToken{
		AccessToken: "ya29.test",
		Scope:       "https://www.googleapis.com/auth/gmail.send",
		ObtainedAt:  time.Now().UTC(),
	}
	if _, err := r.store(t.Context(), "tenantA", "google", "default", tok); err != nil {
		t.Fatalf("store: %v", err)
	}
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = es
	h.gw.OAuth = r
	required := []string{
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/drive.readonly", // granted token lacks this
	}
	stale := h.gw.oauthAPI().staleAccounts(context.Background(), "tenantA", "google", []string{"default"}, required)
	if !contains(stale, "default") {
		t.Errorf("expected default to be stale; got %v", stale)
	}
}

func TestStaleAccounts_FullScopeNotFlagged(t *testing.T) {
	t.Parallel()
	es := newMemSecrets(t)
	r := NewOAuthRegistry("https://example.test", es)
	r.Register(providerDefault("google").toProvider("c", "s"))
	full := strings.Join(providerDefault("google").Scopes, " ")
	tok := &StoredOAuthToken{AccessToken: "ya29", Scope: full, ObtainedAt: time.Now().UTC()}
	_, _ = r.store(t.Context(), "tenantA", "google", "default", tok)
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = es
	h.gw.OAuth = r
	stale := h.gw.oauthAPI().staleAccounts(context.Background(), "tenantA", "google", []string{"default"}, providerDefault("google").Scopes)
	if len(stale) != 0 {
		t.Errorf("fully-scoped token should not be stale; got %v", stale)
	}
}

func TestSplitScopes_HandlesSpacesAndCommas(t *testing.T) {
	t.Parallel()
	got := splitScopes("chat:write, channels:read  channels:history")
	for _, want := range []string{"chat:write", "channels:read", "channels:history"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

// ---- helpers --------------------------------------------------------

func newMemSecrets(t *testing.T) *EncryptedSecrets {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 23)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	return es
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
