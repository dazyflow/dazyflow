// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
)

// Approval mail: who gets told a flow is waiting on a person, and who gets
// told what they decided.
//
// The recipient set is resolved ONCE per notification from the same rule in
// both directions, so the people asked to decide are exactly the people told
// the outcome — a second approver never goes hunting for an item somebody
// already resolved.
//
// The rule, in order:
//
//  1. The step's "Email these people" param, if filled in. Explicit beats
//     inferred, and it's the only way to reach someone who ISN'T a member
//     (an external reviewer, a shared ops alias).
//  2. Otherwise every member of the org who could actually act on it —
//     editors and admins. Viewers are excluded: PermGraphRun lets them start
//     a flow, but the inbox they'd be mailed about is one they can resolve,
//     so including them would be mail about a decision they may not be meant
//     to make. Approving is not permission-gated today (anyone in the
//     workspace can), which is exactly why the DEFAULT is narrowed here
//     rather than blasted at everyone.
//
// Everything here is best-effort: an unreachable mailer must never block a
// flow from parking, and must never fail the decision that resumes it.

// approvalParamApprovers reads the step's explicit recipient list. Comma or
// semicolon separated — people paste both, and a list that silently notified
// nobody because of the wrong separator is the worst failure mode here.
func approvalParamApprovers(params map[string]any) []string {
	raw, _ := params["approvers"].(string)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		addr := strings.ToLower(strings.TrimSpace(part))
		if addr == "" || !strings.Contains(addr, "@") || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out
}

// orgApprovers lists the members of a tenant who can act on an approval:
// editors and admins. The tenant OWNER is included explicitly — a
// person-owned org keeps the owner's access on User.Tenant/User.Roles, not in
// the membership table, so listing memberships alone silently omits the one
// person guaranteed to be able to decide.
func (s *Service) orgApprovers(ctx context.Context, tenant string) []string {
	if tenant == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(email string) {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || seen[email] {
			return
		}
		seen[email] = true
		out = append(out, email)
	}
	if s.Memberships != nil {
		rows, err := s.Memberships.ListByTenant(ctx, tenant)
		if err != nil {
			log.Printf("approval-notify: list members of %s: %v", tenant, err)
		}
		for _, m := range rows {
			for _, r := range m.Roles {
				if r.Has(core.PermGraphEdit) || r.Has(core.PermOrganizationAdmin) {
					add(m.UserEmail)
					break
				}
			}
		}
	}
	if s.Users != nil {
		users, err := s.Users.ListUsers(ctx)
		if err != nil {
			log.Printf("approval-notify: list users: %v", err)
		}
		for _, u := range users {
			if u.Tenant != tenant {
				continue
			}
			for _, r := range u.Roles {
				if r.Has(core.PermGraphEdit) || r.Has(core.PermOrganizationAdmin) {
					add(u.Email)
					break
				}
			}
		}
	}
	// Stable order so a test — and a log line — reads the same every run.
	sort.Strings(out)
	return out
}

// approvalRecipients resolves the addresses for one await_approval node.
func (s *Service) approvalRecipients(ctx context.Context, graph core.Graph, nodeID string) []string {
	for _, n := range graph.Nodes {
		if n.ID != nodeID {
			continue
		}
		if explicit := approvalParamApprovers(n.Params); len(explicit) > 0 {
			return explicit
		}
		break
	}
	return s.orgApprovers(ctx, graph.Tenant)
}

// approvalNodePrompt returns the author's question for a node, if any.
func approvalNodePrompt(graph core.Graph, nodeID string) string {
	for _, n := range graph.Nodes {
		if n.ID == nodeID {
			p, _ := n.Params["prompt"].(string)
			return p
		}
	}
	return ""
}

// HandleNodeAwaiting is the WorkerConfig.OnNodeAwaiting adapter: it pulls the
// signed approval link off the parked result and mails the approvers. Lives
// here rather than in the wiring so cmd/dzd stays a declaration of what is
// connected, not a place that knows which port carries the link.
//
// Nodes that park for other reasons (a subgraph awaiting its child) carry no
// pending_url and fall out here, which is the whole filter — there is no
// approval to ask about.
func (s *Service) HandleNodeAwaiting(ctx context.Context, graph core.Graph, runID, nodeID string, result core.Result) {
	ref, ok := result.Output["pending_url"]
	if !ok {
		return
	}
	// Ref.Inline is `any` — the module writes a string here, but a wrong type
	// must degrade to "no mail", never to a panic on the worker goroutine.
	url, _ := ref.Inline.(string)
	s.NotifyApprovalRequested(ctx, graph, runID, nodeID, url)
}

