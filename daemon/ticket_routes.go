// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// ticket_routes.go wires the Support ticket + chat surface. Two audiences
// share one TicketStore:
//
//   - The END USER (own tenant, PermGraphRun) files a ticket about a flow and
//     chats on it: POST/GET /api/v1/me/support/tickets[…]. On filing, a redacted
//     SupportBundle for the referenced flow/run is auto-built and attached — so
//     support can diagnose the common case WITHOUT a live read-only grant.
//   - The SUPPORT AGENT (PermSupportAgent) works the cross-tenant queue and
//     replies: GET/POST /api/v1/support/tickets[…].
//
// Trust rules enforced here: every chat body is secret-scrubbed on ingest
// (core.ScrubSecrets); a user only ever sees tickets in their own tenant; and
// the bundle attached is the redaction-by-construction one, never raw flow data.
//
// Role separation (Phase 3) runs in BOTH directions. Support can't reach a
// tenant's data without a grant, and the customer can't see the support
// organisation's internals: who a ticket is assigned to and which individual
// staff member replied are stripped from every user-facing response
// (ticketForUser / messagesForUser), because the customer's channel for "what
// did support do" is their own audit log, not the support team's rota. Status
// changes are split too: only support can declare a ticket resolved.

// ticketsEnabled reports whether the ticket store is wired; endpoints 501 when
// not (a deployment with no support surface).
func (h *HTTPGateway) ticketsEnabled() bool { return h.Tickets != nil }

// maxTicketBodyLen bounds a single subject/message so a paste can't balloon the
// store. Generous — a stack trace or a paragraph fits.
const maxTicketBodyLen = 16 * 1024

// ticketView is the wire shape for a single ticket plus its thread.
type ticketView struct {
	Ticket   core.Ticket          `json:"ticket"`
	Messages []core.TicketMessage `json:"messages"`
}

// ---- End-user surface ------------------------------------------------------

