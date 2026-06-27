// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

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
