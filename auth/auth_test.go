package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// TestSession_StoredKeyIsHashedNotToken locks in that a session is stored
// under the SHA-256 of its token, never the token itself — so a leak of the
// session store can't be replayed as a live bearer credential. The cleartext
// token still authenticates end-to-end.
func TestSession_StoredKeyIsHashedNotToken(t *testing.T) {
	store := NewMemSessionStore()
	user := User{Subject: "u", Tenant: "t", Workspace: "ws"}
	sess, token, err := IssueSession(context.Background(), store, user, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.HasPrefix(token, SessionTokenPrefix) {
		t.Fatalf("token missing prefix: %q", token)
	}
	// The store key must be the hash, not the token.
	if sess.ID == token {
		t.Fatal("session stored under the raw token; a store leak would hand out live credentials")
	}
	if sess.ID != SessionLookupKey(token) {
		t.Errorf("sess.ID = %q, want SessionLookupKey(token)", sess.ID)
	}
	// Looking the store up by the raw token must miss; only the hash hits.
	if _, err := store.GetSession(context.Background(), token); err == nil {
		t.Error("GetSession(rawToken) succeeded; the raw token must never be a store key")
	}
	if _, err := store.GetSession(context.Background(), SessionLookupKey(token)); err != nil {
		t.Errorf("GetSession(hash) = %v, want a hit", err)
	}
	// End-to-end: the authenticator still accepts the cleartext token.
	a := &SessionAuthenticator{Store: store}
	p, err := a.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if p.Subject != "u" || p.Tenant != "t" {
		t.Errorf("principal = %+v, want subject=u tenant=t", p)
	}
}

func TestAPIKey_RoundTrip(t *testing.T) {
	store := NewMemKeyStore()
	roles := []core.Role{{Name: "runner", Permissions: []core.Permission{core.PermGraphRun}}}

	_, cleartext, err := IssueAPIKey(store, t.Context(), "k1", "acme", "ws1", "ci-bot", roles, nil)
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

func TestIssueAPIKey_RejectsUnsafeID(t *testing.T) {
	store := NewMemKeyStore()
	// An "_" in the id would split wrong in the hzk_<id>_<secret> wire
	// format and mint a key that can never authenticate — reject at issue.
	for _, id := range []string{"", "team_key", "has space", "emoji😀"} {
		if _, _, err := IssueAPIKey(store, t.Context(), id, "t", "", "u", nil, nil); err == nil {
			t.Errorf("IssueAPIKey(id=%q) = nil error, want rejection", id)
		}
	}
	// A clean id with a hyphen round-trips and authenticates.
	_, cleartext, err := IssueAPIKey(store, t.Context(), "ci-bot-1", "t", "", "u", nil, nil)
	if err != nil {
		t.Fatalf("IssueAPIKey(clean id): %v", err)
	}
	if _, err := (&APIKeyAuthenticator{Store: store}).Authenticate(t.Context(), cleartext); err != nil {
		t.Fatalf("Authenticate clean id: %v", err)
	}
}

func TestAPIKey_RejectsTampered(t *testing.T) {
	store := NewMemKeyStore()
	_, cleartext, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil, nil)

	// Flip a hex char in the secret portion.
	tampered := cleartext[:len(cleartext)-1] + flipHex(cleartext[len(cleartext)-1])
	auth := &APIKeyAuthenticator{Store: store}
	if _, err := auth.Authenticate(t.Context(), tampered); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("tampered key accepted: %v", err)
	}
}

func TestAPIKey_RejectsRevoked(t *testing.T) {
	store := NewMemKeyStore()
	_, cleartext, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil, nil)
	_ = store.Revoke(t.Context(), "k1", time.Now())

	auth := &APIKeyAuthenticator{Store: store}
	if _, err := auth.Authenticate(t.Context(), cleartext); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("revoked key accepted: %v", err)
	}
}

func TestAPIKey_RejectsExpired(t *testing.T) {
	store := NewMemKeyStore()
	past := time.Now().Add(-time.Hour)
	k, ct, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil, &past)
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
	_, cleartext, _ := IssueAPIKey(store, t.Context(), "k1", "t", "", "u", nil, nil)

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
