// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/support"
)

// TestOAuthErrorCode_Cov covers every arm of the status->code mapping.
func TestOAuthErrorCode_Cov(t *testing.T) {
	cases := map[int]string{
		http.StatusNotImplemented:     "oauth_not_configured",
		http.StatusServiceUnavailable: "provider_not_configured",
		http.StatusForbidden:          "forbidden",
		http.StatusNotFound:           "provider_not_found",
		http.StatusBadRequest:         "invalid_request",
		http.StatusTeapot:             "internal_error", // default arm
	}
	for status, want := range cases {
		if got := oauthErrorCode(status); got != want {
			t.Errorf("oauthErrorCode(%d) = %q, want %q", status, got, want)
		}
	}
}

// TestRedactGraphSecrets_Cov covers redactGraphSecrets across triggers, node
// params/env, nested secret keys, and the FailureNotify webhook — verifying the
// original graph is never mutated in place.
func TestRedactGraphSecrets_Cov(t *testing.T) {
	orig := core.Graph{
		ID: "g",
		Triggers: []core.GraphTrigger{
			{Type: "webhook", Secret: "super-secret"},
			{Type: "cron"}, // no secret -> untouched
		},
		Nodes: []core.Node{
			{
				ID: "n",
				Params: map[string]any{
					"url":     "https://example.test",
					"api_key": "leak-me",
					"headers": map[string]any{"Authorization": "Bearer xyz"},
				},
				Env: map[string]string{"TOKEN": "envleak", "REGION": "eu"},
			},
		},
		FailureNotify: &core.FailureNotify{Webhook: "https://hooks.test/abc", Email: "ops@x.test"},
	}

	got := redactGraphSecrets(orig)

	if got.Triggers[0].Secret != redactedValue {
		t.Errorf("trigger secret not redacted: %q", got.Triggers[0].Secret)
	}
	if got.Nodes[0].Params["api_key"] != redactedValue {
		t.Errorf("api_key not redacted: %v", got.Nodes[0].Params["api_key"])
	}
	if got.Nodes[0].Params["url"] != "https://example.test" {
		t.Errorf("non-secret url should survive: %v", got.Nodes[0].Params["url"])
	}
	nested, _ := got.Nodes[0].Params["headers"].(map[string]any)
	if nested["Authorization"] != redactedValue {
		t.Errorf("nested Authorization not redacted: %v", nested)
	}
	if got.Nodes[0].Env["TOKEN"] != redactedValue {
		t.Errorf("env TOKEN not redacted: %v", got.Nodes[0].Env["TOKEN"])
	}
	if got.Nodes[0].Env["REGION"] != "eu" {
		t.Errorf("non-secret env should survive: %v", got.Nodes[0].Env["REGION"])
	}
	if got.FailureNotify.Webhook != redactedValue || got.FailureNotify.Email != "ops@x.test" {
		t.Errorf("failure notify = %+v", got.FailureNotify)
	}

	// The original must not have been mutated in place.
	if orig.Triggers[0].Secret != "super-secret" {
		t.Error("original trigger secret was mutated")
	}
	if orig.Nodes[0].Params["api_key"] != "leak-me" {
		t.Error("original node param was mutated")
	}
	if orig.FailureNotify.Webhook != "https://hooks.test/abc" {
		t.Error("original failure-notify webhook was mutated")
	}
}

func TestChildErrMessage_Cov(t *testing.T) {
	if got := childErrMessage(nil); got != "no error message" {
		t.Errorf("nil = %q", got)
	}
	je := &core.JobError{Code: "boom", Message: "kaboom"}
	if got := childErrMessage(je); got != je.Error() {
		t.Errorf("err = %q, want %q", got, je.Error())
	}
}

