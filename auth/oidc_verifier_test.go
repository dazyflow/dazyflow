// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeIdP is a minimal OIDC issuer: a discovery document, a JWKS with
// one RSA key, and a token mint that signs RS256 JWTs with it. Real
// crypto end to end — the verifier under test fetches the JWKS over
// HTTP and checks real signatures.
type fakeIdP struct {
	srv  *httptest.Server
	priv *rsa.PrivateKey
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f := &fakeIdP{priv: priv}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(rw http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"jwks_uri":                              f.srv.URL + "/jwks",
			"authorization_endpoint":                f.srv.URL + "/auth",
			"token_endpoint":                        f.srv.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(rw http.ResponseWriter, _ *http.Request) {
		pub := priv.Public().(*rsa.PublicKey)
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test-key",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// mint signs an RS256 JWT with the IdP's key. extra overlays/overrides
// the standard claims, so tests can vary iss/aud/exp/roles freely.
func (f *fakeIdP) mint(t *testing.T, extra map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": f.srv.URL,
		"sub": "user@corp.example",
		"aud": "dazyflow-api",
		"iat": time.Now().Add(-time.Minute).Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	b64 := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := b64(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test-key"}) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestOIDCVerifier(t *testing.T, idp *fakeIdP, cfg OIDCConfig) IDTokenVerifier {
	t.Helper()
	cfg.Issuer = idp.srv.URL
	if cfg.ClientID == "" && cfg.Audience == "" {
		cfg.ClientID = "dazyflow-api"
	}
	v, err := NewOIDCVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewOIDCVerifier: %v", err)
	}
	return v
}

func TestOIDCVerifier_ValidToken(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestOIDCVerifier(t, idp, OIDCConfig{})

	claims, err := v.Verify(context.Background(), idp.mint(t, map[string]any{
		"tenant": "acme", "roles": []string{"editor", "release-managers"},
	}))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "user@corp.example" || claims.Tenant != "acme" {
		t.Errorf("claims = %+v", claims)
	}
	if len(claims.Roles) != 2 || claims.Roles[0] != "editor" {
		t.Errorf("roles = %v", claims.Roles)
	}
}

func TestOIDCVerifier_Rejections(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestOIDCVerifier(t, idp, OIDCConfig{})

	// A second issuer with a DIFFERENT key — its tokens must not verify
	// against the first issuer's JWKS, and its iss claim is wrong too.
	rogue := newFakeIdP(t)

	cases := map[string]string{
		"expired":        idp.mint(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"wrong audience": idp.mint(t, map[string]any{"aud": "someone-else"}),
		"wrong issuer":   idp.mint(t, map[string]any{"iss": "https://evil.example"}),
		"foreign key":    rogue.mint(t, map[string]any{"iss": idp.srv.URL}),
		"garbage":        "aaa.bbb.ccc",
	}
	for name, token := range cases {
		if _, err := v.Verify(context.Background(), token); err == nil {
			t.Errorf("%s: token verified, want rejection", name)
		}
	}
}

func TestOIDCVerifier_ClaimShapes(t *testing.T) {
	idp := newFakeIdP(t)

	// Entra-style: tenant in "tid", roles as a space-separated string.
	v := newTestOIDCVerifier(t, idp, OIDCConfig{TenantClaim: "tid", RolesClaim: "scp"})
	claims, err := v.Verify(context.Background(), idp.mint(t, map[string]any{
		"tid": "11111111-2222", "scp": "viewer editor",
	}))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Tenant != "11111111-2222" {
		t.Errorf("tenant = %q", claims.Tenant)
	}
	if len(claims.Roles) != 2 || claims.Roles[1] != "editor" {
		t.Errorf("roles = %v", claims.Roles)
	}

	// Missing optional claims: empty tenant/roles, not an error.
	claims, err = v.Verify(context.Background(), idp.mint(t, nil))
	if err != nil || claims.Tenant != "" || len(claims.Roles) != 0 {
		t.Errorf("bare token = %+v / %v", claims, err)
	}
}

// End to end through the Chain: an IdP token authenticates a principal
// whose catalog role names carry catalog permissions, and unknown IdP
// groups grant nothing.
func TestOIDCAuthenticator_ThroughChain(t *testing.T) {
	idp := newFakeIdP(t)
	v := newTestOIDCVerifier(t, idp, OIDCConfig{})
	chain := Chain{
		&APIKeyAuthenticator{Store: NewMemKeyStore()},
		&OIDCAuthenticator{Config: OIDCConfig{}, Verifier: v},
	}

	token := idp.mint(t, map[string]any{
		"tenant": "acme", "roles": []string{"editor", "random-idp-group"},
	})
	p, err := chain.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.Subject != "user@corp.example" || p.Tenant != "acme" {
		t.Errorf("principal = %+v", p)
	}
	var editor, unknown *core.Role
	for i := range p.Roles {
		switch p.Roles[i].Name {
		case "editor":
			editor = &p.Roles[i]
		case "random-idp-group":
			unknown = &p.Roles[i]
		}
	}
	if editor == nil || !editor.Has(core.PermGraphEdit) || !editor.Has(core.PermSecretWrite) {
		t.Errorf("editor role should carry the catalog permissions: %+v", p.Roles)
	}
	if editor != nil && editor.Has(core.PermOrganizationAdmin) {
		t.Errorf("editor must not be org admin: %+v", editor)
	}
	if unknown == nil || len(unknown.Permissions) != 0 {
		t.Errorf("unknown IdP group must grant nothing: %+v", unknown)
	}

	// Non-JWT credentials fall through the OIDC authenticator to the
	// chain's normal invalid-credential error (not an OIDC error).
	if _, err := chain.Authenticate(context.Background(), "dzs_not_a_jwt"); err == nil {
		t.Error("non-JWT credential authenticated")
	}
}

func TestOIDCVerifier_AllowedTenants(t *testing.T) {
	idp := newFakeIdP(t)

	// Unset allowlist: any tenant the issuer asserts is honored (unchanged).
	v := newTestOIDCVerifier(t, idp, OIDCConfig{})
	if c, err := v.Verify(context.Background(), idp.mint(t, map[string]any{"tenant": "anything"})); err != nil || c.Tenant != "anything" {
		t.Errorf("unset allowlist should accept any tenant: %+v / %v", c, err)
	}

	// Configured allowlist: an in-list tenant passes, an out-of-list one
	// is rejected even though the token is otherwise perfectly valid.
	v = newTestOIDCVerifier(t, idp, OIDCConfig{AllowedTenants: []string{"acme", "globex"}})
	if c, err := v.Verify(context.Background(), idp.mint(t, map[string]any{"tenant": "acme"})); err != nil || c.Tenant != "acme" {
		t.Errorf("in-list tenant should verify: %+v / %v", c, err)
	}
	if _, err := v.Verify(context.Background(), idp.mint(t, map[string]any{"tenant": "evilcorp"})); err == nil {
		t.Error("out-of-list tenant must be rejected even with a valid signature")
	}
	// An empty/absent tenant claim is also outside a configured allowlist.
	if _, err := v.Verify(context.Background(), idp.mint(t, nil)); err == nil {
		t.Error("absent tenant claim must be rejected when an allowlist is configured")
	}
}

func TestNewOIDCVerifier_ConfigErrors(t *testing.T) {
	if _, err := NewOIDCVerifier(context.Background(), OIDCConfig{}); err == nil ||
		!strings.Contains(err.Error(), "issuer") {
		t.Errorf("missing issuer: %v", err)
	}
	if _, err := NewOIDCVerifier(context.Background(), OIDCConfig{Issuer: "https://x.example"}); err == nil ||
		!strings.Contains(err.Error(), "audience") {
		t.Errorf("missing audience: %v", err)
	}
	// Unreachable issuer fails discovery loudly.
	if _, err := NewOIDCVerifier(context.Background(), OIDCConfig{
		Issuer: "http://127.0.0.1:1", ClientID: "x",
	}); err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Errorf("unreachable issuer: %v", err)
	}
}
