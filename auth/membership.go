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

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Membership is one (user, tenant) pair: a person's access to an
// organization other than the one they own. The org a user creates
// on signup lives in User.Tenant + User.Roles directly; Memberships
// are *additional* orgs they've been invited to (or invited others
// into). The schema is intentionally permissive — a user can be a
// member of many orgs, each with its own role set and workspace.
//
// Lookups happen by lowercased email so the case-insensitivity is
// the same as User.Email. Tenant is the org ID (usr_<hex> for
// person-owned orgs; eventually team-owned orgs will get their own
// prefix).
type Membership struct {
	UserEmail string      `json:"user_email"`
	Tenant    string      `json:"tenant"`
	Workspace string      `json:"workspace"`
	Roles     []core.Role `json:"roles"`
	InvitedBy string      `json:"invited_by,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
}

// MembershipStore is the membership lookup boundary. Implementations
// back themselves with JSON or Postgres. ListByEmail powers the
// per-user org list (the switcher); ListByTenant powers the per-org
// member list (the admin page).
type MembershipStore interface {
	PutMembership(ctx context.Context, m Membership) error
	DeleteMembership(ctx context.Context, email, tenant string) error
	GetMembership(ctx context.Context, email, tenant string) (Membership, error)
	ListByEmail(ctx context.Context, email string) ([]Membership, error)
	ListByTenant(ctx context.Context, tenant string) ([]Membership, error)
}

var ErrUnknownMembership = errors.New("unknown membership")

// JSONMembershipStore persists memberships to a single JSON file —
// same shape as JSONUserStore. Intended for dev / single-node; a
// Postgres variant lives alongside PgUserStore for production.
type JSONMembershipStore struct {
	mu   sync.RWMutex
	path string
	// keyed by "email|tenant" — unique per pair, so re-inviting
	// someone who's already a member is an upsert.
	members map[string]Membership
}

func OpenJSONMembershipStore(path string) (*JSONMembershipStore, error) {
	s := &JSONMembershipStore{path: path, members: make(map[string]Membership)}
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
	var slice []Membership
	if err := json.Unmarshal(data, &slice); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	for _, m := range slice {
		m.UserEmail = strings.ToLower(strings.TrimSpace(m.UserEmail))
		s.members[membershipKey(m.UserEmail, m.Tenant)] = m
	}
	return s, nil
}

func membershipKey(email, tenant string) string {
	return strings.ToLower(strings.TrimSpace(email)) + "|" + tenant
}

func (s *JSONMembershipStore) PutMembership(_ context.Context, m Membership) error {
	m.UserEmail = strings.ToLower(strings.TrimSpace(m.UserEmail))
	if m.UserEmail == "" {
		return fmt.Errorf("user_email required")
	}
	if m.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[membershipKey(m.UserEmail, m.Tenant)] = m
	return s.flushLocked()
}

func (s *JSONMembershipStore) DeleteMembership(_ context.Context, email, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.members, membershipKey(email, tenant))
	return s.flushLocked()
}

func (s *JSONMembershipStore) GetMembership(_ context.Context, email, tenant string) (Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.members[membershipKey(email, tenant)]
	if !ok {
		return Membership{}, ErrUnknownMembership
	}
	return m, nil
}

func (s *JSONMembershipStore) ListByEmail(_ context.Context, email string) ([]Membership, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Membership{}
	for _, m := range s.members {
		if m.UserEmail == email {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *JSONMembershipStore) ListByTenant(_ context.Context, tenant string) ([]Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Membership{}
	for _, m := range s.members {
		if m.Tenant == tenant {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *JSONMembershipStore) flushLocked() error {
	if s.path == "" {
		return nil
	}
	slice := make([]Membership, 0, len(s.members))
	for _, m := range s.members {
		slice = append(slice, m)
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