// createTicket: an org member files a support ticket, optionally about a flow /
// failed run. POST /api/v1/me/support/tickets
// Body: {subject, flow_id?, run_id?, message?}
func (h *HTTPGateway) createTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if err := core.Require(p, core.PermGraphRun); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "you don't have access to file a ticket")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "no tenant in context")
		return
	}
	// Before decoding: filing is the endpoint that persists a bundle per call.
	if !h.allowSupportWrite(rw, p) {
		return
	}
	var body struct {
		Subject string `json:"subject"`
		FlowID  string `json:"flow_id"`
		RunID   string `json:"run_id"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	subject := strings.TrimSpace(body.Subject)
	if subject == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "a subject is required")
		return
	}
	subject = clampTicketText(subject)

	id, err := newID()
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	now := h.supportTime()

	// Auto-attach a redacted diagnostic bundle for the referenced flow. Best
	// effort: a bad/foreign flow id just means no bundle, never a failed filing.
	bundleID := ""
	if body.FlowID != "" {
		bundleID = h.buildAndStoreBundle(r.Context(), p, body.FlowID, body.RunID, now)
	}

	t := core.Ticket{
		ID:        id,
		Tenant:    p.Tenant,
		Workspace: p.Workspace,
		CreatedBy: p.Subject,
		Subject:   subject,
		Status:    core.TicketAwaitingSupport, // filed → support's turn
		FlowID:    body.FlowID,
		RunID:     body.RunID,
		BundleID:  bundleID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.Tickets.Create(r.Context(), t); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// The opening message (optional) rides along as the first user post.
	if msg := strings.TrimSpace(body.Message); msg != "" {
		_ = h.appendTicketMessage(r.Context(), t.ID, p.Subject, core.AuthorUser, msg, "", now)
	}
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.create", t.FlowID, "ticket="+t.ID)
	h.notifyTicketFiled(t)
	writeJSON(rw, http.StatusCreated, ticketForUser(t))
}

// listMyTickets: the org member's own ticket list. GET /api/v1/me/support/tickets
func (h *HTTPGateway) listMyTickets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	// Ownership filters are support-side only — assignment isn't part of the
	// customer's view — so the user list takes status + limit and nothing else.
	tickets, err := h.Tickets.ListForTenant(r.Context(), p.Tenant, core.TicketListOpts{
		Status: core.TicketStatus(r.URL.Query().Get("status")),
		Limit:  ticketQueryLimit(r),
	})
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	for i := range tickets {
		tickets[i] = ticketForUser(tickets[i])
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tickets": tickets})
}

// getMyTicket: one ticket + thread, scoped to the caller's tenant.
// GET /api/v1/me/support/tickets/{id}
func (h *HTTPGateway) getMyTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	h.writeUserTicketView(rw, r, t)
}

// postMyTicketMessage: an org member replies on their own ticket.
// POST /api/v1/me/support/tickets/{id}/messages  {message}
func (h *HTTPGateway) postMyTicketMessage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	if !h.allowSupportWrite(rw, p) {
		return
	}
	msg, ok := decodeTicketMessageBody(rw, r)
	if !ok {
		return
	}
	now := h.supportTime()
	if err := h.appendTicketMessage(r.Context(), t.ID, p.Subject, core.AuthorUser, msg, "", now); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// A user reply hands the ball back to support (and reopens a resolved ticket).
	t.Status = core.TicketAwaitingSupport
	t.UpdatedAt = now
	_ = h.Tickets.Update(r.Context(), t)
	h.notifyUserReplied(t)
	h.writeUserTicketView(rw, r, t)
}

// setMyTicketStatus: the requester closes their own ticket (they sorted it out,
// or it no longer matters) or reopens a finished one.
// POST /api/v1/me/support/tickets/{id}/status  {status}
//
// The split with the support-side handler is deliberate role separation: the
// requester may withdraw ("closed") or reopen their ticket, but cannot declare
// it "resolved" — that is support's verdict on the problem, and a customer
// stamping it would corrupt the queue's own resolution record.
func (h *HTTPGateway) setMyTicketStatus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	status, ok := decodeTicketStatusBody(rw, r)
	if !ok {
		return
	}
	if status != core.TicketClosed && status != core.TicketAwaitingSupport {
		writeAPIError(rw, http.StatusBadRequest, "bad_request",
			"you can close your own ticket or reopen it; only support can mark it resolved")
		return
	}
	if status == t.Status { // no-op: don't bump activity or narrate a non-change
		h.writeUserTicketView(rw, r, t)
		return
	}
	// Narrate only what actually happened. Handing a live ticket back to support
	// isn't a "reopen" and needs no note — the reply beside it says everything.
	note, code := "The customer closed this ticket.", core.NoteCustomerClosed
	if status != core.TicketClosed {
		note, code = "", core.SystemNote("")
		if t.Status.IsTerminal() {
			note, code = "The customer reopened this ticket.", core.NoteCustomerReopened
		}
	}
	now := h.supportTime()
	t.Status = status
	t.UpdatedAt = now
	if err := h.Tickets.Update(r.Context(), t); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// appendSystemNote skips an empty body, so the no-note case is a no-op.
	_ = h.appendSystemNote(r.Context(), t.ID, code, note, now)
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.status", t.FlowID, "ticket="+t.ID+" status="+string(status))
	h.writeUserTicketView(rw, r, t)
}

// markMyTicketRead records that the customer has opened this thread.
// POST /api/v1/me/support/tickets/{id}/read
//
// An explicit call rather than a side effect of GET. The thread polls while it
// is open and other surfaces prefetch, so "we fetched it" is not "a person
// looked at it" — and the read receipt is what decides whether they get a
// reminder, so a false positive here means silence when someone is waiting.
func (h *HTTPGateway) markMyTicketRead(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	h.markTicketRead(r.Context(), t, NudgeUser)
	h.writeUserTicketView(rw, r, t)
}

// markSupportTicketRead is the agent-side counterpart.
// POST /api/v1/support/tickets/{id}/read
func (h *HTTPGateway) markSupportTicketRead(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	h.markTicketRead(r.Context(), t, NudgeSupport)
	h.writeTicketView(rw, r, t)
}

// markTicketRead stamps one side's read receipt. Deliberately does NOT touch
// UpdatedAt: that field orders the queue by activity, and reading is not
// activity — letting it bump would float every ticket an agent merely glanced
// at to the top of the list, above ones actually waiting.
//
// Best-effort. Failing to record a read costs at worst one extra reminder,
// which is a far better outcome than failing the request that renders the
// thread the person is trying to read.
func (h *HTTPGateway) markTicketRead(ctx context.Context, t core.Ticket, side NudgeSide) {
	now := h.supportTime()
	if side == NudgeUser {
		t.UserReadAt = now
	} else {
		t.SupportReadAt = now
	}
	_ = h.Tickets.Update(ctx, t)
}

// ---- Support surface -------------------------------------------------------

// listTicketQueue: the cross-tenant support queue. GET /api/v1/support/tickets
func (h *HTTPGateway) listTicketQueue(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if err := core.Require(p, core.PermSupportAgent); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "support agent role required")
		return
	}
	tickets, err := h.Tickets.ListQueue(r.Context(), queueListOpts(r, p))
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tickets": tickets})
}

// ticketQueueSummary: the support dashboard's headline counts over the whole
// cross-org queue. GET /api/v1/support/tickets/summary
//
// Separate from the listing because it must NOT be bounded by the list limit — a
// tile reading "12 unassigned" because page one happens to hold 12 would be a lie
// on a queue of 300.
func (h *HTTPGateway) ticketQueueSummary(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	if err := core.Require(p, core.PermSupportAgent); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "support agent role required")
		return
	}
	sum, err := h.Tickets.QueueSummary(r.Context())
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// "mine" saves the dashboard from having to know its own subject.
	writeJSON(rw, http.StatusOK, map[string]any{
		"summary": sum,
		"mine":    sum.ByAssignee[p.Subject],
	})
}

// assignSupportTicket: an agent claims a ticket, hands it to a colleague, or
// releases it back to the unassigned pool.
// POST /api/v1/support/tickets/{id}/assign  {assignee}
//
// assignee: "me" (or the caller's own subject) claims it, "" releases it, and any
// other value must be a PROVISIONED support agent — assignment is not a way to
// name an arbitrary principal as support staff. Reassignment away from another
// agent is allowed (teams hand work over) and audited into the org's log like
// every other support action.
func (h *HTTPGateway) assignSupportTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	var body struct {
		Assignee string `json:"assignee"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	assignee := strings.TrimSpace(body.Assignee)
	if assignee == "me" {
		assignee = p.Subject
	}
	if assignee != "" && assignee != p.Subject && !h.isProvisionedSupportAgent(assignee) {
		writeAPIError(rw, http.StatusBadRequest, "not_support_agent",
			"that person isn't a provisioned support agent")
		return
	}
	if assignee == t.AssignedTo { // nothing to do; don't bump activity or audit
		h.writeTicketView(rw, r, t)
		return
	}
	now := h.supportTime()
	t.AssignedTo = assignee
	t.UpdatedAt = now
	if err := h.Tickets.Update(r.Context(), t); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Deliberately NO system note in the thread: who on the support side owns a
	// ticket is internal, and a note would leak staff identities to the customer
	// (see ticketForUser). The org still sees the action in its audit log.
	detail := "ticket=" + t.ID + " assignee=" + assignee
	if assignee == "" {
		detail = "ticket=" + t.ID + " unassigned"
	}
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.assign", t.FlowID, detail)
	h.writeTicketView(rw, r, t)
}

