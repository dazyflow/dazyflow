// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// End-to-end test of the ticket + chat surface over real HTTP: a user files a
// ticket about a flow (redacted bundle auto-attached, pasted secret scrubbed),
// support works the cross-tenant queue and replies, the user sees the reply, and
// support resolves it. Also checks the trust boundaries (non-support can't reach
// the queue; another org's ticket 404s).
func TestTicketFlow_EndToEnd(t *testing.T) {
	h := newGatewayHarness(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	h.gw.Tickets = NewMemTicketStore()
	h.gw.Bundles = NewMemBundleStore()
	h.gw.supportNow = func() time.Time { return now }
	ctx := context.Background()

	// Seed the user's flow with a pasted secret so we can prove the auto-attached
	// bundle is redacted.
	if _, err := h.ws.Save(core.Graph{
		ID: "flow1", Tenant: "t", Workspace: "ws", Name: "Daily invoice",
		Nodes: []core.Node{{
			ID: "charge", Module: "stripe_create_customer",
			Params: map[string]any{"api_key": "sk_live_leakyleakyleaky00"},
		}},
	}, "alice"); err != nil {
		t.Fatalf("seed graph: %v", err)
	}

	do := func(token, method, path string, body any) *httptest.ResponseRecorder {
		var buf *bytes.Buffer
		if body != nil {
			b, _ := json.Marshal(body)
			buf = bytes.NewBuffer(b)
		} else {
			buf = bytes.NewBuffer(nil)
		}
		req := httptest.NewRequest(method, path, buf)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	// --- User files a ticket, pasting a secret into the opening message --------
	rw := do(h.token, "POST", "/api/v1/me/support/tickets", map[string]any{
		"subject": "Invoice flow keeps failing",
		"flow_id": "flow1",
		"message": "here is my key sk_live_leakyleakyleaky00 please help",
	})
	if rw.Code != 201 {
		t.Fatalf("create ticket = %d: %s", rw.Code, rw.Body)
	}
	var created core.Ticket
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	if created.Tenant != "t" || created.CreatedBy != "alice" || created.Status != core.TicketAwaitingSupport {
		t.Errorf("ticket fields wrong: %+v", created)
	}
	if created.BundleID == "" {
		t.Fatal("expected a diagnostic bundle to be auto-attached")
	}

	// The attached bundle exists and is redacted (no secret).
	rec, err := h.gw.Bundles.Get(ctx, created.BundleID)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	if strings.Contains(string(rec.Payload), "sk_live_leakyleakyleaky00") {
		t.Errorf("attached bundle leaked the secret:\n%s", rec.Payload)
	}
	if !strings.Contains(string(rec.Payload), "stripe_create_customer") {
		t.Errorf("bundle missing diagnostic structure:\n%s", rec.Payload)
	}

	// The bundle is fetchable over HTTP (owner) and still redacted.
	rw = do(h.token, "GET", "/api/v1/me/support/tickets/"+created.ID+"/bundle", nil)
	if rw.Code != 200 {
		t.Fatalf("get own ticket bundle = %d: %s", rw.Code, rw.Body)
	}
	if strings.Contains(rw.Body.String(), "sk_live_leakyleakyleaky00") {
		t.Errorf("ticket bundle endpoint leaked the secret:\n%s", rw.Body)
	}
	if !strings.Contains(rw.Body.String(), "stripe_create_customer") {
		t.Errorf("ticket bundle endpoint missing structure:\n%s", rw.Body)
	}

	// The pasted secret was scrubbed out of the chat message too.
	rw = do(h.token, "GET", "/api/v1/me/support/tickets/"+created.ID, nil)
	if rw.Code != 200 {
		t.Fatalf("get own ticket = %d: %s", rw.Code, rw.Body)
	}
	var view ticketView
	json.Unmarshal(rw.Body.Bytes(), &view)
	if len(view.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(view.Messages))
	}
	if strings.Contains(view.Messages[0].Body, "sk_live_leakyleakyleaky00") {
		t.Errorf("chat message leaked the secret: %q", view.Messages[0].Body)
	}

	// The user sees the ticket in their own list.
	rw = do(h.token, "GET", "/api/v1/me/support/tickets", nil)
	if !strings.Contains(rw.Body.String(), created.ID) {
		t.Errorf("own ticket list missing the ticket: %s", rw.Body)
	}

	// --- Trust boundaries ------------------------------------------------------
	// A non-support principal cannot reach the cross-tenant queue.
	if rw := do(h.token, "GET", "/api/v1/support/tickets", nil); rw.Code != 403 {
		t.Errorf("editor hitting support queue = %d, want 403", rw.Code)
	}
	// A made-up ticket id 404s on the user surface.
	if rw := do(h.token, "GET", "/api/v1/me/support/tickets/nope", nil); rw.Code != 404 {
		t.Errorf("missing ticket = %d, want 404", rw.Code)
	}

	// --- Support agent works the queue and replies -----------------------------
	_, agentTok, err := auth.IssueAPIKey(h.ks, ctx, "k-agent", "", "", "agent-a",
		[]core.Role{core.SupportAgentRole()}, nil)
	if err != nil {
		t.Fatalf("issue agent key: %v", err)
	}
	rw = do(agentTok, "GET", "/api/v1/support/tickets", nil)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), created.ID) {
		t.Fatalf("support queue = %d, missing ticket: %s", rw.Code, rw.Body)
	}
	// Support replies → ticket becomes awaiting_user and is assigned to the agent.
	rw = do(agentTok, "POST", "/api/v1/support/tickets/"+created.ID+"/messages", map[string]any{
		"message": "Reconnect Stripe on the Apps page and retry.",
	})
	if rw.Code != 200 {
		t.Fatalf("support reply = %d: %s", rw.Code, rw.Body)
	}
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.Status != core.TicketAwaitingUser || view.Ticket.AssignedTo != "agent-a" {
		t.Errorf("after support reply: %+v", view.Ticket)
	}

	// The user sees the support reply on their own ticket.
	rw = do(h.token, "GET", "/api/v1/me/support/tickets/"+created.ID, nil)
	json.Unmarshal(rw.Body.Bytes(), &view)
	if len(view.Messages) != 2 || view.Messages[1].AuthorKind != core.AuthorSupport {
		t.Errorf("user should see the support reply: %+v", view.Messages)
	}

	// A user reply hands the ball back to support.
	rw = do(h.token, "POST", "/api/v1/me/support/tickets/"+created.ID+"/messages", map[string]any{
		"message": "Did that, still failing.",
	})
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.Status != core.TicketAwaitingSupport {
		t.Errorf("after user reply status = %q, want awaiting_support", view.Ticket.Status)
	}

	// --- Support resolves it ---------------------------------------------------
	rw = do(agentTok, "POST", "/api/v1/support/tickets/"+created.ID+"/status", map[string]any{
		"status": string(core.TicketResolved),
	})
	if rw.Code != 200 {
		t.Fatalf("resolve = %d: %s", rw.Code, rw.Body)
	}
	json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Ticket.Status != core.TicketResolved {
		t.Errorf("status after resolve = %q", view.Ticket.Status)
	}
	// A system note recorded the resolution.
	var sawSystem bool
	for _, m := range view.Messages {
		if m.AuthorKind == core.AuthorSystem && strings.Contains(m.Body, "resolved") {
			sawSystem = true
		}
	}
	if !sawSystem {
		t.Errorf("expected a system 'resolved' note: %+v", view.Messages)
	}

	// Bad status is rejected.
	if rw := do(agentTok, "POST", "/api/v1/support/tickets/"+created.ID+"/status", map[string]any{
		"status": "bogus",
	}); rw.Code != 400 {
		t.Errorf("bad status = %d, want 400", rw.Code)
	}
}

// When the store isn't wired, the surface reports 501 (disabled), not 500.
func TestTicketFlow_Disabled(t *testing.T) {
	h := newGatewayHarness(t) // no Tickets store
	req := httptest.NewRequest("GET", "/api/v1/me/support/tickets", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != 501 {
		t.Errorf("disabled ticket surface = %d, want 501", rw.Code)
	}
}
