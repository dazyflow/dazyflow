// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	// Read receipts, one per side of the thread: when that side last opened it.
	// Zero means never. Written by the mark-read endpoints, read by the nudge
	// sweeper to tell "hasn't answered yet" from "hasn't even looked".
	UserReadAt    time.Time `json:"user_read_at,omitempty"`
	SupportReadAt time.Time `json:"support_read_at,omitempty"`
	// When each side was last emailed a reminder about a message it had not
	// read. Compared against the message's own timestamp so a reminder is sent
	// once per waiting period rather than on every sweep: a thread that mails
	// repeatedly is one people filter.
	UserNudgedAt    time.Time `json:"user_nudged_at,omitempty"`
	SupportNudgedAt time.Time `json:"support_nudged_at,omitempty"`
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
	// SystemCode names WHICH system note this is, for AuthorSystem messages
	// only ("" on anything a person wrote).
	//
	// The daemon composes these notes in English and stores the prose in Body,
	// which is right for an API reader, an email digest or a support agent
	// grepping the table — and wrong for the web UI, which is translated and
	// was rendering "The customer closed this ticket." in the middle of a
	// Swedish thread. Prose cannot be translated after the fact without
	// matching on English sentences, which is the fragile thing this field
	// exists to avoid.
	//
	// One code per distinct sentence, no parameters: "marked resolved" and
	// "marked closed" are separate codes rather than one code plus a status
	// argument. Interpolating a status label into a sentence is where
	// translations go wrong — Swedish inflects around it — and a flat code is
	// also greppable from either side of the stack.
	//
	// Body stays populated, and a client that does not recognise a code (an
	// older UI, a newer daemon, or any row written before this field existed)
	// falls back to it. So the English is the floor, never the ceiling.
	SystemCode SystemNote `json:"system_code,omitempty"`
	BundleID   string     `json:"bundle_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// SystemNote identifies a machine-generated thread note. See
// TicketMessage.SystemCode for why these are codes rather than prose.
type SystemNote string

const (
	NoteCustomerClosed   SystemNote = "customer_closed"
	NoteCustomerReopened SystemNote = "customer_reopened"
	NoteGrantRequested   SystemNote = "grant_requested"
)

// MarkedNote is the note for "an agent moved this ticket to <status>". One
// code per status, so the web has one whole sentence to translate per case.
func MarkedNote(s TicketStatus) SystemNote { return SystemNote("marked_" + s) }

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
