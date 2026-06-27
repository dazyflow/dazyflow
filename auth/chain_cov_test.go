package auth

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fatalAuthenticator returns a non-ErrInvalidCredential error to drive
// Chain's short-circuit branch.
type fatalAuthenticator struct{ err error }

func (f fatalAuthenticator) Authenticate(context.Context, string) (core.Principal, error) {
	return core.Principal{}, f.err
}

func TestChain_Cov(t *testing.T) {
	ctx := context.Background()

	// Empty chain → ErrInvalidCredential.
	if _, err := (Chain{}).Authenticate(ctx, "x"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("empty chain err = %v", err)
	}

	// A non-ErrInvalidCredential error short-circuits the whole chain.
	boom := errors.New("backend down")
	chain := Chain{fatalAuthenticator{boom}, alwaysReject{}}
	if _, err := chain.Authenticate(ctx, "x"); !errors.Is(err, boom) {
		t.Errorf("fatal chain err = %v, want %v", err, boom)
	}

	// All-reject chain returns the last ErrInvalidCredential.
	rejectChain := Chain{alwaysReject{}, alwaysReject{}}
	if _, err := rejectChain.Authenticate(ctx, "x"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("reject chain err = %v", err)
	}
}

func TestOIDCAuthenticate_Cov(t *testing.T) {
	ctx := context.Background()

	// No verifier configured → error.
	a := &OIDCAuthenticator{}
	if _, err := a.Authenticate(ctx, "a.b.c"); err == nil {
		t.Error("nil verifier should error")
	}

	// Non-JWT credential (not two dots) → falls through as ErrInvalidCredential.
	a = &OIDCAuthenticator{Verifier: stubVerifier{}}
	if _, err := a.Authenticate(ctx, "notajwt"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("non-jwt err = %v", err)
	}

	// Verifier failure wraps ErrInvalidCredential.
	a = &OIDCAuthenticator{Verifier: failVerifier{errors.New("bad sig")}}
	if _, err := a.Authenticate(ctx, "a.b.c"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("verify failure err = %v", err)
	}
}

type failVerifier struct{ err error }

func (f failVerifier) Verify(context.Context, string) (Claims, error) {
	return Claims{}, f.err
}

func TestLooksLikeJWT_Cov(t *testing.T) {
	if !looksLikeJWT("a.b.c") {
		t.Error("a.b.c should look like a JWT")
	}
	if looksLikeJWT("a.b") {
		t.Error("a.b should not look like a JWT")
	}
	if looksLikeJWT("plain") {
		t.Error("plain should not look like a JWT")
	}
}

func TestRolePermissions_Cov(t *testing.T) {
	// Unknown role → no permissions.
	if perms := rolePermissions("totally-unknown-role"); len(perms) != 0 {
		t.Errorf("unknown role perms = %v, want none", perms)
	}
}

// failKeyStore.PutKey always errors, driving IssueAPIKey's persist-failure path.
type failKeyStore struct{ err error }

func (f failKeyStore) PutKey(context.Context, APIKey) error { return f.err }

func TestIssueAPIKey_PutError(t *testing.T) {
	boom := errors.New("db down")
	if _, _, err := IssueAPIKey(failKeyStore{boom}, context.Background(), "k1", "t", "ws", "u", nil, nil); !errors.Is(err, boom) {
		t.Errorf("IssueAPIKey put err = %v, want %v", err, boom)
	}
}

// failSessionStore.PutSession always errors, driving IssueSession's path.
type failSessionStore struct{ err error }

func (f failSessionStore) GetSession(context.Context, string) (Session, error) {
	return Session{}, ErrInvalidCredential
}
func (f failSessionStore) PutSession(context.Context, Session) error   { return f.err }
func (f failSessionStore) DeleteSession(context.Context, string) error { return nil }

func TestIssueSession_PutError(t *testing.T) {
	boom := errors.New("db down")
	if _, _, err := IssueSession(context.Background(), failSessionStore{boom}, User{Subject: "u"}, 0); !errors.Is(err, boom) {
		t.Errorf("IssueSession put err = %v, want %v", err, boom)
	}
}