// isProvisionedSupportAgent reports whether subject currently holds a runtime
// support-agent grant. Session subjects are the user's email (see signup / SSO),
// which is exactly what support.AgentStore is keyed on. With no store wired
// (single-node / tests) there is nothing to check against, so any subject is
// accepted — the caller already had to hold PermSupportAgent to get here.
func (h *HTTPGateway) isProvisionedSupportAgent(subject string) bool {
	if h.SupportAgents == nil {
		return true
	}
	return h.SupportAgents.Granted(subject)
}

// getSupportTicket: a support agent reads any ticket + thread.
// GET /api/v1/support/tickets/{id}
func (h *HTTPGateway) getSupportTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	h.writeTicketView(rw, r, t)
}

// postSupportTicketMessage: a support agent replies (and self-assigns).
// POST /api/v1/support/tickets/{id}/messages  {message}
func (h *HTTPGateway) postSupportTicketMessage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	if !h.allowSupportWrite(rw, p) {
		return
	}
	msg, ok := decodeTicketMessageBody(rw, r)
	if !ok {
		return
	}
	now := h.supportTime()
	if err := h.appendTicketMessage(r.Context(), t.ID, p.Subject, core.AuthorSupport, msg, "", now); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// First responder claims the ticket; a support reply awaits the user.
	if t.AssignedTo == "" {
		t.AssignedTo = p.Subject
	}
	t.Status = core.TicketAwaitingUser
	t.UpdatedAt = now
	_ = h.Tickets.Update(r.Context(), t)
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.reply", t.FlowID, "ticket="+t.ID)
	h.notifySupportReplied(t)
	h.writeTicketView(rw, r, t)
}

