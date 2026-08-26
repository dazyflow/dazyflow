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
	"git.sr.ht/~klahr/dazyflow/internal/maillang"
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
	// This email is sent BY A FLOW, so it speaks the flow's language — the same
	// field the Date & time step reads — rather than any reader's preference.
	// Its recipients are addresses typed into the step and often have no
	// account here at all, so there is frequently no preference to read.
	m := maillang.For(flowLang(graph))
	link, linkLabel, shareWarning := approvalURL, m.ApprovalOpenLink, true
	if link == "" {
		link, linkLabel, shareWarning = approvalsURL, m.ApprovalOpenInbox, false
	}

	facts := []emailtheme.Fact{{Label: m.FactFlow, Value: name}, {Label: m.FactStep, Value: nodeID}}
	// The run's own URL used to appear only in the plain-text half of this
	// message, so an HTML reader never got it. As a fact it reaches both,
	// while the button stays the thing that actually decides the approval.
	if runURL != "" {
		facts = append(facts, emailtheme.Fact{Label: m.FactRun, Value: runURL})
	}
	intro := []string{fmt.Sprintf(m.ApprovalIntro, name)}
	if prompt != "" {
		// The prompt is the flow author's own words — never translated.
		intro = append(intro, prompt)
	}
	// The don't-forward warning is only true of the signed link, which is a
	// bearer capability. The run page is access-controlled, so saying it there
	// would be false and would train people to ignore the real warning.
	outro := []string{m.ApprovalOutro}
	if shareWarning {
		outro = append([]string{m.ApprovalShareWarning}, outro...)
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf(m.ApprovalSubject, name),
		Preheader: m.ApprovalPreheader,
		Eyebrow:   m.ApprovalEyebrow,
		Heading:   m.ApprovalHeading,
		Intro:     intro,
		Facts:     facts,
		Outro:     outro,
		LogoURL:   emailLogoURL(s.PublicBaseURL),
	}
	if link != "" {
		content.Button = &emailtheme.Button{Label: linkLabel, URL: link}
	}
	s.sendApprovalMail(ctx, "requested", graph, to, emailtheme.PlainText(content), content)
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
	m := maillang.For(flowLang(graph))
	approved := decision.Decision == "approve"
	tone := "danger"
	if approved {
		tone = "success"
	}
	// Each outcome is its own set of whole sentences rather than a verb slotted
	// into a shared template. English gets away with "was %s" because
	// "approved" and "rejected" are interchangeable there; Swedish inflects
	// them differently as verb and adjective ("godkände"/"avslog",
	// "godkänt"/"avslaget"), so a template could only ever be right in one of
	// them. Title-casing the verb by byte, as this used to, is the same class
	// of mistake — it assumes a language whose first letter is one byte.
	subjectFmt, preheaderFmt, heading, introFmt, outro, decided :=
		m.DecidedRejectedSubject, m.DecidedRejectedPreheader, m.DecidedRejectedHeading,
		m.DecidedRejectedIntro, m.DecidedRejectedOutro, m.DecidedRejectedValue
	if approved {
		subjectFmt, preheaderFmt, heading, introFmt, outro, decided =
			m.DecidedApprovedSubject, m.DecidedApprovedPreheader, m.DecidedApprovedHeading,
			m.DecidedApprovedIntro, m.DecidedApprovedOutro, m.DecidedApprovedValue
	}
	// The HMAC link path has no session, so Approver can be blank or a
	// self-declared label. Say so rather than printing an empty field.
	who := strings.TrimSpace(decision.Approver)
	if who == "" {
		who = m.DecidedAnonymous
	}

	facts := []emailtheme.Fact{
		{Label: m.FactFlow, Value: name},
		{Label: m.FactStep, Value: nodeID},
		{Label: m.FactDecision, Value: decided},
		{Label: m.FactDecidedBy, Value: who},
	}
	if c := strings.TrimSpace(decision.Comment); c != "" {
		facts = append(facts, emailtheme.Fact{Label: m.FactComment, Value: c})
	}
	content := emailtheme.Content{
		Subject:   fmt.Sprintf(subjectFmt, name),
		Preheader: fmt.Sprintf(preheaderFmt, name),
		Eyebrow:   m.DecidedEyebrow,
		Heading:   heading,
		Tone:      tone,
		Intro:     []string{fmt.Sprintf(introFmt, who, name)},
		Facts:     facts,
		Outro:     []string{outro},
		LogoURL:   emailLogoURL(s.PublicBaseURL),
	}
	if runURL != "" {
		content.Button = &emailtheme.Button{Label: m.DecidedButton, URL: runURL}
	}
	s.sendApprovalMail(ctx, "decided", graph, to, emailtheme.PlainText(content), content)
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
