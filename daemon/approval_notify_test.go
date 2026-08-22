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

// approvalFixture builds a Service with the stores the notifier touches. The
// recipient rule reads the step and nothing else, so there is no org here to
// fall back to — that is the property the tests below pin.
func approvalFixture(t *testing.T) *Service {
	t.Helper()
	users, _ := auth.OpenJSONUserStore("")
	return &Service{Users: users}
}

func approvalGraph(nodeParams map[string]any) core.Graph {
	return core.Graph{
		ID: "refunds", Name: "Refunds", Tenant: "t1", Workspace: "default",
		Nodes: []core.Node{{ID: "gate", Module: "await_approval", Params: nodeParams}},
	}
}

func TestApprovalRecipients_ExplicitList(t *testing.T) {
	svc := approvalFixture(t)
	g := approvalGraph(map[string]any{"approvers": "manager@acme.se, external@vendor.com"})
	got := svc.approvalRecipients(context.Background(), g, "gate")
	want := []string{"manager@acme.se", "external@vendor.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The whole point of the opt-in rule: a step nobody configured mails nobody.
// Upgrading a deployment full of existing approval steps must not turn them
// into a mailshot.
func TestApprovalRecipients_BlankMeansNobody(t *testing.T) {
	svc := approvalFixture(t)
	for _, params := range []map[string]any{
		nil,
		{},
		{"approvers": ""},
		{"approvers": "   "},
		{"prompt": "Refund 230 kr?"},
		// Nothing that parses as an address — same as blank.
		{"approvers": "the ops team"},
	} {
		if got := svc.approvalRecipients(context.Background(), approvalGraph(params), "gate"); len(got) != 0 {
			t.Errorf("params %v should mail nobody, got %v", params, got)
		}
	}
}

// A node id that isn't in the graph resolves to nobody rather than panicking
// or falling through to some wider set.
func TestApprovalRecipients_UnknownNode(t *testing.T) {
	svc := approvalFixture(t)
	g := approvalGraph(map[string]any{"approvers": "manager@acme.se"})
	if got := svc.approvalRecipients(context.Background(), g, "nope"); len(got) != 0 {
		t.Errorf("unknown node = %v, want none", got)
	}
}

// Both notify paths must be inert with no mailer — a deployment without SMTP
// still has to be able to park and resume approvals.
func TestApprovalNotify_NoMailerIsInert(t *testing.T) {
	svc := approvalFixture(t)
	svc.Mailer = nil
	g := approvalGraph(map[string]any{"approvers": "ops@acme.se"})
	svc.NotifyApprovalRequested(context.Background(), g, "run-1", "gate", "https://app/approve/x")
	svc.NotifyApprovalDecided(context.Background(), g, "run-1", "gate",
		ApprovalDecision{Decision: "approve", Approver: "admin@acme.se"})
}

// A subgraph node parks as awaiting too, but carries no approval link. The
// hook has to ignore it rather than mail an empty URL.
func TestHandleNodeAwaiting_IgnoresNonApprovalPauses(t *testing.T) {
	svc := approvalFixture(t)
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
