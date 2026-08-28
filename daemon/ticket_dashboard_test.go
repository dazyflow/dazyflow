// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/support"
)

// ticketDashboardHarness wires the ticket surface with an audit log + a runtime
// support-agent store (so assignment can be validated against provisioned staff)
// and returns a request helper.
type ticketDashboardHarness struct {
	*gatewayHarness
	audit *MemAuditLog
	now   time.Time
}

func newTicketDashboardHarness(t *testing.T) *ticketDashboardHarness {
	t.Helper()
	h := newGatewayHarness(t)
	d := &ticketDashboardHarness{
		gatewayHarness: h,
		audit:          NewMemAuditLog(),
		now:            time.Unix(1_700_000_000, 0).UTC(),
	}
	h.gw.Tickets = support.NewMemTicketStore()
	h.gw.Audit = d.audit
	h.gw.SupportAgents = support.NewMemAgentStore()
	h.gw.supportNow = func() time.Time { return d.now }
	return d
}

func (d *ticketDashboardHarness) do(token, method, path string, body any) *httptest.ResponseRecorder {
	buf := bytes.NewBuffer(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(d.gw, rw, req)
	return rw
}

// agentToken mints an API key carrying only SupportAgentRole — the weak,
// grant-gated support role. The subject is the agent's email, matching what a
// real session carries (see signup / SSO), which is what support.AgentStore keys on.
func (d *ticketDashboardHarness) agentToken(t *testing.T, subject string) string {
	t.Helper()
	// Key ids allow only letters/digits/'-', so derive one from the email.
	keyID := "k-" + strings.NewReplacer("@", "-", ".", "-").Replace(subject)
	_, tok, err := auth.IssueAPIKey(d.ks, t.Context(), keyID, "", "", subject,
		[]core.Role{core.SupportAgentRole()}, nil)
	if err != nil {
		t.Fatalf("issue agent key for %s: %v", subject, err)
	}
	return tok
}

// fileTicket files a ticket as the harness's org member and returns its id.
func (d *ticketDashboardHarness) fileTicket(t *testing.T, subject string) string {
	t.Helper()
	rw := d.do(d.token, "POST", "/api/v1/me/support/tickets", map[string]any{"subject": subject})
	if rw.Code != 201 {
		t.Fatalf("file ticket %q = %d: %s", subject, rw.Code, rw.Body)
	}
	var tk core.Ticket
	if err := json.Unmarshal(rw.Body.Bytes(), &tk); err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	return tk.ID
}

// decodeQueue pulls the ticket ids out of a queue listing response.
func decodeQueue(t *testing.T, rw *httptest.ResponseRecorder) []string {
	t.Helper()
	if rw.Code != 200 {
		t.Fatalf("queue listing = %d: %s", rw.Code, rw.Body)
	}
	var got struct {
		Tickets []core.Ticket `json:"tickets"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	ids := make([]string, 0, len(got.Tickets))
	for _, tk := range got.Tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

// TestSupportQueue_AssignmentAndFilters covers the Phase 3 dashboard: an agent
// claims a ticket, hands it to a provisioned colleague, and releases it; the
// ownership filters and the unbounded summary counts follow along; and a
// non-provisioned assignee is refused.
func TestSupportQueue_AssignmentAndFilters(t *testing.T) {
	d := newTicketDashboardHarness(t)
	d.gw.SupportAgents.Grant(t.Context(), "agent-b@vendor.com", "root")
	agentA := d.agentToken(t, "agent-a@vendor.com")
	agentB := d.agentToken(t, "agent-b@vendor.com")

	first := d.fileTicket(t, "Invoice flow keeps failing")
	d.now = d.now.Add(time.Minute)
	second := d.fileTicket(t, "Webhook never fires")

	// Both tickets start unclaimed, newest first.
	if ids := decodeQueue(t, d.do(agentA, "GET", "/api/v1/support/tickets", nil)); len(ids) != 2 || ids[0] != second {
		t.Fatalf("queue = %v, want [%s %s]", ids, second, first)
	}

	// --- Claim ---------------------------------------------------------------
	d.now = d.now.Add(time.Minute)
	rw := d.do(agentA, "POST", "/api/v1/support/tickets/"+first+"/assign", map[string]any{"assignee": "me"})
	if rw.Code != 200 {
		t.Fatalf("claim = %d: %s", rw.Code, rw.Body)
	}
	var view ticketView
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.AssignedTo != "agent-a@vendor.com" {
		t.Errorf("after claim assigned_to = %q, want agent-a@vendor.com", view.Ticket.AssignedTo)
	}
	// Claiming leaves no note in the customer's thread — assignment is internal.
	for _, m := range view.Messages {
		if m.AuthorKind == core.AuthorSystem {
			t.Errorf("assignment must not post a system note: %q", m.Body)
		}
	}

	// The ownership filters see it. "me" resolves per-caller: the ticket is
	// agent-a's work, and simultaneously NOT agent-b's.
	if ids := decodeQueue(t, d.do(agentA, "GET", "/api/v1/support/tickets?assignee=me", nil)); len(ids) != 1 || ids[0] != first {
		t.Errorf("agent-a's assignee=me = %v, want [%s]", ids, first)
	}
	if ids := decodeQueue(t, d.do(agentB, "GET", "/api/v1/support/tickets?assignee=me", nil)); len(ids) != 0 {
		t.Errorf("agent-b's assignee=me = %v, want []", ids)
	}
	if ids := decodeQueue(t, d.do(agentA, "GET", "/api/v1/support/tickets?unassigned=true", nil)); len(ids) != 1 || ids[0] != second {
		t.Errorf("unassigned = %v, want [%s]", ids, second)
	}
	// Filters compose with status: both tickets are awaiting_support.
	if ids := decodeQueue(t, d.do(agentA, "GET", "/api/v1/support/tickets?unassigned=true&status=resolved", nil)); len(ids) != 0 {
		t.Errorf("unassigned+resolved = %v, want []", ids)
	}

	// --- Summary -------------------------------------------------------------
	sum := d.summary(t, agentA)
	if sum.Summary.Total != 2 || sum.Summary.Open != 2 {
		t.Errorf("summary total/open = %d/%d, want 2/2", sum.Summary.Total, sum.Summary.Open)
	}
	if sum.Summary.Unassigned != 1 {
		t.Errorf("summary unassigned = %d, want 1", sum.Summary.Unassigned)
	}
	if sum.Mine != 1 {
		t.Errorf("summary mine (agent-a) = %d, want 1", sum.Mine)
	}
	if mineB := d.summary(t, agentB).Mine; mineB != 0 {
		t.Errorf("summary mine (agent-b) = %d, want 0", mineB)
	}
	// A resolved ticket drops out of the live counts but stays in ByStatus.
	if rw := d.do(agentA, "POST", "/api/v1/support/tickets/"+first+"/status",
		map[string]any{"status": "resolved"}); rw.Code != 200 {
		t.Fatalf("resolve = %d: %s", rw.Code, rw.Body)
	}
	sum = d.summary(t, agentA)
	if sum.Summary.Open != 1 || sum.Mine != 0 || sum.Summary.ByStatus[core.TicketResolved] != 1 {
		t.Errorf("after resolve: open=%d mine=%d resolved=%d, want 1/0/1",
			sum.Summary.Open, sum.Mine, sum.Summary.ByStatus[core.TicketResolved])
	}

	// --- Hand over + release -------------------------------------------------
	if rw := d.do(agentA, "POST", "/api/v1/support/tickets/"+second+"/assign",
		map[string]any{"assignee": "agent-b@vendor.com"}); rw.Code != 200 {
		t.Fatalf("hand over = %d: %s", rw.Code, rw.Body)
	}
	if ids := decodeQueue(t, d.do(agentB, "GET", "/api/v1/support/tickets?assignee=me", nil)); len(ids) != 1 || ids[0] != second {
		t.Errorf("after hand over agent-b's queue = %v, want [%s]", ids, second)
	}
	// Releasing puts it back in the unclaimed pool.
	if rw := d.do(agentB, "POST", "/api/v1/support/tickets/"+second+"/assign",
		map[string]any{"assignee": ""}); rw.Code != 200 {
		t.Fatalf("release = %d: %s", rw.Code, rw.Body)
	}
	if ids := decodeQueue(t, d.do(agentA, "GET", "/api/v1/support/tickets?unassigned=true", nil)); len(ids) != 1 || ids[0] != second {
		t.Errorf("after release unassigned = %v, want [%s]", ids, second)
	}

	// --- Assignment is not a way to name arbitrary staff ---------------------
	rw = d.do(agentA, "POST", "/api/v1/support/tickets/"+second+"/assign",
		map[string]any{"assignee": "stranger@example.com"})
	if rw.Code != 400 {
		t.Errorf("assigning a non-provisioned subject = %d, want 400: %s", rw.Code, rw.Body)
	}

	// --- Every support action lands in the ORG's audit log --------------------
	// The org sees who claimed, handed over, and released their ticket — the
	// transparency channel that pays for assignment being invisible in the thread.
	actors := map[string]bool{}
	for _, e := range d.audit.mustList(t, core.AuditQuery{Tenant: "t", Limit: 100}) {
		if e.Action == "support.ticket.assign" {
			actors[e.Actor] = true
		}
	}
	for _, want := range []string{"agent-a@vendor.com", "agent-b@vendor.com"} {
		if !actors[want] {
			t.Errorf("no support.ticket.assign audited for %s (got %v)", want, actors)
		}
	}
	// A refused assignment must not be audited as if it happened.
	if len(actors) != 2 {
		t.Errorf("assign audit actors = %v, want exactly the two agents", actors)
	}
}

// summaryResponse mirrors the /support/tickets/summary body.
type summaryResponse struct {
	Summary core.TicketQueueSummary `json:"summary"`
	Mine    int                     `json:"mine"`
}

func (d *ticketDashboardHarness) summary(t *testing.T, token string) summaryResponse {
	t.Helper()
	rw := d.do(token, "GET", "/api/v1/support/tickets/summary", nil)
	if rw.Code != 200 {
		t.Fatalf("summary = %d: %s", rw.Code, rw.Body)
	}
	var out summaryResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	return out
}

// TestSupportQueue_RoleSeparation pins the Phase 3 boundaries in both
// directions: the customer never sees which staff member owns or answered their
// ticket, the customer may close/reopen but not declare "resolved", and neither a
// plain member nor a platform admin can work the cross-org queue.
func TestSupportQueue_RoleSeparation(t *testing.T) {
	d := newTicketDashboardHarness(t)
	agent := d.agentToken(t, "agent-a@vendor.com")
	id := d.fileTicket(t, "Flow stopped running")

	// Support claims it and replies.
	if rw := d.do(agent, "POST", "/api/v1/support/tickets/"+id+"/messages",
		map[string]any{"message": "Reconnect Stripe and retry."}); rw.Code != 200 {
		t.Fatalf("support reply = %d: %s", rw.Code, rw.Body)
	}

	// --- The customer's view hides the support organisation's internals ------
	rw := d.do(d.token, "GET", "/api/v1/me/support/tickets/"+id, nil)
	if rw.Code != 200 {
		t.Fatalf("get own ticket = %d: %s", rw.Code, rw.Body)
	}
	if body := rw.Body.String(); containsAny(body, "agent-a@vendor.com") {
		t.Errorf("user-facing ticket leaked the support agent's identity:\n%s", body)
	}
	var view ticketView
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.AssignedTo != "" {
		t.Errorf("user-facing assigned_to = %q, want empty", view.Ticket.AssignedTo)
	}
	if len(view.Messages) != 1 || view.Messages[0].AuthorKind != core.AuthorSupport {
		t.Fatalf("expected one support message, got %+v", view.Messages)
	}
	if view.Messages[0].Author != "" {
		t.Errorf("support message author = %q, want blanked for the customer", view.Messages[0].Author)
	}
	// The list endpoint redacts the same way.
	if body := d.do(d.token, "GET", "/api/v1/me/support/tickets", nil).Body.String(); containsAny(body, "agent-a@vendor.com") {
		t.Errorf("user-facing ticket list leaked the agent:\n%s", body)
	}
	// Support's own view still shows the record as stored — that's the point of
	// the split.
	if body := d.do(agent, "GET", "/api/v1/support/tickets/"+id, nil).Body.String(); !containsAny(body, "agent-a@vendor.com") {
		t.Errorf("support view should show the assignee:\n%s", body)
	}

	// --- The customer can close and reopen, but not resolve -----------------
	if rw := d.do(d.token, "POST", "/api/v1/me/support/tickets/"+id+"/status",
		map[string]any{"status": "resolved"}); rw.Code != 400 {
		t.Errorf("user declaring resolved = %d, want 400", rw.Code)
	}
	rw = d.do(d.token, "POST", "/api/v1/me/support/tickets/"+id+"/status",
		map[string]any{"status": "closed"})
	if rw.Code != 200 {
		t.Fatalf("user closing own ticket = %d: %s", rw.Code, rw.Body)
	}
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.Status != core.TicketClosed {
		t.Errorf("status after user close = %q, want closed", view.Ticket.Status)
	}
	if !hasSystemNote(view.Messages, "closed") {
		t.Errorf("closing should leave a system note: %+v", view.Messages)
	}
	rw = d.do(d.token, "POST", "/api/v1/me/support/tickets/"+id+"/status",
		map[string]any{"status": "awaiting_support"})
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.Status != core.TicketAwaitingSupport {
		t.Errorf("status after user reopen = %q, want awaiting_support", view.Ticket.Status)
	}
	if !hasSystemNote(view.Messages, "reopened") {
		t.Errorf("reopening a closed ticket should leave a system note: %+v", view.Messages)
	}
	// Handing a live ticket back to support again is a no-op: no new note, no
	// invented "reopened" for a ticket that was never finished.
	before := len(view.Messages)
	rw = d.do(d.token, "POST", "/api/v1/me/support/tickets/"+id+"/status",
		map[string]any{"status": "awaiting_support"})
	json.Unmarshal(rw.Body.Bytes(), &view)
	if len(view.Messages) != before {
		t.Errorf("re-sending the current status added %d message(s)", len(view.Messages)-before)
	}
	// Closing preserved the internal assignment even though the customer never
	// saw it (the redaction is a view concern, never a write).
	stored, err := d.gw.Tickets.Get(t.Context(), id)
	if err != nil || stored.AssignedTo != "agent-a@vendor.com" {
		t.Errorf("stored assignment = %q (err %v), want agent-a@vendor.com", stored.AssignedTo, err)
	}

	// --- Only a support agent works the queue -------------------------------
	for _, path := range []string{
		"/api/v1/support/tickets",
		"/api/v1/support/tickets/summary",
	} {
		if rw := d.do(d.token, "GET", path, nil); rw.Code != 403 {
			t.Errorf("org member GET %s = %d, want 403", path, rw.Code)
		}
		// A platform admin is the instance operator, not support staff: the
		// cross-tenant super-admin deliberately does NOT imply support:agent.
		if rw := d.platformDo(t, "GET", path, nil); rw.Code != 403 {
			t.Errorf("platform admin GET %s = %d, want 403", path, rw.Code)
		}
	}
	if rw := d.do(d.token, "POST", "/api/v1/support/tickets/"+id+"/assign",
		map[string]any{"assignee": "me"}); rw.Code != 403 {
		t.Errorf("org member assigning = %d, want 403", rw.Code)
	}
}

// hasSystemNote reports whether the thread carries a system note mentioning want.
func hasSystemNote(msgs []core.TicketMessage, want string) bool {
	for _, m := range msgs {
		if m.AuthorKind == core.AuthorSystem && containsAny(m.Body, want) {
			return true
		}
	}
	return false
}

// containsAny is a tiny readability helper for the leak assertions.
func containsAny(haystack string, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}
