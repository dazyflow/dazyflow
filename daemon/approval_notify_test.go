// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

func TestApprovalParamApprovers(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"unset", nil, nil},
		{"blank", "   ", nil},
		{"single", "ops@acme.se", []string{"ops@acme.se"}},
		// People paste whichever separator their address book gave them; a
		// list that silently notified nobody because of the wrong one is the
		// worst outcome this function can produce.
		{"comma and semicolon", "a@x.se, b@x.se; c@x.se", []string{"a@x.se", "b@x.se", "c@x.se"}},
		{"newlines", "a@x.se\nb@x.se", []string{"a@x.se", "b@x.se"}},
		{"trims and lowercases", "  Ops@ACME.se ", []string{"ops@acme.se"}},
		{"dedupes after normalising", "a@x.se, A@X.SE", []string{"a@x.se"}},
		// Not an address: dropping it beats handing the mailer garbage.
		{"skips non-addresses", "ops@acme.se, the ops team", []string{"ops@acme.se"}},
		{"only junk", "the ops team", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{}
			if tc.in != nil {
				params["approvers"] = tc.in
			}
			got := approvalParamApprovers(params)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("approvalParamApprovers(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// approvalFixture builds an org with one of each role plus an owner, and a
// graph carrying a single await_approval node.
func approvalFixture(t *testing.T, nodeParams map[string]any) *Service {
	t.Helper()
	ctx := context.Background()
	users, _ := auth.OpenJSONUserStore("")
	// The tenant OWNER holds their access on the user row, not in the
	// membership table — the case that made "list the members" wrong.
	if err := users.PutUser(ctx, auth.User{
		Email: "owner@acme.se", Subject: "owner@acme.se", Tenant: "t1",
		Roles: []core.Role{core.TeamRoleAdmin()},
	}); err != nil {
		t.Fatal(err)
	}
	// A user in a DIFFERENT org must never be swept in.
	if err := users.PutUser(ctx, auth.User{
		Email: "stranger@other.se", Subject: "stranger@other.se", Tenant: "t2",
		Roles: []core.Role{core.TeamRoleAdmin()},
	}); err != nil {
		t.Fatal(err)
	}
	mem := newFakeMembershipStore()
	for _, m := range []auth.Membership{
		{UserEmail: "editor@acme.se", Tenant: "t1", Roles: []core.Role{core.TeamRoleEditor()}},
		{UserEmail: "admin@acme.se", Tenant: "t1", Roles: []core.Role{core.TeamRoleAdmin()}},
		{UserEmail: "viewer@acme.se", Tenant: "t1", Roles: []core.Role{core.TeamRoleViewer()}},
		{UserEmail: "elsewhere@acme.se", Tenant: "t2", Roles: []core.Role{core.TeamRoleAdmin()}},
	} {
		if err := mem.PutMembership(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Users: users, Memberships: mem}
}

func approvalGraph(nodeParams map[string]any) core.Graph {
	return core.Graph{
		ID: "refunds", Name: "Refunds", Tenant: "t1", Workspace: "default",
		Nodes: []core.Node{{ID: "gate", Module: "await_approval", Params: nodeParams}},
	}
}

func TestApprovalRecipients_ExplicitListWins(t *testing.T) {
	svc := approvalFixture(t, nil)
	g := approvalGraph(map[string]any{"approvers": "manager@acme.se, external@vendor.com"})
	got := svc.approvalRecipients(context.Background(), g, "gate")
	want := []string{"manager@acme.se", "external@vendor.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestApprovalRecipients_DefaultsToEditorsAndAdmins(t *testing.T) {
	svc := approvalFixture(t, nil)
	g := approvalGraph(nil)
	got := svc.approvalRecipients(context.Background(), g, "gate")
	// Sorted: admin, editor, owner. Viewers are excluded (they can start a
	// flow, not decide one), and neither the other org's admin nor the
	// other org's member appears.
	want := []string{"admin@acme.se", "editor@acme.se", "owner@acme.se"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A blank param is the same as an absent one — an author who clears the field
// gets the org default back, not silence.
func TestApprovalRecipients_BlankParamFallsBack(t *testing.T) {
	svc := approvalFixture(t, nil)
	g := approvalGraph(map[string]any{"approvers": "   "})
	if got := svc.approvalRecipients(context.Background(), g, "gate"); len(got) != 3 {
		t.Fatalf("blank param should fall back to the org, got %v", got)
	}
}

// Without a membership store the default must NARROW to whoever the user
// store can vouch for, never widen or panic.
func TestApprovalRecipients_NoMembershipStore(t *testing.T) {
	svc := approvalFixture(t, nil)
	svc.Memberships = nil
	got := svc.approvalRecipients(context.Background(), approvalGraph(nil), "gate")
	if !reflect.DeepEqual(got, []string{"owner@acme.se"}) {
		t.Fatalf("got %v, want just the owner", got)
	}
}

// Both notify paths must be inert with no mailer — a deployment without SMTP
// still has to be able to park and resume approvals.
func TestApprovalNotify_NoMailerIsInert(t *testing.T) {
	svc := approvalFixture(t, nil)
	svc.Mailer = nil
	g := approvalGraph(nil)
	svc.NotifyApprovalRequested(context.Background(), g, "run-1", "gate", "https://app/approve/x")
	svc.NotifyApprovalDecided(context.Background(), g, "run-1", "gate",
		ApprovalDecision{Decision: "approve", Approver: "admin@acme.se"})
}

// A subgraph node parks as awaiting too, but carries no approval link. The
// hook has to ignore it rather than mail an empty URL.
func TestHandleNodeAwaiting_IgnoresNonApprovalPauses(t *testing.T) {
	svc := approvalFixture(t, nil)
	svc.Mailer = nil // the assertion is that we return before touching it
	svc.HandleNodeAwaiting(context.Background(), approvalGraph(nil), "run-1", "gate",
		core.Result{Output: map[string]core.Ref{"child_run": {Inline: "run-2"}}})
}

func TestApprovalNodePrompt(t *testing.T) {
	g := approvalGraph(map[string]any{"prompt": "Refund 230 kr?"})
	if got := approvalNodePrompt(g, "gate"); got != "Refund 230 kr?" {
		t.Errorf("prompt = %q", got)
	}
	if got := approvalNodePrompt(g, "missing"); got != "" {
		t.Errorf("unknown node should have no prompt, got %q", got)
	}
}
