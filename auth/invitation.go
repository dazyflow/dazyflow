// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// Invitation is a pending offer for someone to join an org: the prospective
// member's email (case-insensitive), the org, the roles they get on accept, who
// invited them, and an opaque single-use token. The recipient hits an /invite
// link carrying the token, signs in or signs up, and the daemon turns the
// invitation into a Membership.
//
// Token is stored unhashed: it only grants viewing and accepting the invite, so
// hashing would cost operator inspection of pending invites for no real gain.
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

func (i Invitation) IsPending(now time.Time) bool {
	return i.AcceptedAt == nil && i.RevokedAt == nil && now.Before(i.ExpiresAt)
}

// SignupInviteTenant marks an Invitation as a platform signup-invite rather
// than an org-join invite: a platform admin authorizing one specific email to
// create its OWN account on a deployment where self-serve signup is disabled.
//
// The value is not a real tenant — nothing is ever keyed on it, and signup mints
// a fresh usr_<hex>. Reusing the invitations store rather than a parallel table
// means signup-invites inherit its TTL, audit trail and GDPR erasure for free;
// the org-join handlers reject any invite where IsSignupInvite is true.
const SignupInviteTenant = "_signup"

// IsSignupInvite reports whether this invitation is a platform
// signup-invite (see SignupInviteTenant) rather than an org-join
// invite. The two share a store but never a code path.
func (i Invitation) IsSignupInvite() bool { return i.Tenant == SignupInviteTenant }

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

type JSONInvitationStore struct {
	*jsonFileStore[string, Invitation]
}

func OpenJSONInvitationStore(path string) (*JSONInvitationStore, error) {
	base, err := newJSONFileStore(path, func(i Invitation) string { return i.Token }, nil)
	if err != nil {
		return nil, err
	}
	return &JSONInvitationStore{base}, nil
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

func (s *JSONInvitationStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	ident = strings.ToLower(strings.TrimSpace(ident))
	if ident == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for token, inv := range s.items {
		if strings.ToLower(strings.TrimSpace(inv.InvitedBy)) == ident {
			inv.InvitedBy = core.ErasedIdentity
			s.items[token] = inv
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	if err := s.flushLocked(); err != nil {
		return 0, err
	}
	return n, nil
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

func (s *JSONInvitationStore) filter(pred func(Invitation) bool) []Invitation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Invitation{}
	for _, i := range s.items {
		if pred(i) {
			out = append(out, i)
		}
	}
	return out
}

func (s *JSONInvitationStore) deleteWhere(pred func(Invitation) bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for token, i := range s.items {
		if pred(i) {
			delete(s.items, token)
			n++
		}
	}
	return n, s.flushLocked()
}

func emailMatches(email string) func(Invitation) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	return func(i Invitation) bool { return strings.ToLower(i.Email) == email }
}

func (s *JSONInvitationStore) ListByTenant(_ context.Context, tenant string) ([]Invitation, error) {
	return s.filter(func(i Invitation) bool { return i.Tenant == tenant }), nil
}

func (s *JSONInvitationStore) ListByEmail(_ context.Context, email string) ([]Invitation, error) {
	return s.filter(emailMatches(email)), nil
}

func (s *JSONInvitationStore) DeleteByEmail(_ context.Context, email string) (int, error) {
	return s.deleteWhere(emailMatches(email))
}

func (s *JSONInvitationStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	return s.deleteWhere(func(i Invitation) bool { return i.Tenant == tenant })
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
