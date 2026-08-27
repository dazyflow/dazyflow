// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"git.sr.ht/~klahr/dazyflow/core"
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

// SignupInviteTenant is the sentinel Tenant value that marks an
// Invitation as a platform signup-invite rather than an org-join
// invite. A platform admin issues these to authorize one specific
// email to create its OWN account — own tenant, default signup roles —
// on a deployment where self-serve signup is disabled (see the signUp
// gate in daemon/httpsignup.go). The value is NOT a real tenant: no
// org, membership, workspace, or profile is ever keyed on it, and no
// account ever lands here (signup mints a fresh usr_<hex> tenant).
// Reusing the invitations store rather than a parallel table means
// signup-invites inherit its TTL, audit trail, and GDPR erasure
// (DeleteByEmail) for free. The org-join handlers
// (viewInvitation/acceptInvitation) reject any invite where
// IsSignupInvite is true, and a tenant admin's ListByTenant never
// returns these because their tenant is never the sentinel.
const SignupInviteTenant = "_signup"

// IsSignupInvite reports whether this invitation is a platform
// signup-invite (see SignupInviteTenant) rather than an org-join
// invite. The two share a store but never a code path.
func (i Invitation) IsSignupInvite() bool { return i.Tenant == SignupInviteTenant }

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
// on token. The load/flush/atomic-write machinery lives in the embedded
// jsonFileStore.
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

// AnonymizeSubject replaces an erased person's email where it appears as the
// INVITER, returning the rows changed.
//
// The row belongs to somebody else — the person invited — and survives the
// inviter's erasure, so the identifier is pseudonymised rather than deleted.
// Probed by the erasure cascade rather than declared on the interface, matching
// how DeleteByEmail is already handled for this store.
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

// filter returns every invitation matching pred (read-locked).
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

// deleteWhere hard-deletes every invitation matching pred and flushes
// (write-locked), returning the number removed.
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

// ListByEmail returns every invitation addressed to an email (export).
func (s *JSONInvitationStore) ListByEmail(_ context.Context, email string) ([]Invitation, error) {
	return s.filter(emailMatches(email)), nil
}

// DeleteByEmail hard-deletes every invitation to an email (erasure).
func (s *JSONInvitationStore) DeleteByEmail(_ context.Context, email string) (int, error) {
	return s.deleteWhere(emailMatches(email))
}

// DeleteByTenant hard-deletes every invitation in a tenant (org deletion).
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
