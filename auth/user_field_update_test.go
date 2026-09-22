// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func enrolledTOTPUser(t *testing.T, email string) (*JSONUserStore, []byte, string, []string) {
	t.Helper()
	ctx := context.Background()
	key := testTOTPKey(t)
	users := newUserStoreWithUser(t, email)
	setup, err := EnrolStart(ctx, users, key, email)
	if err != nil {
		t.Fatalf("EnrolStart: %v", err)
	}
	codes, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32))
	if err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}
	return users, key, setup.SecretBase32, codes
}

// Two logins racing with one TOTP code (each on its own challenge) must not
// both succeed: the step check and the step write are one atomic update.
func TestConsumeTOTPChallenge_ConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	const email = "race@example.com"
	users, key, secret, codes := enrolledTOTPUser(t, email)
	challenges := NewMemTOTPChallengeStore()

	for _, tc := range []struct {
		name, code, recovery string
	}{
		{"totp", codeFor(t, secret), ""},
		{"recovery", "", codes[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const n = 8
			toks := make([]string, n)
			for i := range toks {
				toks[i], _ = IssueTOTPChallenge(ctx, challenges, email)
			}
			var wins atomic.Int32
			var wg sync.WaitGroup
			for _, tok := range toks {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := ConsumeTOTPChallenge(ctx, challenges, users, key, tok, tc.code, tc.recovery); err == nil {
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			if got := wins.Load(); got != 1 {
				t.Fatalf("%d logins succeeded with one code, want exactly 1", got)
			}
		})
	}
}

// staleReadStore hands out the row as read, then lets a concurrent writer
// change it before the caller writes back.
type staleReadStore struct {
	*JSONUserStore
	afterRead func()
}

func (s *staleReadStore) GetByEmail(ctx context.Context, email string) (User, error) {
	u, err := s.JSONUserStore.GetByEmail(ctx, email)
	if s.afterRead != nil {
		f := s.afterRead
		s.afterRead = nil
		f()
	}
	return u, err
}

func suspend(t *testing.T, s *JSONUserStore, email string) func() {
	return func() {
		u, err := s.GetByEmail(context.Background(), email)
		if err != nil {
			t.Fatalf("GetByEmail: %v", err)
		}
		u.Status = StatusSuspended
		if err := s.PutUser(context.Background(), u); err != nil {
			t.Fatalf("PutUser: %v", err)
		}
	}
}

func TestConsumeTOTPChallenge_DoesNotUndoConcurrentSuspension(t *testing.T) {
	ctx := context.Background()
	const email = "stale@example.com"
	inner, key, secret, _ := enrolledTOTPUser(t, email)
	store := &staleReadStore{JSONUserStore: inner, afterRead: suspend(t, inner, email)}
	challenges := NewMemTOTPChallengeStore()
	tok, _ := IssueTOTPChallenge(ctx, challenges, email)
	if _, err := ConsumeTOTPChallenge(ctx, challenges, store, key, tok, codeFor(t, secret), ""); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if u, _ := inner.GetByEmail(ctx, email); !u.Suspended() {
		t.Error("TOTP login overwrote a concurrent suspension with the stale row")
	}
}

func TestUpgradePasswordCost_DoesNotUndoConcurrentChange(t *testing.T) {
	ctx := context.Background()
	const email = "cost@example.com"
	const pw = "correct-horse-battery-staple"
	inner, _ := OpenJSONUserStore("")
	legacy, _ := bcrypt.GenerateFromPassword([]byte(pw), activeHashCost-1)
	_ = inner.PutUser(ctx, User{Email: email, Subject: email, PasswordHash: legacy})

	store := &staleReadStore{JSONUserStore: inner, afterRead: suspend(t, inner, email)}
	if _, err := VerifyPassword(ctx, store, email, pw); err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	u, _ := inner.GetByEmail(ctx, email)
	if !u.Suspended() {
		t.Error("password re-hash overwrote a concurrent suspension")
	}
	if cost, _ := bcrypt.Cost(u.PasswordHash); cost != activeHashCost {
		t.Errorf("hash cost = %d, want upgraded to %d", cost, activeHashCost)
	}

	// A reset between read and re-hash must win over the re-hash.
	_ = inner.PutUser(ctx, User{Email: email, Subject: email, PasswordHash: legacy})
	reset := []byte("reset-hash")
	store.afterRead = func() {
		u, _ := inner.GetByEmail(ctx, email)
		u.PasswordHash = reset
		_ = inner.PutUser(ctx, u)
	}
	_, _ = VerifyPassword(ctx, store, email, pw)
	if u, _ := inner.GetByEmail(ctx, email); !bytes.Equal(u.PasswordHash, reset) {
		t.Error("password re-hash overwrote a concurrent password reset")
	}
}

func TestMarkEmailVerified(t *testing.T) {
	ctx := context.Background()
	const email = "verify@example.com"
	store := newUserStoreWithUser(t, email)
	now := time.Now().UTC()
	suspend(t, store, email)()
	if changed, err := MarkEmailVerified(ctx, store, email, now); err != nil || !changed {
		t.Fatalf("MarkEmailVerified = %v, %v", changed, err)
	}
	if changed, _ := MarkEmailVerified(ctx, store, email, now); changed {
		t.Error("second MarkEmailVerified reported a change")
	}
	u, _ := store.GetByEmail(ctx, email)
	if !u.EmailVerified() || !u.Suspended() {
		t.Errorf("user = %+v, want verified and still suspended", u)
	}
}