// NotifyApprovalRequested mails the approvers when a run parks on an
// await_approval node. Called from the worker's park path, which is the only
// place that knows the pause actually took effect (as opposed to the module
// merely asking for one).
//
// approvalURL is the signed, single-purpose link — the same one the
// pending_url port carries. Anyone holding it can decide, which is why the
// recipient rule above is deliberately conservative.
func (s *Service) NotifyApprovalRequested(ctx context.Context, graph core.Graph, runID, nodeID, approvalURL string) {
	if s.Mailer == nil || approvalURL == "" {
		return
	}
	to := s.approvalRecipients(ctx, graph, nodeID)
	if len(to) == 0 {
		return
	}
	name := flowDisplayName(graph, graph.ID)
	prompt := approvalNodePrompt(graph, nodeID)
	runURL := buildRunURL(s.PublicBaseURL, graph.Tenant, runID)

	var b strings.Builder
	fmt.Fprintf(&b, "The flow %q is waiting for a decision.\n\n", name)
	if prompt != "" {
		fmt.Fprintf(&b, "%s\n\n", prompt)
	}
	fmt.Fprintf(&b, "Approve or reject:  %s\n", approvalURL)
	if runURL != "" {
		fmt.Fprintf(&b, "Run details:        %s\n", runURL)
	}

	facts := []emailtheme.Fact{{Label: "Flow", Value: name}, {Label: "Step", Value: nodeID}}
	intro := []string{fmt.Sprintf("The flow “%s” has paused and needs someone to decide before it can carry on.", name)}
	if prompt != "" {
		intro = append(intro, prompt)
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf("Approval needed: %s", name),
		Preheader: "A flow has paused and is waiting for your decision.",
		Eyebrow:   "Approval needed",
		Heading:   "A flow is waiting on you",
		Intro:     intro,
		Facts:     facts,
		Button:    &emailtheme.Button{Label: "Open the approval", URL: approvalURL},
		Outro: []string{
			"Anyone with this link can approve or reject, so please don't forward it.",
			"Whoever decides first resolves it — we'll email everyone the outcome.",
		},
		LogoURL: emailLogoURL(s.PublicBaseURL),
	}
	s.sendApprovalMail(ctx, "requested", graph, to, b.String(), content)
}

// NotifyApprovalDecided closes the loop: the same people who were asked now
// learn what happened and who did it. Called from Service.Approve after the
// resume has been committed, so the mail can never claim a decision that
// didn't land.
func (s *Service) NotifyApprovalDecided(
	ctx context.Context,
	graph core.Graph,
	runID, nodeID string,
	decision ApprovalDecision,
) {
	if s.Mailer == nil {
		return
	}
	to := s.approvalRecipients(ctx, graph, nodeID)
	if len(to) == 0 {
		return
	}
	name := flowDisplayName(graph, graph.ID)
	runURL := buildRunURL(s.PublicBaseURL, graph.Tenant, runID)
	approved := decision.Decision == "approve"
	verb := "rejected"
	tone := "danger"
	if approved {
		verb = "approved"
		tone = "success"
	}
	// The HMAC link path has no session, so Approver can be blank or a
	// self-declared label. Say so rather than printing an empty field.
	who := strings.TrimSpace(decision.Approver)
	if who == "" {
		who = "someone with the approval link"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The flow %q was %s by %s.\n\n", name, verb, who)
	if c := strings.TrimSpace(decision.Comment); c != "" {
		fmt.Fprintf(&b, "Comment: %s\n\n", c)
	}
	if runURL != "" {
		fmt.Fprintf(&b, "Run details: %s\n", runURL)
	}

	facts := []emailtheme.Fact{
		{Label: "Flow", Value: name},
		{Label: "Step", Value: nodeID},
		{Label: "Decision", Value: strings.ToUpper(verb[:1]) + verb[1:]},
		{Label: "Decided by", Value: who},
	}
	if c := strings.TrimSpace(decision.Comment); c != "" {
		facts = append(facts, emailtheme.Fact{Label: "Comment", Value: c})
	}
	outro := []string{"Nothing further is needed from you — this is just so you know it's handled."}
	if approved {
		outro = []string{"The flow has resumed and is running the steps after the approval."}
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf("%s: %s", strings.ToUpper(verb[:1])+verb[1:], name),
		Preheader: fmt.Sprintf("The approval on “%s” was %s.", name, verb),
		Eyebrow:   "Decision made",
		Heading:   fmt.Sprintf("The approval was %s", verb),
		Tone:      tone,
		Intro:     []string{fmt.Sprintf("%s %s the pending approval on “%s”.", who, verb, name)},
		Facts:     facts,
		Outro:     outro,
		LogoURL:   emailLogoURL(s.PublicBaseURL),
	}
	if runURL != "" {
		content.Button = &emailtheme.Button{Label: "View run details", URL: runURL}
	}
	s.sendApprovalMail(ctx, "decided", graph, to, b.String(), content)
}

// sendApprovalMail fans the message out one recipient at a time. One
// address per message on purpose: a shared To/Cc header would leak the org's
// member list to every approver, and an external reviewer named in the
// step's param would see it too.
func (s *Service) sendApprovalMail(
	ctx context.Context,
	kind string,
	graph core.Graph,
	to []string,
	text string,
	content emailtheme.Content,
) {
	for _, addr := range to {
		if err := s.Mailer.SendThemed(ctx, addr, text, content); err != nil {
			// Best-effort, and per-recipient: one bad address must not stop
			// the rest of the list being told.
			log.Printf("approval-notify(%s) %s/%s -> %s: %v", kind, graph.Tenant, graph.ID, addr, err)
		}
	}
}