func TestExportHandler_Cov(t *testing.T) {
	user := auth.User{Subject: "ex@example.com", Email: "ex@example.com", Tenant: "home", Workspace: "main"}
	h, mem, _, tok := orgsSessionHarness(t, user)
	ctx := context.Background()
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "ex@example.com", Tenant: "acme", Workspace: "ws"})

	// API-key credential is rejected (export requires a session).
	if rw := h.do(t, "GET", "/api/v1/me/export", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("api-key export = %d, want 403", rw.Code)
	}

	// Session credential succeeds and the export is offered as a download.
	rw := sessionDo(t, h, tok, "GET", "/api/v1/me/export", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rw.Code, rw.Body.String())
	}
	if cd := rw.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition download header")
	}
	var exp DataExport
	if err := json.Unmarshal(rw.Body.Bytes(), &exp); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if exp.Profile.Email != "ex@example.com" {
		t.Fatalf("export profile email = %q", exp.Profile.Email)
	}
	if len(exp.Memberships) != 1 || exp.Memberships[0].Tenant != "acme" {
		t.Fatalf("export memberships = %+v", exp.Memberships)
	}

	// A session for an unknown email -> 404 (assembleExport's only hard error).
	ghost := auth.User{Subject: "ghost@example.com", Email: "ghost@example.com", Tenant: "x"}
	_, gtok, err := auth.IssueSession(ctx, h.gw.Sessions.(*auth.MemSessionStore), ghost, 3600*1e9)
	if err != nil {
		t.Fatalf("issue ghost session: %v", err)
	}
	if rw := sessionDo(t, h, gtok, "GET", "/api/v1/me/export", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("ghost export = %d, want 404", rw.Code)
	}
}

// TestAssembleExport_IncludesSupportAuditAndRoles covers the Art. 15 sections
// added after the first export shipped: support correspondence, the subject's
// own audit trail, and platform roles held.
func TestAssembleExport_IncludesSupportAuditAndRoles(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"
	const tenant = "acme"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{
		Email: email, Subject: email, Tenant: tenant, Workspace: "default",
	})

	tickets := support.NewMemTicketStore()
	_ = tickets.Create(ctx, core.Ticket{
		ID: "t1", Tenant: tenant, CreatedBy: email, Subject: "Flow broke",
		Status: core.TicketAwaitingSupport, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	_ = tickets.AppendMessage(ctx, core.TicketMessage{
		ID: "m1", TicketID: "t1", Author: email, AuthorKind: core.AuthorUser,
		Body: "my flow is broken", CreatedAt: time.Now(),
	})
	_ = tickets.AppendMessage(ctx, core.TicketMessage{
		ID: "m2", TicketID: "t1", Author: "agent@vendor.test", AuthorKind: core.AuthorSupport,
		Body: "looking into it", CreatedAt: time.Now(),
	})
	// Another member's thread in the SAME org must not appear: it is their
	// personal data, not this subject's.
	_ = tickets.Create(ctx, core.Ticket{
		ID: "t2", Tenant: tenant, CreatedBy: "bob@example.com", Subject: "Bob's problem",
		Status: core.TicketAwaitingSupport, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})

	audit := &fakeAuditLog{}
	_ = audit.Append(ctx, core.AuditEvent{
		Tenant: tenant, Actor: email, Action: "auth.login", Detail: "ip=1.2.3.4",
	})
	_ = audit.Append(ctx, core.AuditEvent{
		Tenant: tenant, Actor: "bob@example.com", Action: "auth.login", Detail: "ip=5.6.7.8",
	})

	admins := newMemPlatformAdmins()
	_ = admins.Grant(ctx, email, "root@platform.test")
	agents := support.NewMemAgentStore()

	h := &HTTPGateway{
		svc:                 &Service{},
		Users:               users,
		Tickets:             tickets,
		Audit:               audit,
		PlatformAdminGrants: admins,
		SupportAgents:       agents,
	}
	p := core.Principal{Subject: email, Tenant: tenant}

	exp, err := h.assembleExport(ctx, p)
	if err != nil {
		t.Fatalf("assembleExport: %v", err)
	}

	// Support: their thread, with both sides of the conversation.
	if len(exp.SupportTickets) != 1 {
		t.Fatalf("SupportTickets = %d, want 1 (theirs only)", len(exp.SupportTickets))
	}
	got := exp.SupportTickets[0]
	if got.ID != "t1" {
		t.Errorf("exported the wrong ticket: %q", got.ID)
	}
	if len(got.Messages) != 2 {
		t.Errorf("messages = %d, want 2 (their message and the reply)", len(got.Messages))
	}
	var foundBody bool
	for _, m := range got.Messages {
		if m.Body == "my flow is broken" {
			foundBody = true
		}
	}
	if !foundBody {
		t.Error("the subject's own words are missing from their support history")
	}

	// Audit: theirs, including the source IP; not their colleague's.
	if len(exp.AuditEvents) != 1 {
		t.Fatalf("AuditEvents = %d, want 1", len(exp.AuditEvents))
	}
	if exp.AuditEvents[0].Detail != "ip=1.2.3.4" {
		t.Errorf("audit detail = %q, want the subject's source IP", exp.AuditEvents[0].Detail)
	}

	// Roles held.
	if !exp.RoleGrants.PlatformAdmin {
		t.Error("platform-admin grant missing from the export")
	}
	if exp.RoleGrants.SupportAgent {
		t.Error("reported a support-agent role the subject does not hold")
	}

	// The document says what it left out.
	if len(exp.Excluded) == 0 {
		t.Error("Excluded is empty — a DSAR that silently omits a category is " +
			"indistinguishable from one with nothing to report")
	}
}

// TestAssembleExport_ExcludesOtherPeoplesData is the Art. 15(4) boundary: an
// access request is for the requester's data, not their colleagues'.
func TestAssembleExport_ExcludesOtherPeoplesData(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"
	const tenant = "acme"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{Email: email, Subject: email, Tenant: tenant, Workspace: "default"})

	tickets := support.NewMemTicketStore()
	for _, other := range []string{"bob@example.com", "carol@example.com"} {
		_ = tickets.Create(ctx, core.Ticket{
			ID: "t-" + other, Tenant: tenant, CreatedBy: other, Subject: "private",
			Status: core.TicketAwaitingSupport, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		})
		_ = tickets.AppendMessage(ctx, core.TicketMessage{
			ID: "m-" + other, TicketID: "t-" + other, Author: other,
			AuthorKind: core.AuthorUser, Body: "confidential", CreatedAt: time.Now(),
		})
	}
	audit := &fakeAuditLog{}
	_ = audit.Append(ctx, core.AuditEvent{Tenant: tenant, Actor: "bob@example.com", Action: "auth.login", Detail: "ip=5.6.7.8"})

	h := &HTTPGateway{svc: &Service{}, Users: users, Tickets: tickets, Audit: audit}
	exp, err := h.assembleExport(ctx, core.Principal{Subject: email, Tenant: tenant})
	if err != nil {
		t.Fatalf("assembleExport: %v", err)
	}
	if len(exp.SupportTickets) != 0 {
		t.Errorf("export leaked %d colleague tickets", len(exp.SupportTickets))
	}
	if len(exp.AuditEvents) != 0 {
		t.Errorf("export leaked %d colleague audit events (incl. their IP)", len(exp.AuditEvents))
	}
}

