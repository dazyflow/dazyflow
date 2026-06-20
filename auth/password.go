package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.sr.ht/~klahr/dazyflow/core"
)

// User is a password-authenticated identity. PasswordHash is bcrypt.
// Roles are checked the same way API-key roles are, so once a user
// signs in their session principal is indistinguishable from an
// equivalent API-key principal downstream.
type User struct {
	Email        string      `json:"email"`
	PasswordHash []byte      `json:"password_hash"`
	Subject      string      `json:"subject"`
	Tenant       string      `json:"tenant"`
	Workspace    string      `json:"workspace"`
	Roles        []core.Role `json:"roles"`
	CreatedAt    time.Time   `json:"created_at"`

	// TOTP 2FA state. All four fields are zero on a user who has never
	// enrolled; the JSON tags carry omitempty so existing user-store
	// files stay byte-compatible when 2FA is off. The secret is stored
	// AES-256-GCM-encrypted (see auth/totp.go) so a leak of the store —
	// a DB backup, a stray JSON file — yields ciphertext, not live
	// seeds. Recovery codes are kept as bcrypt hashes only; the
	// plaintext is shown to the user exactly once at mint time.
	//
	// TOTPSecretEnc holds a *pending* secret (TOTPEnabled=false) between
	// EnrolStart and EnrolConfirm, and the *active* secret once enabled.
	TOTPSecretEnc      []byte     `json:"totp_secret_enc,omitempty"`
	TOTPEnabled        bool       `json:"totp_enabled,omitempty"`
	TOTPEnrolledAt     *time.Time `json:"totp_enrolled_at,omitempty"`
	RecoveryCodeHashes []string   `json:"recovery_code_hashes,omitempty"`

	// TOTPLastStep is the most recent TOTP time-step (unix/period) this
	// user successfully authenticated with. A code is valid for ~90s
	// (period 30 × skew ±1), so without recording the consumed step an
	// observed code could be replayed inside its window. ConsumeTOTPChallenge
	// rejects any code whose step is <= this. Zero = never used a TOTP code.
	TOTPLastStep int64 `json:"totp_last_step,omitempty"`

	// Email-verification state. All three are zero on deployments
	// without a transactional mailer (verification can't run there) and
	// on accounts created before the feature; omitempty keeps existing
	// stores byte-compatible. VerifyTokenHash is the SHA-256 of the
	// emailed token — the store never holds the clickable secret.
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
	VerifyTokenHash []byte     `json:"verify_token_hash,omitempty"`
	VerifyExpiresAt *time.Time `json:"verify_expires_at,omitempty"`

	// Password-reset state. Both zero on accounts with no reset in
	// flight; omitempty keeps existing stores byte-compatible. Mirrors
	// the email-verification token: ResetTokenHash is the SHA-256 of the
	// emailed token (the store never holds the clickable secret), and
	// the pair is cleared the moment the reset is consumed or superseded
	// by a fresh request. See daemon/password_reset.go.
	ResetTokenHash []byte     `json:"reset_token_hash,omitempty"`
	ResetExpiresAt *time.Time `json:"reset_expires_at,omitempty"`
}

// EmailVerified reports whether the account's address was confirmed.
func (u User) EmailVerified() bool { return u.VerifiedAt != nil }

// UserStore is the password-auth lookup boundary. Implementations may
// back themselves with a JSON file (this package's JSONUserStore), a
// Postgres table, or whatever else fits the deployment.
type UserStore interface {
	GetByEmail(ctx context.Context, email string) (User, error)
	PutUser(ctx context.Context, u User) error
	ListUsers(ctx context.Context) ([]User, error)
}

var ErrUnknownUser = errors.New("unknown user")

// timingDummyHash is a bcrypt hash of a throwaway password. The unknown-user
// and no-password-set paths compare against it so they spend the same bcrypt
// cost as a genuine wrong-password compare. Without this, a missing account
// returns ~instantly while an existing one pays full bcrypt cost — a remotely
// observable timing difference that reveals which emails have accounts, the
// very enumeration this function's uniform error is meant to prevent.
var timingDummyHash, _ = bcrypt.GenerateFromPassword([]byte("dazyflow-timing-equalizer"), bcrypt.DefaultCost)

// VerifyPassword normalizes email and bcrypt-compares the password.
// Returns the User on success; ErrInvalidCredential on any failure
// (unknown user OR wrong password) so callers cannot enumerate
// accounts via the error distinction.
func VerifyPassword(ctx context.Context, store UserStore, email, password string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || password == "" {
		return User{}, ErrInvalidCredential
	}
	u, err := store.GetByEmail(ctx, email)
	if err != nil || len(u.PasswordHash) == 0 {
		// Equalize timing: spend the bcrypt cost even when there's no account
		// (or no password set, e.g. an SSO-only user) so the response time
		// doesn't betray account existence.
		_ = bcrypt.CompareHashAndPassword(timingDummyHash, []byte(password))
		return User{}, ErrInvalidCredential
	}
	if bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) != nil {
		return User{}, ErrInvalidCredential
	}
	return u, nil
}

// HashPassword wraps bcrypt so callers don't have to import the package
// (and keeps the cost choice in one place).
func HashPassword(password string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("password required")
	}
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

// JSONUserStore persists users to a single JSON file. Mutations rewrite
// the file atomically (.tmp + rename) under a mutex. Intended for dev
// / single-node deployments — production should use a database. The
// load/flush/atomic-write machinery lives in the embedded jsonFileStore.
type JSONUserStore struct {
	*jsonFileStore[string, User]
}

// normalizeUserEmail lower-cases and trims the record's email — applied on
// load and on Put so the map key and the stored value stay canonical.
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

// DeleteUser removes the user (erasure, Art. 17). Idempotent.
func (s *JSONUserStore) DeleteUser(_ context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, email)
	return s.flushLocked()
}