// setSupportTicketStatus: a support agent resolves/closes/reopens a ticket.
// POST /api/v1/support/tickets/{id}/status  {status}
func (h *HTTPGateway) setSupportTicketStatus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	status, ok := decodeTicketStatusBody(rw, r)
	if !ok {
		return
	}
	// No-op guard, mirroring setMyTicketStatus: a double-clicked button or a
	// retried request must not re-narrate the change, re-bump activity, or
	// (worse) re-email the customer that their ticket was resolved.
	if status == t.Status {
		h.writeTicketView(rw, r, t)
		return
	}
	now := h.supportTime()
	t.Status = status
	t.UpdatedAt = now
	if t.AssignedTo == "" {
		t.AssignedTo = p.Subject
	}
	if err := h.Tickets.Update(r.Context(), t); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Leave a system note in the thread so the user sees the state change.
	_ = h.appendSystemNote(r.Context(), t.ID, core.MarkedNote(status),
		"Ticket marked "+string(status)+".", now)
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.status", t.FlowID, "ticket="+t.ID+" status="+string(status))
	// Only the resolved edge is worth an email — "closed" is the customer's own
	// action, and awaiting_* flips constantly as the thread goes back and forth.
	if status == core.TicketResolved {
		h.notifyTicketResolved(t)
	}
	h.writeTicketView(rw, r, t)
}

// ---- Shared helpers --------------------------------------------------------

// loadTicketForTenant loads {id} and enforces it belongs to `tenant`. A
// cross-tenant id 404s (never reveal another org's ticket exists).
func (h *HTTPGateway) loadTicketForTenant(rw http.ResponseWriter, r *http.Request, tenant string) (core.Ticket, bool) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return core.Ticket{}, false
	}
	t, err := h.Tickets.Get(r.Context(), r.PathValue("id"))
	if err != nil || t.Tenant != tenant {
		writeAPIError(rw, http.StatusNotFound, "ticket_not_found", "no ticket with that id")
		return core.Ticket{}, false
	}
	return t, true
}

// loadTicketForAgent loads {id} and enforces PermSupportAgent (cross-tenant).
func (h *HTTPGateway) loadTicketForAgent(rw http.ResponseWriter, r *http.Request, p core.Principal) (core.Ticket, bool) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return core.Ticket{}, false
	}
	if err := core.Require(p, core.PermSupportAgent); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "support agent role required")
		return core.Ticket{}, false
	}
	t, err := h.Tickets.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "ticket_not_found", "no ticket with that id")
		return core.Ticket{}, false
	}
	return t, true
}

// getMyTicketBundle returns the redacted diagnostic bundle attached to one of
// the caller's own tickets. GET /api/v1/me/support/tickets/{id}/bundle
func (h *HTTPGateway) getMyTicketBundle(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	h.writeTicketBundle(rw, r, t)
}

// getSupportTicketBundle returns the redacted diagnostic bundle for any ticket
// (support agents). GET /api/v1/support/tickets/{id}/bundle
func (h *HTTPGateway) getSupportTicketBundle(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	h.writeTicketBundle(rw, r, t)
}

