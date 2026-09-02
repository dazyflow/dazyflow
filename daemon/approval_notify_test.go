// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

func TestApprovalParamApprovers(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	svc := approvalFixture(t)
	g := approvalGraph(map[string]any{"approvers": "manager@acme.se"})
	if got := svc.approvalRecipients(context.Background(), g, "nope"); len(got) != 0 {
		t.Errorf("unknown node = %v, want none", got)
	}
}

// Both notify paths must be inert with no mailer — a deployment without SMTP
// still has to be able to park and resume approvals.
func TestApprovalNotify_NoMailerIsInert(t *testing.T) {
	t.Parallel()
	svc := approvalFixture(t)
	svc.Mailer = nil
	g := approvalGraph(map[string]any{"approvers": "ops@acme.se"})
	svc.NotifyApprovalRequested(context.Background(), g, "run-1", "gate", "https://app/approve/x", "")
	svc.NotifyApprovalDecided(context.Background(), g, "run-1", "gate",
		ApprovalDecision{Decision: "approve", Approver: "admin@acme.se"})
}

// A subgraph node parks as awaiting too, but carries no approval link. The
// hook has to ignore it rather than mail an empty URL.
func TestHandleNodeAwaiting_IgnoresNonApprovalPauses(t *testing.T) {
	t.Parallel()
	svc := approvalFixture(t)
	svc.Mailer = nil // the assertion is that we return before touching it
	svc.HandleNodeAwaiting(context.Background(), approvalGraph(nil), "run-1", "gate",
		core.Result{Output: map[string]core.Ref{"child_run": {Inline: "run-2"}}})
}

