// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
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
// The rule: the step's "Email these people" field, and nothing else. Blank
// means Dazyflow sends no mail and the step behaves exactly as it did before
// this existed — you deliver the `pending_url` link yourself, or people work
// the Approvals inbox.
//
// Defaulting to "everyone in the org who could act on it" was the other
// option and was rejected: approving is not permission-gated (anyone with the
// workspace can), so that set is wide, and it would have turned every
// already-deployed approval step into a mailshot the moment the daemon was
// upgraded — a behaviour change nobody asked for, on a channel that is hard
// to take back. Opt-in costs one field and surprises no one.
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

// approvalRecipients resolves the addresses for one await_approval node:
// whatever its "Email these people" field names, and nothing if it is blank.
func (s *Service) approvalRecipients(_ context.Context, graph core.Graph, nodeID string) []string {
	for _, n := range graph.Nodes {
		if n.ID == nodeID {
			return approvalParamApprovers(n.Params)
		}
	}
	return nil
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

// buildApprovalsURL points at the Approvals inbox, org-scoped the same way
// buildRunURL scopes a run link. This is the fallback destination when there is
// no signed one-click link — NOT the run page, which shows an awaiting node but
// offers no way to decide it (RunDetail can only stop a run). Sending someone to
// a page where they can see the thing waiting on them and do nothing about it is
// worse than sending no link at all.
func buildApprovalsURL(baseURL, tenant string) string {
	if baseURL == "" {
		return ""
	}
	return withOrg(strings.TrimRight(baseURL, "/")+"/approvals", tenant)
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
	if s.Mailer == nil {
		return
	}
	to := s.approvalRecipients(ctx, graph, nodeID)
	if len(to) == 0 {
		return
	}
	name := flowDisplayName(graph, graph.ID)
	prompt := approvalNodePrompt(graph, nodeID)
	runURL := buildRunURL(s.PublicBaseURL, graph.Tenant, runID)

	// approvalURL is the signed one-click link, and it only exists when the
	// deployment sets DAZYFLOW_APPROVAL_HMAC_SECRET — engine.ApprovalSigner is
	// nil otherwise and the step emits an empty pending_url. Requiring it here
	// meant every deployment without that secret sent no request mail at all,
	// silently, while still sending the decision mail.
	//
	// The fallback is the Approvals inbox, which needs a sign-in but does carry
	// Approve/Reject. It is deliberately not the run page: that shows the node
	// parked and gives you no way to act on it.
	approvalsURL := buildApprovalsURL(s.PublicBaseURL, graph.Tenant)
	link, linkLabel, shareWarning := approvalURL, "Open the approval", true
	if link == "" {
		link, linkLabel, shareWarning = approvalsURL, "Open Approvals", false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "The flow %q is waiting for a decision.\n\n", name)
	if prompt != "" {
		fmt.Fprintf(&b, "%s\n\n", prompt)
	}
	if approvalURL != "" {
		fmt.Fprintf(&b, "Approve or reject:  %s\n", approvalURL)
	}
	if approvalURL == "" && approvalsURL != "" {
		fmt.Fprintf(&b, "Approve or reject:  %s\n", approvalsURL)
	}
	if runURL != "" {
		fmt.Fprintf(&b, "Run details:        %s\n", runURL)
	}
	if approvalURL == "" && approvalsURL == "" {
		b.WriteString("Open Approvals in Dazyflow to approve or reject.\n")
	}

	facts := []emailtheme.Fact{{Label: "Flow", Value: name}, {Label: "Step", Value: nodeID}}
	intro := []string{fmt.Sprintf("The flow “%s” has paused and needs someone to decide before it can carry on.", name)}
	if prompt != "" {
		intro = append(intro, prompt)
	}
	// The don't-forward warning is only true of the signed link, which is a
	// bearer capability. The run page is access-controlled, so saying it there
	// would be false and would train people to ignore the real warning.
	outro := []string{"Whoever decides first resolves it — we'll email everyone the outcome."}
	if shareWarning {
		outro = append([]string{"Anyone with this link can approve or reject, so please don't forward it."}, outro...)
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf("Approval needed: %s", name),
		Preheader: "A flow has paused and is waiting for your decision.",
		Eyebrow:   "Approval needed",
		Heading:   "A flow is waiting on you",
		Intro:     intro,
		Facts:     facts,
		Outro:     outro,
		LogoURL:   emailLogoURL(s.PublicBaseURL),
	}
	if link != "" {
		content.Button = &emailtheme.Button{Label: linkLabel, URL: link}
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
	// One line per notification, before the sends. Duplicate approval mail was
	// reported from a live deployment and could not be reproduced — the
	// recipient list dedupes, Approve is guarded against a second decision,
	// and SendTrusted does not retry — which left no way to tell an
	// application double-send from a duplicate delivery downstream. This makes
	// that answerable from the log: one line means Dazyflow sent once.
	log.Printf("approval-notify(%s) %s/%s: sending to %d recipient(s)", kind, graph.Tenant, graph.ID, len(to))
	for _, addr := range to {
		if err := s.Mailer.SendThemed(ctx, addr, text, content); err != nil {
			// Best-effort, and per-recipient: one bad address must not stop
			// the rest of the list being told.
			log.Printf("approval-notify(%s) %s/%s -> %s: %v", kind, graph.Tenant, graph.ID, addr, err)
		}
	}
}
