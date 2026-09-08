// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/dazyflow/dazyflow/core"
)

type User struct {
	Email        string      `json:"email"`
	PasswordHash []byte      `json:"password_hash"`
	Subject      string      `json:"subject"`
	Tenant       string      `json:"tenant"`
	Workspace    string      `json:"workspace"`
	Roles        []core.Role `json:"roles"`
	CreatedAt    time.Time   `json:"created_at"`

	// All four are zero on a user who never enrolled.
	TOTPSecretEnc      []byte     `json:"totp_secret_enc,omitempty"`
	TOTPEnabled        bool       `json:"totp_enabled,omitempty"`
	TOTPEnrolledAt     *time.Time `json:"totp_enrolled_at,omitempty"`
	RecoveryCodeHashes []string   `json:"recovery_code_hashes,omitempty"`

	// Replay protection: a code stays valid ~90s, so the last step must be refused.
	TOTPLastStep int64 `json:"totp_last_step,omitempty"`

	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
	VerifyTokenHash []byte     `json:"verify_token_hash,omitempty"`
	VerifyExpiresAt *time.Time `json:"verify_expires_at,omitempty"`

	ResetTokenHash []byte     `json:"reset_token_hash,omitempty"`
	ResetExpiresAt *time.Time `json:"reset_expires_at,omitempty"`

	Notify NotifyPrefs `json:"notify,omitempty"`

	UI UIPrefs `json:"ui,omitempty"`

	// An empty status reads as active, so an old row is not locked out.
	Status        string     `json:"status,omitempty"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	SuspendReason string     `json:"suspend_reason,omitempty"`
}

// Implicit: an empty column reads as active.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

func (u User) Suspended() bool { return u.Status == StatusSuspended }

type UIPrefs struct {
	Theme    string `json:"theme,omitempty"`
	Language string `json:"language,omitempty"`
}

type NotifyPrefs struct {
	// A tri-state pointer: unset means ON, so it is opt-out.
	EmailOnFlowFailure *bool `json:"email_on_flow_failure,omitempty"`

	// A tri-state pointer: unset means ON, so it is opt-out.
	EmailOnSupportReply *bool `json:"email_on_support_reply,omitempty"`
}

func (p NotifyPrefs) EmailOnFlowFailureEnabled() bool {
	return p.EmailOnFlowFailure == nil || *p.EmailOnFlowFailure
}

func (p NotifyPrefs) EmailOnSupportReplyEnabled() bool {
	return p.EmailOnSupportReply == nil || *p.EmailOnSupportReply
}

func (u User) EmailVerified() bool { return u.VerifiedAt != nil }

type UserStore interface {
	GetByEmail(ctx context.Context, email string) (User, error)
	PutUser(ctx context.Context, u User) error
	ListUsers(ctx context.Context) ([]User, error)
}

var ErrUnknownUser = errors.New("unknown user")

// The unknown-user path compares against this, so a missing account costs the
// same bcrypt work as a wrong password and cannot be told apart by timing.
var timingDummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("dazyflow-timing-equalizer"), activeHashCost)
	return h
})

// Precomputed off the request path, or the first sign-in pays for it.
func WarmPasswordTiming() { _ = timingDummyHash() }

func VerifyPassword(ctx context.Context, store UserStore, email, password string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return User{}, ErrInvalidCredential
	}
	u, err := store.GetByEmail(ctx, email)
	if err != nil || len(u.PasswordHash) == 0 {
		// Spend the bcrypt cost even with no account, or timing enumerates users.
		_ = bcrypt.CompareHashAndPassword(timingDummyHash(), []byte(password))
		return User{}, ErrInvalidCredential
	}
	if bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) != nil {
		return User{}, ErrInvalidCredential
	}
	// The only point where a correct plaintext is in hand to re-hash with.
	if err := UpgradePasswordCost(ctx, store, u, password); err != nil {
		log.Printf("WARNING: could not re-hash password for %q at cost %d: %v", email, activeHashCost, err)
	}
	return u, nil
}

const PasswordHashCost = 12

const testPasswordHashCost = bcrypt.MinCost + 1

// What new hashes are minted at, and what an upgrade re-hashes to.
var activeHashCost = func() int {
	if testing.Testing() {
		return testPasswordHashCost
	}
	return PasswordHashCost
}()

func HashPassword(password string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("password required")
	}
	return bcrypt.GenerateFromPassword([]byte(password), activeHashCost)
}

func NeedsPasswordRehash(hash []byte) bool {
	cost, err := bcrypt.Cost(hash)
	if err != nil {
		return false
	}
	return cost < activeHashCost
}

// Only ever called with an already-verified password.
func UpgradePasswordCost(ctx context.Context, store UserStore, u User, password string) error {
	if !NeedsPasswordRehash(u.PasswordHash) {
		return nil
	}
	fresh, err := HashPassword(password)
	if err != nil {
		return err
	}
	u.PasswordHash = fresh
	return store.PutUser(ctx, u)
}

// Mutations rewrite the whole file, so it is for single-node use only.
type JSONUserStore struct {
	*jsonFileStore[string, User]
}

func normalizeUserEmail(u User) User {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	return u
}

func OpenJSONUserStore(path string) (*JSONUserStore, error) {
	base, err := newJSONFileStore(path, func(u User) string { return u.Email }, normalizeUserEmail)
	if err != nil {
		return nil, err
	}
	return &JSONUserStore{base}, nil
}

func (s *JSONUserStore) GetByEmail(_ context.Context, email string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.items[email]
	if !ok {
		return User{}, ErrUnknownUser
	}
	return u, nil
}

func (s *JSONUserStore) PutUser(_ context.Context, u User) error {
	u = normalizeUserEmail(u)
	if u.Email == "" {
		return fmt.Errorf("email required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[u.Email] = u
	return s.flushLocked()
}

func (s *JSONUserStore) ListUsers(_ context.Context) ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.items))
	for _, u := range s.items {
		out = append(out, u)
	}
	return out, nil
}

func (s *JSONUserStore) DeleteUser(_ context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, email)
	return s.flushLocked()
}
