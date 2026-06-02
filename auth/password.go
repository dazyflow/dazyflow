package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.sr.ht/~klahr/hazyflow/core"
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
}

// UserStore is the password-auth lookup boundary. Implementations may
// back themselves with a JSON file (this package's JSONUserStore), a
// Postgres table, or whatever else fits the deployment.
type UserStore interface {
	GetByEmail(ctx context.Context, email string) (User, error)
	PutUser(ctx context.Context, u User) error
	ListUsers(ctx context.Context) ([]User, error)
}

var ErrUnknownUser = errors.New("unknown user")

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
	if err != nil {
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
// / single-node deployments — production should use a database.
type JSONUserStore struct {
	mu    sync.RWMutex
	path  string
	users map[string]User
}

func OpenJSONUserStore(path string) (*JSONUserStore, error) {
	s := &JSONUserStore{path: path, users: make(map[string]User)}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var slice []User
	if err := json.Unmarshal(data, &slice); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	for _, u := range slice {
		u.Email = strings.ToLower(strings.TrimSpace(u.Email))
		s.users[u.Email] = u
	}
	return s, nil
}

func (s *JSONUserStore) GetByEmail(_ context.Context, email string) (User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[email]
	if !ok {
		return User{}, ErrUnknownUser
	}
	return u, nil
}

func (s *JSONUserStore) PutUser(_ context.Context, u User) error {
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	if u.Email == "" {
		return fmt.Errorf("email required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.Email] = u
	return s.flushLocked()
}

func (s *JSONUserStore) ListUsers(_ context.Context) ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	return out, nil
}

func (s *JSONUserStore) flushLocked() error {
	if s.path == "" {
		return nil
	}
	slice := make([]User, 0, len(s.users))
	for _, u := range s.users {
		slice = append(slice, u)
	}
	data, err := json.MarshalIndent(slice, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
