// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// support_grant.go holds the AccessGrant — the trust-critical piece of the
// Support feature (see TODO-support-tickets.md). A grant is explicit org
// consent for ONE named support agent to view ONE flow, read-only, until a
// deadline. It is a capability, never tenant-crossing: possession of an active
// grant is the sole authority AuthorizeGraphSupportView accepts, and even then
// the agent sees only the redacted SupportBundle view.

// GrantStatus is the lifecycle state of an AccessGrant.
//
// State machine:
//
//	requested → approved | denied
//	approved  → revoked | expired
//
// A grant authorizes access only in the `approved` state, and only until
// ExpiresAt (see IsActive). `expired` is a lazy label — a still-`approved`
// grant past its deadline is already inactive without a status write.
type GrantStatus string

const (
	GrantRequested GrantStatus = "requested"
	GrantApproved  GrantStatus = "approved"
	GrantDenied    GrantStatus = "denied"
	GrantRevoked   GrantStatus = "revoked"
	GrantExpired   GrantStatus = "expired"
)

// AccessGrant records one consented, time-boxed, read-only view of a single
// flow by a single support agent. The (AgentSubject, Tenant, FlowID) tuple is
// the authority — the agent's own Principal.Tenant is irrelevant.
//
// Invariants enforced everywhere regardless of the grant: read-only, secrets
// masked, run data redacted. A grant unlocks freshness/navigation of the
// redacted view, never plaintext.
type AccessGrant struct {
	ID           string      `json:"id"`
	TicketID     string      `json:"ticket_id"`     // the reason/anchor for the request
	Tenant       string      `json:"tenant"`        // scope: the org that owns the flow
	FlowID       string      `json:"flow_id"`       // scope: ONE flow, not the account
	AgentSubject string      `json:"agent_subject"` // the SPECIFIC agent this grant is for
	Status       GrantStatus `json:"status"`
	RequestedAt  time.Time   `json:"requested_at"`
	RequestedBy  string      `json:"requested_by"`         // agent subject
	DecidedBy    string      `json:"decided_by,omitempty"` // org admin subject (approve/deny)
	DecidedAt    *time.Time  `json:"decided_at,omitempty"`
	ExpiresAt    time.Time   `json:"expires_at"` // time box; access auto-expires
	RevokedAt    *time.Time  `json:"revoked_at,omitempty"`
	RevokedBy    string      `json:"revoked_by,omitempty"`
}

// IsActive reports whether the grant authorizes access right now: it must be
// approved, not revoked, and strictly before its expiry. This is the single
// source of truth for "may support see this flow at this moment" — the expiry
// boundary is exclusive, so now == ExpiresAt is already inactive.
func (g AccessGrant) IsActive(now time.Time) bool {
	return g.Status == GrantApproved && g.RevokedAt == nil && now.Before(g.ExpiresAt)
}

// CanDecide reports whether a requested grant may be approved/denied — only a
// grant still in the `requested` state can be decided. Guards the store's
// Decide against double-deciding or deciding a revoked/expired grant.
func (g AccessGrant) CanDecide() bool {
	return g.Status == GrantRequested
}

// CanRevoke reports whether a grant may be revoked — only an approved grant has
// anything to revoke (denied/requested/already-terminal grants do not).
func (g AccessGrant) CanRevoke() bool {
	return g.Status == GrantApproved
}

// GrantStore persists AccessGrants. Implementations live in daemon/ (in-memory
// for tests, Postgres for prod), mirroring the JobStore / AuditLog pattern.
//
// (Deviations from the design sketch: Decide/Revoke take an explicit timestamp
// so the transition time is testable/deterministic rather than time.Now(); and
// ActiveGrant returns an error for the Postgres implementation's I/O.)
type GrantStore interface {
	// Create records a new grant (the request). The grant should be in the
	// requested state; a duplicate ID is an error.
	Create(ctx context.Context, g AccessGrant) error
	// Decide approves or denies a requested grant. status must be GrantApproved
	// or GrantDenied; the grant must satisfy CanDecide. `by` is the deciding org
	// admin's subject, stamped with `at`. On approval, expiresAt sets the grant's
	// time box (the caller computes now+TTL); it is ignored for a denial.
	Decide(ctx context.Context, id string, status GrantStatus, by string, at, expiresAt time.Time) error
	// Revoke ends an approved grant early. The grant must satisfy CanRevoke.
	Revoke(ctx context.Context, id, by string, at time.Time) error
	// Get returns the grant, or ErrNotFound.
	Get(ctx context.Context, id string) (AccessGrant, error)
	// ActiveGrant returns the active grant (if any) that authorizes `agent` to
	// view `flowID` in `tenant` at `now` — the lookup AuthorizeGraphSupportView
	// needs. ok is false when none is active.
	ActiveGrant(ctx context.Context, agent, tenant, flowID string, now time.Time) (grant AccessGrant, ok bool, err error)
	// ListForTenant returns every grant scoped to tenant (any status), for the
	// org's consent/audit surface.
	ListForTenant(ctx context.Context, tenant string) ([]AccessGrant, error)
}