// writeTicketBundle streams the attached SupportBundleRecord's payload verbatim.
// The payload is a redacted-by-construction SupportBundle (no secrets, no run
// data) so it is safe to serve to both the ticket owner and support. 404 when no
// bundle is attached or bundles aren't wired.
func (h *HTTPGateway) writeTicketBundle(rw http.ResponseWriter, r *http.Request, t core.Ticket) {
	if h.Bundles == nil || t.BundleID == "" {
		writeAPIError(rw, http.StatusNotFound, "no_bundle", "no diagnostic bundle attached to this ticket")
		return
	}
	rec, err := h.Bundles.Get(r.Context(), t.BundleID)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "no_bundle", "no diagnostic bundle attached to this ticket")
		return
	}
	// Payload is stored JSON of a redacted SupportBundle — write it byte-for-byte
	// rather than re-marshalling, so what support reads is exactly what was
	// persisted.
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(rec.Payload)
}

// writeTicketView returns a ticket plus its (chronological) thread.
func (h *HTTPGateway) writeTicketView(rw http.ResponseWriter, r *http.Request, t core.Ticket) {
	msgs, err := h.Tickets.ListMessages(r.Context(), t.ID)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, ticketView{Ticket: t, Messages: msgs})
}

// queueListOpts parses the support queue's filters: ?status=, ?assignee= (with
// "me" resolving to the caller so the dashboard needn't know its own subject),
// ?unassigned=true, and ?limit=.
func queueListOpts(r *http.Request, p core.Principal) core.TicketListOpts {
	q := r.URL.Query()
	assignee := strings.TrimSpace(q.Get("assignee"))
	if assignee == "me" {
		assignee = p.Subject
	}
	return core.TicketListOpts{
		Status:     core.TicketStatus(q.Get("status")),
		AssignedTo: assignee,
		Unassigned: q.Get("unassigned") == "true",
		Limit:      ticketQueryLimit(r),
	}
}

// ticketQueryLimit reads ?limit=, clamped to the store default. A junk or absent
// value means "store default" rather than an error — a listing is not the place
// to fail a request over a query string.
func ticketQueryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > support.DefaultTicketListLimit {
		return 0
	}
	return n
}

// decodeTicketStatusBody reads + validates a {status} body against the core
// status set, writing the error on failure. Per-role restrictions on WHICH status
// may be set are the caller's business (see setMyTicketStatus).
func decodeTicketStatusBody(rw http.ResponseWriter, r *http.Request) (core.TicketStatus, bool) {
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return "", false
	}
	status := core.TicketStatus(body.Status)
	if !status.Valid() {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "unknown ticket status")
		return "", false
	}
	return status, true
}

// ticketForUser strips the support organisation's internals from a ticket before
// it is served to the customer. Today that is AssignedTo: which staff member owns
// a ticket is the support team's rota, not the customer's business, and the field
// carries a support agent's email. Returns a copy — callers must never persist
// the result.
func ticketForUser(t core.Ticket) core.Ticket {
	t.AssignedTo = ""
	// The support side's read receipt and reminder clock are its own business.
	// "Support opened your ticket 3 days ago and said nothing" is a true and
	// unhelpful thing to hand a customer, and the reminder timestamps say more
	// about the desk's staffing than about the ticket.
	t.SupportReadAt = time.Time{}
	t.SupportNudgedAt = time.Time{}
	return t
}

// messagesForUser blanks the author of support-written messages: the customer
// sees "Support", not the individual who happened to pick the ticket up. User and
// system messages are untouched (the user's own name is theirs to see, and system
// notes have no author). Returns a copy of the slice.
func messagesForUser(msgs []core.TicketMessage) []core.TicketMessage {
	out := make([]core.TicketMessage, len(msgs))
	copy(out, msgs)
	for i := range out {
		if out[i].AuthorKind == core.AuthorSupport {
			out[i].Author = ""
		}
	}
	return out
}

