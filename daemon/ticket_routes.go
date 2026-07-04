// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ticket_routes.go wires the Support ticket + chat surface (Phase 2 of
// TODO-support-tickets.md). Two audiences share one TicketStore:
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
	writeJSON(rw, http.StatusCreated, t)
}

// listMyTickets: the org member's own ticket list. GET /api/v1/me/support/tickets
func (h *HTTPGateway) listMyTickets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
	tickets, err := h.Tickets.ListForTenant(r.Context(), p.Tenant, core.TicketListOpts{
		Status: core.TicketStatus(r.URL.Query().Get("status")),
	})
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
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
	h.writeTicketView(rw, r, t)
}

// postMyTicketMessage: an org member replies on their own ticket.
// POST /api/v1/me/support/tickets/{id}/messages  {message}
func (h *HTTPGateway) postMyTicketMessage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
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
	h.writeTicketView(rw, r, t)
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
	tickets, err := h.Tickets.ListQueue(r.Context(), core.TicketListOpts{
		Status: core.TicketStatus(r.URL.Query().Get("status")),
	})
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"tickets": tickets})
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
	h.writeTicketView(rw, r, t)
}

// setSupportTicketStatus: a support agent resolves/closes/reopens a ticket.
// POST /api/v1/support/tickets/{id}/status  {status}
func (h *HTTPGateway) setSupportTicketStatus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	status := core.TicketStatus(body.Status)
	if !status.Valid() {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", "unknown ticket status")
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
	_ = h.appendTicketMessage(r.Context(), t.ID, "", core.AuthorSystem,
		"Ticket marked "+string(status)+".", "", now)
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.status", t.FlowID, "ticket="+t.ID+" status="+string(status))
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
	manifests := h.svc.manifestsSnapshot()
	issues := append(core.ValidateGraphFull(graph, manifests), core.LintGraph(graph)...)
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
