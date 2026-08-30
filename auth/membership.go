// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
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

// SeatLimitedMembershipStore is an optional MembershipStore capability: seat
// someone only if the tenant still has room, deciding and writing as one
// atomic step.
//
// It exists because "count the seats, then insert" has a window between the
// two. Two people accepting invitations in the same moment both read the last
// free seat and both take it, and the org ends up over its plan — which is
// the one thing a seat limit is for. Stores that can't do it atomically are
// still valid MembershipStores; the caller falls back to the check-then-write
// and accepts the window.
type SeatLimitedMembershipStore interface {
	// PutMembershipWithinLimit seats m unless that would take the tenant past
	// maxRows membership rows, and reports whether the seat was taken.
	//
	// Updating someone who already holds a row never counts against the limit:
	// they occupy a seat already, so a role change must not be refused because
	// the org is full.
	//
	// maxRows counts ROWS, not people — an owner holds a seat without a row
	// (ownership is implicit in the home tenant), so the caller subtracts that
	// implicit seat before calling.
	PutMembershipWithinLimit(ctx context.Context, m Membership, maxRows int) (seated bool, err error)
}

var ErrUnknownMembership = errors.New("unknown membership")
