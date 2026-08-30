// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon/support"
)

// The mark-read endpoints, which are what make the reminder sweep able to tell
// "hasn't answered" from "hasn't even looked". A read receipt that never
// arrives means mail to people who are up to date; one that arrives for the
// wrong side means silence for someone who is waiting.

func TestMarkTicketRead(t *testing.T) {
	h := newGatewayHarness(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	h.gw.Tickets = support.NewMemTicketStore()
	h.gw.supportNow = func() time.Time { return now }
	ctx := context.Background()

	do := func(token, method, path string, body any) *httptest.ResponseRecorder {
		buf := bytes.NewBuffer(nil)
		if body != nil {
			b, _ := json.Marshal(body)
			buf = bytes.NewBuffer(b)
		}
		req := httptest.NewRequest(method, path, buf)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		return rw
	}

	rw := do(h.token, "POST", "/api/v1/me/support/tickets", map[string]any{
		"subject": "Invoice flow keeps failing",
	})
	if rw.Code != 201 {
		t.Fatalf("create ticket = %d: %s", rw.Code, rw.Body)
	}
	var created core.Ticket
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// --- The customer's side ---------------------------------------------------
	// Advance the clock first. Marking read at the same instant the ticket was
	// created makes "did UpdatedAt move?" unanswerable — the assertion below
	// passed against a version that DID bump it, which is how this test earned
	// its own note.
	readAt := now.Add(30 * time.Minute)
	h.gw.supportNow = func() time.Time { return readAt }
	if rw := do(h.token, "POST", "/api/v1/me/support/tickets/"+created.ID+"/read", nil); rw.Code != 200 {
		t.Fatalf("mark read = %d: %s", rw.Code, rw.Body)
	}
	got, err := h.gw.Tickets.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.UserReadAt.Equal(readAt) {
		t.Errorf("UserReadAt = %v, want %v", got.UserReadAt, readAt)
	}
	if !got.SupportReadAt.IsZero() {
		t.Errorf("the customer's read stamped the support side too: %v", got.SupportReadAt)
	}
	// Reading is not activity. If it bumped UpdatedAt, every ticket anyone
	// glanced at would float to the top of the queue above ones actually
	// waiting — the ordering the support dashboard runs on.
	if !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("UpdatedAt moved on read: %v, want %v", got.UpdatedAt, created.UpdatedAt)
	}

	// --- The agent's side ------------------------------------------------------
	_, agentTok, err := auth.IssueAPIKey(h.ks, ctx, "k-agent", "", "", "agent-a",
		[]core.Role{core.SupportAgentRole()}, nil)
	if err != nil {
		t.Fatalf("issue agent key: %v", err)
	}
	later := now.Add(time.Hour)
	h.gw.supportNow = func() time.Time { return later }
	if rw := do(agentTok, "POST", "/api/v1/support/tickets/"+created.ID+"/read", nil); rw.Code != 200 {
		t.Fatalf("agent mark read = %d: %s", rw.Code, rw.Body)
	}
	got, _ = h.gw.Tickets.Get(ctx, created.ID)
	if !got.SupportReadAt.Equal(later) {
		t.Errorf("SupportReadAt = %v, want %v", got.SupportReadAt, later)
	}
	if !got.UserReadAt.Equal(readAt) {
		t.Errorf("the agent's read moved the customer's: %v", got.UserReadAt)
	}

	// --- What the customer is allowed to see -----------------------------------
	// The support side's read receipt is not the customer's business: "support
	// opened your ticket three days ago and said nothing" is true, unhelpful,
	// and not something to hand over by accident.
	rw = do(h.token, "GET", "/api/v1/me/support/tickets/"+created.ID, nil)
	if rw.Code != 200 {
		t.Fatalf("get own ticket = %d", rw.Code)
	}
	var view struct {
		Ticket core.Ticket `json:"ticket"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if !view.Ticket.SupportReadAt.IsZero() || !view.Ticket.SupportNudgedAt.IsZero() {
		t.Errorf("customer view leaked the support side's clocks: %+v", view.Ticket)
	}
	if !view.Ticket.UserReadAt.Equal(readAt) {
		t.Errorf("customer view lost their OWN read receipt: %v", view.Ticket.UserReadAt)
	}

	// --- Authorization ---------------------------------------------------------
	if rw := do(h.token, "POST", "/api/v1/support/tickets/"+created.ID+"/read", nil); rw.Code != 403 {
		t.Errorf("non-agent on the queue read endpoint = %d, want 403", rw.Code)
	}
	if rw := do(h.token, "POST", "/api/v1/me/support/tickets/nope/read", nil); rw.Code != 404 {
		t.Errorf("unknown ticket = %d, want 404", rw.Code)
	}
}
