// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Both implementations answer to one contract. The Postgres one exists to
// replace the in-memory one underneath callers that cannot tell them apart, so
// "the same tests pass" is the requirement.
func runEphemeralConformance(t *testing.T, mk func(t *testing.T) EphemeralStore) {
	t.Helper()
	ctx := context.Background()
	soon := func() time.Time { return time.Now().Add(5 * time.Minute) }

	t.Run("PutGetDelete", func(t *testing.T) {
		s := mk(t)
		if err := s.Put(ctx, EphemeralGoogleSignIn, "tok", []byte(`{"tenant":"acme"}`), soon()); err != nil {
			t.Fatal(err)
		}
		got, attempts, err := s.Get(ctx, EphemeralGoogleSignIn, "tok")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if string(got) != `{"tenant":"acme"}` || attempts != 0 {
			t.Fatalf("get = %q / %d", got, attempts)
		}
		if err := s.Delete(ctx, EphemeralGoogleSignIn, "tok"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Get(ctx, EphemeralGoogleSignIn, "tok"); !errors.Is(err, ErrEphemeralNotFound) {
			t.Fatalf("get after delete = %v, want ErrEphemeralNotFound", err)
		}
	})

	t.Run("UnknownTokenIsNotFound", func(t *testing.T) {
		s := mk(t)
		if _, _, err := s.Get(ctx, EphemeralGoogleSignIn, "nope"); !errors.Is(err, ErrEphemeralNotFound) {
			t.Fatalf("= %v, want ErrEphemeralNotFound", err)
		}
	})

	// The kinds are separate namespaces: a token minted for one flow must never
	// be redeemable by another.
	t.Run("KindsDoNotCollide", func(t *testing.T) {
		s := mk(t)
		if err := s.Put(ctx, EphemeralGoogleSignIn, "same", []byte(`"a"`), soon()); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, EphemeralOAuthPending, "same", []byte(`"b"`), soon()); err != nil {
			t.Fatal(err)
		}
		a, _, err := s.Get(ctx, EphemeralGoogleSignIn, "same")
		if err != nil || string(a) != `"a"` {
			t.Fatalf("google kind = %q / %v", a, err)
		}
		b, _, err := s.Get(ctx, EphemeralOAuthPending, "same")
		if err != nil || string(b) != `"b"` {
			t.Fatalf("oauth kind = %q / %v", b, err)
		}
		// Deleting one leaves the other.
		if err := s.Delete(ctx, EphemeralGoogleSignIn, "same"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Get(ctx, EphemeralOAuthPending, "same"); err != nil {
			t.Fatalf("deleting one kind took the other: %v", err)
		}
	})

	// An expired token must read as gone whether or not a sweep has run — a
	// late sweep may not become an accepted stale token.
	t.Run("ExpiredIsGoneBeforeAnySweep", func(t *testing.T) {
		s := mk(t)
		if err := s.Put(ctx, EphemeralSignInHandoff, "old", []byte(`"x"`), time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Get(ctx, EphemeralSignInHandoff, "old"); !errors.Is(err, ErrEphemeralNotFound) {
			t.Fatalf("expired token = %v, want ErrEphemeralNotFound", err)
		}
		if _, err := s.IncrAttempts(ctx, EphemeralSignInHandoff, "old"); !errors.Is(err, ErrEphemeralNotFound) {
			t.Fatalf("IncrAttempts on an expired token = %v, want ErrEphemeralNotFound", err)
		}
	})

	t.Run("SweepRemovesExpiredAndKeepsLive", func(t *testing.T) {
		s := mk(t)
		if err := s.Put(ctx, EphemeralTOTPChallenge, "dead", []byte(`"x"`), time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, EphemeralTOTPChallenge, "live", []byte(`"y"`), soon()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Sweep(ctx); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if _, _, err := s.Get(ctx, EphemeralTOTPChallenge, "live"); err != nil {
			t.Fatalf("sweep removed a live entry: %v", err)
		}
	})

	// The brute-force cap depends on this: concurrent wrong guesses against one
	// token must not read the same count and lose increments.
	t.Run("IncrAttemptsIsAtomic", func(t *testing.T) {
		s := mk(t)
		if err := s.Put(ctx, EphemeralTOTPChallenge, "tok", []byte(`"x"`), soon()); err != nil {
			t.Fatal(err)
		}
		const n = 20
		var wg sync.WaitGroup
		seen := make([]int, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := s.IncrAttempts(ctx, EphemeralTOTPChallenge, "tok")
				if err != nil {
					t.Errorf("IncrAttempts: %v", err)
					return
				}
				seen[i] = got
			}()
		}
		wg.Wait()
		_, attempts, err := s.Get(ctx, EphemeralTOTPChallenge, "tok")
		if err != nil {
			t.Fatal(err)
		}
		if attempts != n {
			t.Fatalf("after %d concurrent increments the count is %d — increments were lost", n, attempts)
		}
		// Every caller saw a distinct value, which is what "atomic" buys.
		distinct := map[int]bool{}
		for _, v := range seen {
			distinct[v] = true
		}
		if len(distinct) != n {
			t.Fatalf("increments returned %d distinct values across %d callers", len(distinct), n)
		}
	})

	t.Run("PutOverwritesAndResetsAttempts", func(t *testing.T) {
		s := mk(t)
		if err := s.Put(ctx, EphemeralTOTPChallenge, "tok", []byte(`"first"`), soon()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.IncrAttempts(ctx, EphemeralTOTPChallenge, "tok"); err != nil {
			t.Fatal(err)
		}
		if err := s.Put(ctx, EphemeralTOTPChallenge, "tok", []byte(`"second"`), soon()); err != nil {
			t.Fatal(err)
		}
		got, attempts, err := s.Get(ctx, EphemeralTOTPChallenge, "tok")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != `"second"` || attempts != 0 {
			t.Fatalf("after re-put: %q / %d attempts, want the new payload and a reset count", got, attempts)
		}
	})

	t.Run("DeleteIsIdempotent", func(t *testing.T) {
		s := mk(t)
		if err := s.Delete(ctx, EphemeralGoogleSignIn, "never-existed"); err != nil {
			t.Fatalf("deleting an unknown token: %v", err)
		}
	})
}

func TestEphemeral_Memory(t *testing.T) {
	t.Parallel()
	runEphemeralConformance(t, func(*testing.T) EphemeralStore { return NewMemEphemeralStore() })
}

func TestEphemeral_Postgres(t *testing.T) {
	dsn := os.Getenv("DAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run the Postgres ephemeral-store suite")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := NewPgEphemeralStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	runEphemeralConformance(t, func(t *testing.T) EphemeralStore {
		t.Helper()
		// Namespace each subtest so they cannot see each other's tokens.
		n++
		return &prefixedEphemeral{inner: store, prefix: fmt.Sprintf("t%d-%d-", time.Now().UnixNano(), n)}
	})
	_, _ = store.Sweep(ctx)
}

// prefixedEphemeral namespaces a shared Postgres store per subtest.
type prefixedEphemeral struct {
	inner  EphemeralStore
	prefix string
}

func (p *prefixedEphemeral) k(kind string) string { return p.prefix + kind }

func (p *prefixedEphemeral) Put(ctx context.Context, kind, token string, payload []byte, exp time.Time) error {
	return p.inner.Put(ctx, p.k(kind), token, payload, exp)
}
func (p *prefixedEphemeral) Get(ctx context.Context, kind, token string) ([]byte, int, error) {
	return p.inner.Get(ctx, p.k(kind), token)
}
func (p *prefixedEphemeral) Delete(ctx context.Context, kind, token string) error {
	return p.inner.Delete(ctx, p.k(kind), token)
}
func (p *prefixedEphemeral) IncrAttempts(ctx context.Context, kind, token string) (int, error) {
	return p.inner.IncrAttempts(ctx, p.k(kind), token)
}
func (p *prefixedEphemeral) Sweep(ctx context.Context) (int, error) { return p.inner.Sweep(ctx) }

// The adapter must be a drop-in for MemTOTPChallengeStore: the sign-in path
// cannot tell which one it is talking to.
func TestEphemeralTOTPChallengeStore_MatchesTheMemoryStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stores := map[string]TOTPChallengeStore{
		"memory":    NewMemTOTPChallengeStore(),
		"ephemeral": NewEphemeralTOTPChallengeStore(NewMemEphemeralStore()),
	}
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			c := TOTPChallenge{
				Email:     "ada@example.com",
				ExpiresAt: time.Now().Add(5 * time.Minute),
				Tenant:    "acme",
				Workspace: "main",
			}
			if err := s.Put(ctx, "tok", c); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(ctx, "tok")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			// The org override must survive: it is what lands an SSO user in the
			// org they were signing into rather than their home org.
			if got.Email != c.Email || got.Tenant != "acme" || got.Workspace != "main" {
				t.Fatalf("challenge round-trip lost content: %+v", got)
			}
			if got.Attempts != 0 {
				t.Fatalf("fresh challenge has %d attempts", got.Attempts)
			}

			// The brute-force cap counts through here.
			for want := 1; want <= 3; want++ {
				n, err := s.IncrAttempts(ctx, "tok")
				if err != nil {
					t.Fatal(err)
				}
				if n != want {
					t.Fatalf("IncrAttempts = %d, want %d", n, want)
				}
			}
			if got, _ := s.Get(ctx, "tok"); got.Attempts != 3 {
				t.Fatalf("Get reports %d attempts, want 3", got.Attempts)
			}

			if err := s.Delete(ctx, "tok"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(ctx, "tok"); !errors.Is(err, ErrChallengeUnknown) {
				t.Fatalf("get after delete = %v, want ErrChallengeUnknown", err)
			}
			if _, err := s.IncrAttempts(ctx, "gone"); !errors.Is(err, ErrChallengeUnknown) {
				t.Fatalf("IncrAttempts on an unknown token = %v, want ErrChallengeUnknown", err)
			}
		})
	}
}

// An expired challenge must not be redeemable, whichever store holds it.
func TestEphemeralTOTPChallengeStore_ExpiredIsUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := NewEphemeralTOTPChallengeStore(NewMemEphemeralStore())
	if err := s.Put(ctx, "tok", TOTPChallenge{
		Email: "ada@example.com", ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "tok"); !errors.Is(err, ErrChallengeUnknown) {
		t.Fatalf("expired challenge = %v, want ErrChallengeUnknown", err)
	}
}