// The prompt in the email must be the RESOLVED text the step ran with, not the
// template the author typed. Reading it off the graph node mailed people a
// literal "${upstream.webhook_input_1.body}" where the question belonged —
// while the Approvals inbox, which reads the same value off the parked result,
// showed it correctly. One value, two readers, and only one of them was
// reading the resolved copy.
func TestHandleNodeAwaiting_MailsTheResolvedPromptNotTheTemplate(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	svc := approvalFixture(t)
	svc.Mailer = mailer
	svc.PublicBaseURL = "https://app.example"

	// The graph holds the unresolved template, exactly as it is stored.
	g := approvalGraph(map[string]any{
		"approvers": "ops@acme.se",
		"prompt":    "${upstream.webhook_input_1.body}",
	})
	svc.HandleNodeAwaiting(context.Background(), g, "run-1", "gate", core.Result{
		Output: map[string]core.Ref{
			"pending_url": {Inline: "https://app.example/approve/x"},
			// What the engine resolved it to before the step ran.
			"prompt": {Inline: "Release 0.27.5 to production?"},
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, raw, _ := srv.snapshot(); raw != "" {
			data := mailText(raw)
			if !strings.Contains(data, "Release 0.27.5 to production?") {
				t.Errorf("email is missing the resolved prompt:\n%s", data)
			}
			if strings.Contains(data, "upstream.webhook_input_1") {
				t.Errorf("email carries the raw template:\n%s", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no approval email sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A step with no prompt must not fall back to the node's params: the port is
// omitted only when the prompt was empty, so a fallback could do nothing but
// resurrect an unresolved template.
func TestHandleNodeAwaiting_NoPromptDoesNotFallBackToTheGraph(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	svc := approvalFixture(t)
	svc.Mailer = mailer
	svc.PublicBaseURL = "https://app.example"

	g := approvalGraph(map[string]any{
		"approvers": "ops@acme.se",
		"prompt":    "${upstream.webhook_input_1.body}",
	})
	svc.HandleNodeAwaiting(context.Background(), g, "run-1", "gate", core.Result{
		Output: map[string]core.Ref{"pending_url": {Inline: "https://app.example/approve/x"}},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, _, raw, _ := srv.snapshot(); raw != "" {
			if data := mailText(raw); strings.Contains(data, "upstream.webhook_input_1") {
				t.Errorf("fell back to the graph's raw params:\n%s", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no approval email sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Bodies are quoted-printable, which soft-wraps at 76 columns with "=\r\n" —
// so a phrase can be split mid-word and a naive Contains check silently
// passes (or silently fails). Un-wrap before asserting on prose.
func mailText(raw string) string {
	return strings.ReplaceAll(strings.ReplaceAll(raw, "=\r\n", ""), "=\n", "")
}

// Regression: the request mail used to require the signed approval link, so a
// deployment without DAZYFLOW_APPROVAL_HMAC_SECRET — where engine.ApprovalSigner
// is nil and the step emits an empty pending_url — sent no "please approve"
// mail at all, silently, while still sending the decision mail afterwards.
func TestNotifyApprovalRequested_SendsWithoutSignedLink(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	svc := approvalFixture(t)
	svc.Mailer = mailer
	svc.PublicBaseURL = "https://app.example"

	// The prompt now arrives as an argument — the RESOLVED text the step ran
	// with — rather than being re-read from the node. The graph keeps its copy
	// so this stays a faithful fixture, but it is no longer what is mailed.
	g := approvalGraph(map[string]any{"approvers": "ops@acme.se", "prompt": "Refund 230 kr?"})
	svc.NotifyApprovalRequested(context.Background(), g, "run-1", "gate", "", "Refund 230 kr?")

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, raw, to := srv.snapshot()
		if raw != "" {
			data := mailText(raw)
			if len(to) != 1 || !strings.Contains(to[0], "ops@acme.se") {
				t.Errorf("to = %v", to)
			}
			for _, want := range []string{"Refunds", "Refund 230 kr?", "run-1"} {
				if !strings.Contains(data, want) {
					t.Errorf("email missing %q", want)
				}
			}
			// No signed link, so the CTA must point at the Approvals inbox —
			// the run page shows the parked node but cannot decide it.
			if !strings.Contains(data, "/approvals") {
				t.Error("fallback CTA does not point at the Approvals inbox")
			}
			// The don't-forward warning is true only of a bearer link.
			// Phrase chosen without an apostrophe: the HTML part escapes it
			// to &#39;, so "don't forward" never matches and the check would
			// pass no matter what the mail said.
			if strings.Contains(data, "Anyone with this link") {
				t.Error("share warning shown for an access-controlled link")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no request email sent without a signed link")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// With a signer configured the one-click link is the CTA, and the warning
// that it is a bearer capability comes back.
func TestNotifyApprovalRequested_UsesSignedLinkWhenPresent(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	svc := approvalFixture(t)
	svc.Mailer = mailer
	svc.PublicBaseURL = "https://app.example"

	g := approvalGraph(map[string]any{"approvers": "ops@acme.se"})
	svc.NotifyApprovalRequested(context.Background(), g, "run-1", "gate",
		"https://app.example/approve/run-1/gate?token=abc", "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, raw, _ := srv.snapshot()
		if raw != "" {
			data := mailText(raw)
			if !strings.Contains(data, "approve/run-1/gate") {
				t.Error("signed link missing from the email")
			}
			if !strings.Contains(data, "Anyone with this link") {
				t.Error("bearer-link warning missing")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no request email sent")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// End-to-end count: ONE Approve call must produce ONE decision email per
// recipient. Reported from a live deployment as two identical emails to the
// same address for a single click, which neither the recipient dedupe nor the
// conditional Complete guard explains — so this drives the real path and
// counts what reaches the wire.
func TestApprove_SendsExactlyOneDecisionEmail(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")

	store := jobstore.NewMemory()
	graph := core.Graph{
		ID: "refunds", Name: "Refunds", Tenant: "acme", Workspace: "default",
		Nodes: []core.Node{{ID: "gate", Module: "await_approval",
			Params: map[string]any{"approvers": "ops@acme.se"}}},
	}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "refunds", NodeID: "*", Tenant: "acme",
		Status: core.JobStatusRunning, GraphPayload: payload,
	})
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "gate"), Kind: core.JobKindNode,
		GraphRunID: "run-1", NodeID: "gate", Status: core.JobStatusAwaiting,
	})
	svc := &Service{
		Jobs: store, Bus: NewMemoryBus(), Mailer: mailer,
		PublicBaseURL: "https://app.example",
		Engine:        &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}},
	}

	if err := svc.Approve(t.Context(), "run-1", "gate",
		ApprovalDecision{Decision: "reject", Approver: "ops@acme.se"}); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// Give any stray second send a chance to land before counting.
	time.Sleep(300 * time.Millisecond)
	_, _, data, to := srv.snapshot()
	// One Message-ID header per delivered message.
	if n := strings.Count(data, "Message-ID:"); n != 1 {
		t.Errorf("delivered %d emails for one Approve, want 1", n)
	}
	if len(to) != 1 {
		t.Errorf("RCPT TO issued %d times: %v", len(to), to)
	}
}

// The fallback destination must be somewhere the recipient can actually
// decide. It previously pointed at the run page, which renders an awaiting
// node and offers only "Stop run" — so the email led to a dead end.
func TestNotifyApprovalRequested_FallbackIsNotTheRunPage(t *testing.T) {
	t.Parallel()
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	svc := approvalFixture(t)
	svc.Mailer = mailer
	svc.PublicBaseURL = "https://app.example"

	svc.NotifyApprovalRequested(context.Background(),
		approvalGraph(map[string]any{"approvers": "ops@acme.se"}), "run-1", "gate", "", "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, raw, _ := srv.snapshot()
		if raw != "" {
			data := mailText(raw)
			if !strings.Contains(data, "https://app.example/approvals") {
				t.Error("CTA should be the Approvals inbox")
			}
			// The run link may still appear as context under "Run details",
			// but never as the thing the button sends you to.
			if strings.Contains(data, `"https://app.example/runs/run-1`) {
				t.Error("run page must not be the CTA target")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no email")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The approver list is a comma-separated param, so nothing but the graph byte
// budget bounded it — about 650,000 addresses — and the notifier sends one
// message per address, serially, on the worker goroutine that parked the run,
// through the OPERATOR'S mailer rather than an account the author connected.
// Capping where the list is READ covers both notifiers (the request mail and
// the decision mail) and any later reader.
func TestApprovalParamApprovers_IsCapped(t *testing.T) {
	t.Parallel()
	var list []string
	for i := range core.MaxApprovalRecipients * 20 {
		list = append(list, fmt.Sprintf("victim%d@example.com", i))
	}
	got := approvalParamApprovers(map[string]any{"approvers": strings.Join(list, ",")})
	if len(got) != core.MaxApprovalRecipients {
		t.Errorf("read %d approvers from a %d-address list, want it capped at %d",
			len(got), len(list), core.MaxApprovalRecipients)
	}
	// The ones it keeps are the ones the author listed first.
	if len(got) > 0 && got[0] != "victim0@example.com" {
		t.Errorf("first approver = %q, want the first one listed", got[0])
	}
	// A real list is untouched.
	real := approvalParamApprovers(map[string]any{"approvers": "ops@acme.se, cto@acme.se"})
	if len(real) != 2 {
		t.Errorf("an ordinary two-person list read as %d addresses", len(real))
	}
}
