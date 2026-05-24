package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func TestAPIKey_RoundTrip(t *testing.T) {
	store := NewMemKeyStore()
	roles := []core.Role{{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}}

	_, cleartext, err := IssueAPIKey(store, t.Context(), "k1", "acme", "ws1", "ci-bot", roles)
	if err != nil {
		t.Fatalf("IssueAPIKey: %v", err)
	}
	if !strings.HasPrefix(cleartext, apiKeyPrefix+"k1_") {
		t.Errorf("cleartext = %q", cleartext)
	}

	auth := &APIKeyAuthenticator{Store: store}
	p, err := auth.Authenticate(t.Context(), cleartext)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Tenant != "acme" || p.Subject != "ci-bot" {
		t.Errorf("principal = %+v", p)
	}
	if !p.Has(core.PermGraphRun) {
		t.Errorf("expected graph:run permission")
	}
}

func TestAPIKey_RejectsTampered(t *testing.T) {
	store := NewMemKeyStore()
	_, cleartext, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil)

	// Flip a hex char in the secret portion.
	tampered := cleartext[:len(cleartext)-1] + flipHex(cleartext[len(cleartext)-1])
	auth := &APIKeyAuthenticator{Store: store}
	if _, err := auth.Authenticate(t.Context(), tampered); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("tampered key accepted: %v", err)
	}
}

func TestAPIKey_RejectsRevoked(t *testing.T) {
	store := NewMemKeyStore()
	_, cleartext, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil)
	_ = store.Revoke(t.Context(), "k1", time.Now())

	auth := &APIKeyAuthenticator{Store: store}
	if _, err := auth.Authenticate(t.Context(), cleartext); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("revoked key accepted: %v", err)
	}
}

func TestAPIKey_RejectsExpired(t *testing.T) {
	store := NewMemKeyStore()
	past := time.Now().Add(-time.Hour)
	k, ct, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil)
	k.ExpiresAt = &past
	_ = store.PutKey(t.Context(), k)

	auth := &APIKeyAuthenticator{Store: store}
	if _, err := auth.Authenticate(t.Context(), ct); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("expired key accepted: %v", err)
	}
}

func TestBearerFromHeader(t *testing.T) {
	cases := map[string]struct {
		header string
		want   string
		err    bool
	}{
		"ok":            {"Bearer abc", "abc", false},
		"trim":          {"Bearer  spaces  ", "spaces", false},
		"wrong scheme":  {"Basic abc", "", true},
		"empty":         {"", "", true},
		"only scheme":   {"Bearer ", "", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := BearerFromHeader(tc.header)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChain_FallsThrough(t *testing.T) {
	store := NewMemKeyStore()
	_, cleartext, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil)

	chain := Chain{
		alwaysReject{},
		&APIKeyAuthenticator{Store: store},
	}
	if _, err := chain.Authenticate(t.Context(), cleartext); err != nil {
		t.Errorf("chain should have succeeded via api-key auth: %v", err)
	}
}

type alwaysReject struct{}

func (alwaysReject) Authenticate(_ context.Context, _ string) (core.Principal, error) {
	return core.Principal{}, ErrInvalidCredential
}

func TestOIDC_AuthenticateWithFakeVerifier(t *testing.T) {
	auth := &OIDCAuthenticator{
		Config: OIDCConfig{Issuer: "https://idp"},
		Verifier: stubVerifier{claims: Claims{
			Subject: "user@example.com",
			Tenant:  "acme",
			Roles:   []string{"editor"},
		}},
	}
	p, err := auth.Authenticate(t.Context(), "aaa.bbb.ccc")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Tenant != "acme" {
		t.Errorf("tenant = %q", p.Tenant)
	}
	if !p.Has(core.PermGraphEdit) {
		t.Errorf("editor should have graph:edit")
	}
}

type stubVerifier struct{ claims Claims }

func (s stubVerifier) Verify(_ context.Context, _ string) (Claims, error) {
	return s.claims, nil
}

func flipHex(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}
