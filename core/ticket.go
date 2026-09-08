// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// Both scoped to the filing org's tenant — that is the security boundary. Chat
// bodies are secret-scrubbed on ingest, because users WILL paste API keys, and
// the only flow data riding along is a redacted SupportBundle, so support can
// diagnose the common case without a live consented grant.

type TicketStatus string

const (
	TicketOpen            TicketStatus = "open"
	TicketAwaitingUser    TicketStatus = "awaiting_user"
	TicketAwaitingSupport TicketStatus = "awaiting_support"
	TicketResolved        TicketStatus = "resolved"
	TicketClosed          TicketStatus = "closed"
)

func (s TicketStatus) Valid() bool {
	switch s {
	case TicketOpen, TicketAwaitingUser, TicketAwaitingSupport, TicketResolved, TicketClosed:
		return true
	}
	return false
}

func (s TicketStatus) IsTerminal() bool {
	return s == TicketResolved || s == TicketClosed
}

type Ticket struct {
	ID         string       `json:"id"`
	Tenant     string       `json:"tenant"` // the org that filed it — scopes everything
	Workspace  string       `json:"workspace"`
	CreatedBy  string       `json:"created_by"` // principal subject
	Subject    string       `json:"subject"`
	Status     TicketStatus `json:"status"`
	FlowID     string       `json:"flow_id,omitempty"`     // optional — the flow this is about
	RunID      string       `json:"run_id,omitempty"`      // optional — the failing run
	BundleID   string       `json:"bundle_id,omitempty"`   // optional — attached SupportBundleRecord
	AssignedTo string       `json:"assigned_to,omitempty"` // optional — support agent subject
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`

	UserReadAt      time.Time `json:"user_read_at,omitempty"`
	SupportReadAt   time.Time `json:"support_read_at,omitempty"`
	UserNudgedAt    time.Time `json:"user_nudged_at,omitempty"`
	SupportNudgedAt time.Time `json:"support_nudged_at,omitempty"`
}

type AuthorKind string

const (
	AuthorUser    AuthorKind = "user"
	AuthorSupport AuthorKind = "support"
	AuthorSystem  AuthorKind = "system"
)

// Body is ALWAYS scrubbed before it is persisted.
type TicketMessage struct {
	ID         string     `json:"id"`
	TicketID   string     `json:"ticket_id"`
	Author     string     `json:"author,omitempty"` // subject; "" for system
	AuthorKind AuthorKind `json:"author_kind"`
	Body       string     `json:"body"`
	SystemCode SystemNote `json:"system_code,omitempty"`
	BundleID   string     `json:"bundle_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type SystemNote string

const (
	NoteCustomerClosed   SystemNote = "customer_closed"
	NoteCustomerReopened SystemNote = "customer_reopened"
	NoteGrantRequested   SystemNote = "grant_requested"
)

func MarkedNote(s TicketStatus) SystemNote { return SystemNote("marked_" + s) }

type TicketListOpts struct {
	Status     TicketStatus // "" = any status
	AssignedTo string       // "" = any assignee; else only this agent's tickets
	Unassigned bool         // true = only tickets no agent has claimed
	Limit      int          // 0 = store default
}

// The fields deliberately count two different sets: ByStatus and Total describe
// EVERY ticket ever filed, the rest only non-terminal ones, since a resolved
// ticket needs no owner and would drown the "needs a first responder" signal.
type TicketQueueSummary struct {
	ByStatus   map[TicketStatus]int `json:"by_status"`
	Total      int                  `json:"total"`
	Open       int                  `json:"open"`
	Unassigned int                  `json:"unassigned"`
	// Unassigned tickets are NOT keyed under "" here.
	ByAssignee map[string]int `json:"by_assignee"`
}

func NewTicketQueueSummary() TicketQueueSummary {
	return TicketQueueSummary{
		ByStatus: map[TicketStatus]int{
			TicketOpen: 0, TicketAwaitingUser: 0, TicketAwaitingSupport: 0,
			TicketResolved: 0, TicketClosed: 0,
		},
		ByAssignee: map[string]int{},
	}
}

// Both stores aggregate through it, so their counts can't drift.
func (s *TicketQueueSummary) Add(status TicketStatus, assignedTo string, n int) {
	s.ByStatus[status] += n
	s.Total += n
	if status.IsTerminal() {
		return
	}
	s.Open += n
	if assignedTo == "" {
		s.Unassigned += n
		return
	}
	s.ByAssignee[assignedTo] += n
}

type TicketStore interface {
	Create(ctx context.Context, t Ticket) error
	Get(ctx context.Context, id string) (Ticket, error)
	ListForTenant(ctx context.Context, tenant string, opts TicketListOpts) ([]Ticket, error)
	ListQueue(ctx context.Context, opts TicketListOpts) ([]Ticket, error)
	QueueSummary(ctx context.Context) (TicketQueueSummary, error)
	Update(ctx context.Context, t Ticket) error
	AppendMessage(ctx context.Context, m TicketMessage) error
	ListMessages(ctx context.Context, ticketID string) ([]TicketMessage, error)
}

// Shares knownSecretValue with the support-bundle safety net, so chat and
// bundles hold one definition of "a secret". Never rejects input, only redacts.
func ScrubSecrets(s string) string {
	return knownSecretValue.ReplaceAllString(s, redactedSecretMarker)
}
