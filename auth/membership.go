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

var ErrUnknownMembership = errors.New("unknown membership")