// TestAssembleExport_SurvivesUnconfiguredStores pins the best-effort contract
// the function documents: a deployment missing a store gets an empty section,
// not a failed export.
//
// It did not hold. ListFlowSummaries opens s.Workspaces with no nil check of
// its own, so an export on a daemon without a workspace store panicked the
// request instead of returning what it could.
func TestAssembleExport_SurvivesUnconfiguredStores(t *testing.T) {
	ctx := context.Background()
	const email = "alice@example.com"

	users, _ := auth.OpenJSONUserStore("")
	_ = users.PutUser(ctx, auth.User{Email: email, Subject: email, Tenant: "acme", Workspace: "default"})

	// Nothing wired but the user store — every other section must degrade.
	h := &HTTPGateway{svc: &Service{}, Users: users}

	exp, err := h.assembleExport(ctx, core.Principal{Subject: email, Tenant: "acme"})
	if err != nil {
		t.Fatalf("assembleExport with no stores: %v", err)
	}
	if exp.Profile.Email != email {
		t.Errorf("profile = %q, want the subject's row to still be present", exp.Profile.Email)
	}
	// Empty, non-nil sections so the JSON has [] rather than null.
	for name, n := range map[string]int{
		"Flows": len(exp.Flows), "Runs": len(exp.Runs),
		"SupportTickets": len(exp.SupportTickets), "AuditEvents": len(exp.AuditEvents),
		"Boards": len(exp.Boards), "Memberships": len(exp.Memberships),
	} {
		if n != 0 {
			t.Errorf("%s = %d, want 0 with no store configured", name, n)
		}
	}
	// Marshals cleanly — the document is the deliverable.
	if _, err := json.Marshal(exp); err != nil {
		t.Fatalf("export does not marshal: %v", err)
	}
}
