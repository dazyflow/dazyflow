// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"context"
	"time"
)

// ticket.go holds the Support ticket + chat model — Phase 2 of
// docs/support-tickets-design.md. An org files a Ticket about a flow; a thread
// of TicketMessages carries the conversation between the user and support. Both
// are scoped to the filing org's tenant (that is the security boundary).
//
// Two trust properties live here, mirroring the rest of the Support feature:
//   - Chat bodies are secret-scrubbed on ingest (users WILL paste API keys) via
//     ScrubSecrets, sharing one detector with the support-bundle safety net.
//   - The only flow data that ever rides along is a redacted SupportBundle
//     (referenced by BundleID) — so support can diagnose the common case WITHOUT
//     a live, consented read-only grant. The grant flow stays the exception.
//
// Pure types + the store interface only; the in-memory and Postgres
// implementations live in daemon/ (mirroring GrantStore / JobStore / AuditLog).

// TicketStatus is the lifecycle state of a support Ticket.
//
// Typical flow: a freshly filed ticket is open; replies flip it between
// awaiting_support (the user said something, support's turn) and awaiting_user
// (support asked for something). resolved/closed are terminal.
type TicketStatus string

const (
	TicketOpen            TicketStatus = "open"
	TicketAwaitingUser    TicketStatus = "awaiting_user"
	TicketAwaitingSupport TicketStatus = "awaiting_support"
	TicketResolved        TicketStatus = "resolved"
	TicketClosed          TicketStatus = "closed"
)

// Valid reports whether s is a known status (guards untrusted status writes from
// the API).
func (s TicketStatus) Valid() bool {
	switch s {
	case TicketOpen, TicketAwaitingUser, TicketAwaitingSupport, TicketResolved, TicketClosed:
		return true
	}
	return false
}

// IsTerminal reports whether the ticket is finished (resolved or closed) — no
// further action is expected, and new messages should reopen it.
func (s TicketStatus) IsTerminal() bool {
	return s == TicketResolved || s == TicketClosed
}

// Ticket is one support request, scoped to the org (Tenant) that filed it. FlowID
// / RunID / BundleID are optional context: the flow the ticket is about, the run
// that failed, and the redacted diagnostic bundle attached at filing time.
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
}

// AuthorKind distinguishes who wrote a TicketMessage. "system" (empty Author) is
// for machine-generated notes like "support was granted read-only access".
type AuthorKind string

const (
	AuthorUser    AuthorKind = "user"
	AuthorSupport AuthorKind = "support"
	AuthorSystem  AuthorKind = "system"
)

// TicketMessage is one entry in a ticket's chat thread. Body is ALWAYS
// secret-scrubbed (ScrubSecrets) before it is persisted, so a pasted key never
// lands in the store. BundleID optionally attaches a redacted bundle to a message.
type TicketMessage struct {
	ID         string     `json:"id"`
	TicketID   string     `json:"ticket_id"`
	Author     string     `json:"author,omitempty"` // subject; "" for system
	AuthorKind AuthorKind `json:"author_kind"`
	Body       string     `json:"body"`
	BundleID   string     `json:"bundle_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// TicketListOpts scopes and bounds a ticket listing. The zero value ("any
// status, any assignee, default limit") is the common case.
//
// AssignedTo and Unassigned are the two halves of the support dashboard's
// ownership filter and are mutually exclusive; when both are set, Unassigned
// wins (it is the narrower "nobody has this" question).
type TicketListOpts struct {
	Status     TicketStatus // "" = any status
	AssignedTo string       // "" = any assignee; else only this agent's tickets
	Unassigned bool         // true = only tickets no agent has claimed
	Limit      int          // 0 = store default
}

// TicketQueueSummary is the support dashboard's headline counts over the whole
// cross-org queue — the numbers an agent needs before reading a single ticket.
//
// Two different questions are being answered, so the fields deliberately count
// two different sets. ByStatus/Total describe EVERY ticket ever filed (they
// label the status filter). Open/Unassigned/ByAssignee describe only
// non-terminal tickets — live work — because a resolved ticket needs no owner
// and would otherwise drown the "needs a first responder" signal.
type TicketQueueSummary struct {
	// ByStatus counts every ticket by status. All five statuses are always
	// present (zero when empty) so the UI can render a stable set of options.
	ByStatus map[TicketStatus]int `json:"by_status"`
	// Total is every ticket in the store, terminal ones included.
	Total int `json:"total"`
	// Open is every ticket in a non-terminal status — the actual workload.
	Open int `json:"open"`
	// Unassigned counts non-terminal tickets no agent has claimed.
	Unassigned int `json:"unassigned"`
	// ByAssignee counts non-terminal tickets per agent subject (claimed work per
	// agent). Unassigned tickets are NOT keyed under "" here — they're Unassigned.
	ByAssignee map[string]int `json:"by_assignee"`
}

// NewTicketQueueSummary returns a zeroed summary with every status key present,
// so both store implementations emit the same stable shape.
func NewTicketQueueSummary() TicketQueueSummary {
	return TicketQueueSummary{
		ByStatus: map[TicketStatus]int{
			TicketOpen: 0, TicketAwaitingUser: 0, TicketAwaitingSupport: 0,
			TicketResolved: 0, TicketClosed: 0,
		},
		ByAssignee: map[string]int{},
	}
}

// Add folds one ticket into the summary, applying the terminal/non-terminal
// split described on the type. Both store implementations aggregate through it
// so their counts can't drift.
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

// TicketStore persists tickets and their chat threads. Implementations live in
// daemon/ (in-memory for tests + single-node, Postgres for prod), the same way
// GrantStore / JobStore have both.
type TicketStore interface {
	// Create records a new ticket; a duplicate ID is an error.
	Create(ctx context.Context, t Ticket) error
	// Get returns the ticket, or ErrNotFound.
	Get(ctx context.Context, id string) (Ticket, error)
	// ListForTenant returns tickets filed by one org (the user-facing list),
	// newest first, filtered/bounded by opts.
	ListForTenant(ctx context.Context, tenant string, opts TicketListOpts) ([]Ticket, error)
	// ListQueue returns tickets across all tenants (the cross-org support queue),
	// newest first, filtered/bounded by opts.
	ListQueue(ctx context.Context, opts TicketListOpts) ([]Ticket, error)
	// QueueSummary returns headline counts over the whole cross-org queue,
	// unbounded by any list limit (the support dashboard's stat tiles).
	QueueSummary(ctx context.Context) (TicketQueueSummary, error)
	// Update writes back a ticket's mutable fields (status, assignment, bundle,
	// updated_at). The ticket must exist.
	Update(ctx context.Context, t Ticket) error
	// AppendMessage adds a message to a ticket's thread; a duplicate ID is an
	// error. Body should already be scrubbed by the caller.
	AppendMessage(ctx context.Context, m TicketMessage) error
	// ListMessages returns a ticket's thread in chronological order.
	ListMessages(ctx context.Context, ticketID string) ([]TicketMessage, error)
}

// ScrubSecrets replaces any well-known secret pattern (Stripe / GitHub / Slack /
// AWS / Google keys, PEM private-key blocks) found anywhere in s with a redaction
// marker. It is used to sanitise free text a user might paste into a support chat
// before it is persisted or displayed. It reuses the same detector the
// support-bundle safety net uses (knownSecretValue), so chat and bundles share
// one definition of "a secret". It never rejects input — it only redacts.
func ScrubSecrets(s string) string {
	return knownSecretValue.ReplaceAllString(s, redactedSecretMarker)
}
