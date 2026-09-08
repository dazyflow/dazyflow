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

// Two audiences on one store: an org sees only its own tickets, an agent sees the
// cross-org queue. Every handler has to say which it is serving, because the same
// row means different things to the two.

func (h *supportAPI) ticketsEnabled() bool { return h.Tickets != nil }

const maxTicketBodyLen = 16 * 1024

type ticketView struct {
	Ticket   core.Ticket          `json:"ticket"`
	Messages []core.TicketMessage `json:"messages"`
}

func (h *supportAPI) createTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	if msg := strings.TrimSpace(body.Message); msg != "" {
		_ = h.appendTicketMessage(r.Context(), t.ID, p.Subject, core.AuthorUser, msg, "", now)
	}
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.create", t.FlowID, "ticket="+t.ID)
	h.notifyTicketFiled(t)
	writeJSON(rw, http.StatusCreated, ticketForUser(t))
}

func (h *supportAPI) listMyTickets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.ticketsEnabled() {
		writeAPIError(rw, http.StatusNotImplemented, "support_disabled", "support is not enabled on this deployment")
		return
	}
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

func (h *supportAPI) getMyTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	h.writeUserTicketView(rw, r, t)
}

func (h *supportAPI) postMyTicketMessage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	t.Status = core.TicketAwaitingSupport
	t.UpdatedAt = now
	_ = h.Tickets.Update(r.Context(), t)
	h.notifyUserReplied(t)
	h.writeUserTicketView(rw, r, t)
}

// The REQUESTER's own close, distinct from an agent resolving it.
func (h *supportAPI) setMyTicketStatus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	_ = h.appendSystemNote(r.Context(), t.ID, code, note, now)
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.status", t.FlowID, "ticket="+t.ID+" status="+string(status))
	h.writeUserTicketView(rw, r, t)
}

func (h *supportAPI) markMyTicketRead(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	h.markTicketRead(r.Context(), t, NudgeUser)
	h.writeUserTicketView(rw, r, t)
}

func (h *supportAPI) markSupportTicketRead(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	h.markTicketRead(r.Context(), t, NudgeSupport)
	h.writeTicketView(rw, r, t)
}

// Deliberately does NOT touch updated_at: reading is not activity.
func (h *supportAPI) markTicketRead(ctx context.Context, t core.Ticket, side NudgeSide) {
	now := h.supportTime()
	if side == NudgeUser {
		t.UserReadAt = now
	} else {
		t.SupportReadAt = now
	}
	_ = h.Tickets.Update(ctx, t)
}

func (h *supportAPI) listTicketQueue(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *supportAPI) ticketQueueSummary(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	writeJSON(rw, http.StatusOK, map[string]any{
		"summary": sum,
		"mine":    sum.ByAssignee[p.Subject],
	})
}

// Claim, hand over, or release.
func (h *supportAPI) assignSupportTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	// No system note: who on the support side owns a ticket is not the org's business.
	detail := "ticket=" + t.ID + " assignee=" + assignee
	if assignee == "" {
		detail = "ticket=" + t.ID + " unassigned"
	}
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.assign", t.FlowID, detail)
	h.writeTicketView(rw, r, t)
}

func (h *supportAPI) isProvisionedSupportAgent(subject string) bool {
	if h.SupportAgents == nil {
		return true
	}
	return h.SupportAgents.Granted(subject)
}

func (h *supportAPI) getSupportTicket(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	h.writeTicketView(rw, r, t)
}

func (h *supportAPI) postSupportTicketMessage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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

func (h *supportAPI) setSupportTicketStatus(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	status, ok := decodeTicketStatusBody(rw, r)
	if !ok {
		return
	}
	// A double-clicked button must not append a second note.
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
	_ = h.appendSystemNote(r.Context(), t.ID, core.MarkedNote(status),
		"Ticket marked "+string(status)+".", now)
	h.audit(r.Context(), core.Principal{Tenant: t.Tenant, Subject: p.Subject},
		"support.ticket.status", t.FlowID, "ticket="+t.ID+" status="+string(status))
	if status == core.TicketResolved {
		h.notifyTicketResolved(t)
	}
	h.writeTicketView(rw, r, t)
}

func (h *supportAPI) loadTicketForTenant(rw http.ResponseWriter, r *http.Request, tenant string) (core.Ticket, bool) {
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

func (h *supportAPI) loadTicketForAgent(rw http.ResponseWriter, r *http.Request, p core.Principal) (core.Ticket, bool) {
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

func (h *supportAPI) getMyTicketBundle(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForTenant(rw, r, p.Tenant)
	if !ok {
		return
	}
	h.writeTicketBundle(rw, r, t)
}

func (h *supportAPI) getSupportTicketBundle(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	t, ok := h.loadTicketForAgent(rw, r, p)
	if !ok {
		return
	}
	h.writeTicketBundle(rw, r, t)
}

func (h *supportAPI) writeTicketBundle(rw http.ResponseWriter, r *http.Request, t core.Ticket) {
	if h.Bundles == nil || t.BundleID == "" {
		writeAPIError(rw, http.StatusNotFound, "no_bundle", "no diagnostic bundle attached to this ticket")
		return
	}
	rec, err := h.Bundles.Get(r.Context(), t.BundleID)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "no_bundle", "no diagnostic bundle attached to this ticket")
		return
	}
	// Byte-for-byte: re-serializing would risk re-introducing what redaction removed.
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(rec.Payload)
}

func (h *supportAPI) writeTicketView(rw http.ResponseWriter, r *http.Request, t core.Ticket) {
	msgs, err := h.Tickets.ListMessages(r.Context(), t.ID)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, ticketView{Ticket: t, Messages: msgs})
}

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

func ticketQueryLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > support.DefaultTicketListLimit {
		return 0
	}
	return n
}

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

// Strips the support org's internals before the requester sees it.
func ticketForUser(t core.Ticket) core.Ticket {
	t.AssignedTo = ""
	t.SupportReadAt = time.Time{}
	t.SupportNudgedAt = time.Time{}
	return t
}

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

func (h *supportAPI) writeUserTicketView(rw http.ResponseWriter, r *http.Request, t core.Ticket) {
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

// Scrubs pasted secrets before anything is persisted.
func (h *supportAPI) appendTicketMessage(ctx context.Context, ticketID, author string, kind core.AuthorKind, body, bundleID string, now time.Time) error {
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

func (h *supportAPI) appendSystemNote(ctx context.Context, ticketID string, code core.SystemNote, body string, now time.Time) error {
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

// Only for the caller's OWN flow.
func (h *supportAPI) buildAndStoreBundle(ctx context.Context, p core.Principal, flowID, runID string, now time.Time) string {
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
