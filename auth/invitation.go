package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Invitation is a pending offer for someone to join an org. It carries
// the prospective member's email (case-insensitive), the org they're
// being invited into, the roles they'll get on accept, who invited
// them, and an opaque single-use token. The recipient hits an /invite
// link that includes the token; they sign in (or sign up using the
// invited email) and the daemon turns the invitation into a
// Membership.
//
// Token is the URL-safe random ID; we don't store a hash because
// the token only grants access to view + accept the invite (no
// privileged action). Hashing would prevent operator inspection of
// pending invites with no real security gain.
type Invitation struct {
	Token      string      `json:"token"`
	Email      string      `json:"email"`
	Tenant     string      `json:"tenant"`
	Workspace  string      `json:"workspace"`
	Roles      []core.Role `json:"roles"`
	InvitedBy  string      `json:"invited_by"`
	CreatedAt  time.Time   `json:"created_at"`
	ExpiresAt  time.Time   `json:"expires_at"`
	AcceptedAt *time.Time  `json:"accepted_at,omitempty"`
	RevokedAt  *time.Time  `json:"revoked_at,omitempty"`
}

// IsPending reports whether the invitation can still be accepted —
// not accepted, not revoked, not past its expiry. The /invite
// detail endpoint exposes this so the UI can render a "this invite
// has been used / cancelled / expired" message rather than just
// failing on accept.
func (i Invitation) IsPending(now time.Time) bool {
	return i.AcceptedAt == nil && i.RevokedAt == nil && now.Before(i.ExpiresAt)
}

// InvitationStore is the invitation lookup boundary. GetByToken is
// the no-auth lookup used by the /invite/<token> detail endpoint;
// the rest of the surface is admin-only.
type InvitationStore interface {
	PutInvitation(ctx context.Context, inv Invitation) error
	GetByToken(ctx context.Context, token string) (Invitation, error)
	ListByTenant(ctx context.Context, tenant string) ([]Invitation, error)
	MarkAccepted(ctx context.Context, token string, at time.Time) error
	MarkRevoked(ctx context.Context, token string, at time.Time) error
}

var ErrUnknownInvitation = errors.New("unknown invitation")

// MintInvitationToken returns a URL-safe random token long enough
// that guessing is infeasible (32 hex chars = 128 bits of entropy).
// Prefix "inv_" so it can be visually distinguished from a session
// token or API key in logs.
func MintInvitationToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("invitation token: %w", err)
	}
	return "inv_" + hex.EncodeToString(b), nil
}

// JSONInvitationStore is the JSON-file backing — same shape as the
// other dev stores in this package. Tokens unique → keyed directly
// on token.
type JSONInvitationStore struct {
	mu    sync.RWMutex
	path  string
	items map[string]Invitation
}

func OpenJSONInvitationStore(path string) (*JSONInvitationStore, error) {
	s := &JSONInvitationStore{path: path, items: make(map[string]Invitation)}
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
	var slice []Invitation
	if err := json.Unmarshal(data, &slice); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	for _, i := range slice {
		s.items[i.Token] = i
	}
	return s, nil
}

func (s *JSONInvitationStore) PutInvitation(_ context.Context, inv Invitation) error {
	if inv.Token == "" {
		return fmt.Errorf("token required")
	}
	if inv.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	inv.Email = strings.ToLower(strings.TrimSpace(inv.Email))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[inv.Token] = inv
	return s.flushLocked()
}

func (s *JSONInvitationStore) GetByToken(_ context.Context, token string) (Invitation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inv, ok := s.items[token]
	if !ok {
		return Invitation{}, ErrUnknownInvitation
	}
	return inv, nil
}

func (s *JSONInvitationStore) ListByTenant(_ context.Context, tenant string) ([]Invitation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Invitation{}
	for _, i := range s.items {
		if i.Tenant == tenant {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *JSONInvitationStore) MarkAccepted(_ context.Context, token string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.items[token]
	if !ok {
		return ErrUnknownInvitation
	}
	inv.AcceptedAt = &at
	s.items[token] = inv
	return s.flushLocked()
}

func (s *JSONInvitationStore) MarkRevoked(_ context.Context, token string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.items[token]
	if !ok {
		return ErrUnknownInvitation
	}
	inv.RevokedAt = &at
	s.items[token] = inv
	return s.flushLocked()
}

func (s *JSONInvitationStore) flushLocked() error {
	if s.path == "" {
		return nil
	}
	slice := make([]Invitation, 0, len(s.items))
	for _, i := range s.items {
		slice = append(slice, i)
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
