// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// Explicit org consent for ONE named support agent to view ONE flow, read-only,
// until a deadline. Possession of an active grant is the sole authority
// AuthorizeGraphSupportView accepts, and even then the agent sees only the
// redacted SupportBundle view.

type GrantStatus string

const (
	GrantRequested GrantStatus = "requested"
	GrantApproved  GrantStatus = "approved"
	GrantDenied    GrantStatus = "denied"
	GrantRevoked   GrantStatus = "revoked"
	GrantExpired   GrantStatus = "expired"
)

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

func (g AccessGrant) IsActive(now time.Time) bool {
	return g.Status == GrantApproved && g.RevokedAt == nil && now.Before(g.ExpiresAt)
}

// Guards Decide against double-deciding, or deciding a revoked or expired grant.
func (g AccessGrant) CanDecide() bool {
	return g.Status == GrantRequested
}

func (g AccessGrant) CanRevoke() bool {
	return g.Status == GrantApproved
}

type GrantStore interface {
	Create(ctx context.Context, g AccessGrant) error
	Decide(ctx context.Context, id string, status GrantStatus, by string, at, expiresAt time.Time) error
	Revoke(ctx context.Context, id, by string, at time.Time) error
	Get(ctx context.Context, id string) (AccessGrant, error)
	ActiveGrant(ctx context.Context, agent, tenant, flowID string, now time.Time) (grant AccessGrant, ok bool, err error)
	ListForTenant(ctx context.Context, tenant string) ([]AccessGrant, error)
	ListForAgent(ctx context.Context, agent string) ([]AccessGrant, error)
}