// writeUserTicketView returns a ticket + thread with the support organisation's
// internals stripped (the end-user surface). The support surface uses
// writeTicketView, which serves the record as stored.
func (h *HTTPGateway) writeUserTicketView(rw http.ResponseWriter, r *http.Request, t core.Ticket) {
	msgs, err := h.Tickets.ListMessages(r.Context(), t.ID)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, ticketView{
		Ticket:   ticketForUser(t),
		Messages: messagesForUser(msgs),
	})
}

// decodeTicketMessageBody reads + validates a {message} body, writing the error
// on failure.
func decodeTicketMessageBody(rw http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return "", false
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "a message is required")
		return "", false
	}
	return msg, true
}

// appendTicketMessage scrubs the body for pasted secrets, clamps its length, and
// persists it. Callers pass "" for author on system messages. It never appends
// an empty (post-scrub/clamp) body.
func (h *HTTPGateway) appendTicketMessage(ctx context.Context, ticketID, author string, kind core.AuthorKind, body, bundleID string, now time.Time) error {
	scrubbed := clampTicketText(core.ScrubSecrets(body))
	if strings.TrimSpace(scrubbed) == "" {
		return nil
	}
	id, err := newID()
	if err != nil {
		return err
	}
	return h.Tickets.AppendMessage(ctx, core.TicketMessage{
		ID:         id,
		TicketID:   ticketID,
		Author:     author,
		AuthorKind: kind,
		Body:       scrubbed,
		BundleID:   bundleID,
		CreatedAt:  now,
	})
}

// appendSystemNote records a machine-generated thread note: the English prose
// in Body for API readers and email digests, and the code the web needs to say
// the same thing in the reader's language. Kept separate from
// appendTicketMessage because a code only ever belongs to a system note — a
// person's message has an author instead.
func (h *HTTPGateway) appendSystemNote(ctx context.Context, ticketID string, code core.SystemNote, body string, now time.Time) error {
	scrubbed := clampTicketText(core.ScrubSecrets(body))
	if strings.TrimSpace(scrubbed) == "" {
		return nil
	}
	id, err := newID()
	if err != nil {
		return err
	}
	return h.Tickets.AppendMessage(ctx, core.TicketMessage{
		ID:         id,
		TicketID:   ticketID,
		AuthorKind: core.AuthorSystem,
		Body:       scrubbed,
		SystemCode: code,
		CreatedAt:  now,
	})
}

// buildAndStoreBundle builds a redacted SupportBundle for one of the caller's own
// flows (+ optional run) and persists it, returning the new bundle id. Returns ""
// (no attachment) when bundles are disabled, the flow can't be loaded, or storing
// fails — filing a ticket must never hinge on the diagnostic attachment.
func (h *HTTPGateway) buildAndStoreBundle(ctx context.Context, p core.Principal, flowID, runID string, now time.Time) string {
	if h.Bundles == nil {
		return ""
	}
	graph, err := h.svc.LoadGraphForSupport(ctx, p.Tenant, p.Workspace, flowID)
	if err != nil || graph.Tenant != p.Tenant {
		return ""
	}
	var runPtr *core.RunSnapshot
	if runID != "" {
		if rs, ok := h.supportRunSnapshot(ctx, p.Tenant, p.Workspace, flowID, runID); ok {
			runPtr = &rs
		}
	}
	manifests := h.svc.manifestsSnapshot(p.Tenant)
	// ValidateGraphFull already includes LintGraph's findings (see
	// core/validate.go), so it's the complete set — appending LintGraph again
	// double-counts every lint issue.
	issues := core.ValidateGraphFull(graph, manifests)
	bundle := core.BuildSupportBundle(graph, runPtr, issues, core.RedactStructureOnly)
	id, err := newID()
	if err != nil {
		return ""
	}
	rec, err := core.NewSupportBundleRecord(id, p.Subject, now, bundle)
	if err != nil {
		return ""
	}
	if err := h.Bundles.Create(ctx, rec); err != nil {
		return ""
	}
	return id
}

// clampTicketText trims a subject/message to maxTicketBodyLen so a runaway paste
// can't bloat the store. It cuts on a rune boundary to avoid splitting UTF-8.
func clampTicketText(s string) string {
	if len(s) <= maxTicketBodyLen {
		return s
	}
	cut := maxTicketBodyLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
