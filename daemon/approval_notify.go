// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

// Who is told a flow is waiting on a person. The mail goes through the OPERATOR'S
// transactional mailer, not an account the author connected, which is why the
// recipient list is capped rather than trusted.

// The choke point where MaxApprovalRecipients is actually applied.
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
		// One message per address, serially, on the worker goroutine that parked the run
		// — which is why the cap matters: an uncapped list holds a worker slot for hours.
		if len(out) >= core.MaxApprovalRecipients {
			break
		}
	}
	return out
}

func (s *Service) approvalRecipients(_ context.Context, graph core.Graph, nodeID string) []string {
	for _, n := range graph.Nodes {
		if n.ID == nodeID {
			return approvalParamApprovers(n.Params)
		}
	}
	return nil
}

// Org-scoped, or the link lands the approver in the wrong org.
func buildApprovalsURL(baseURL, tenant string) string {
	if baseURL == "" {
		return ""
	}
	return withOrg(strings.TrimRight(baseURL, "/")+"/approvals", tenant)
}

func (s *Service) HandleNodeAwaiting(ctx context.Context, graph core.Graph, runID, nodeID string, result core.Result) {
	ref, ok := result.Output["pending_url"]
	if !ok {
		return
	}
	url, _ := ref.Inline.(string)
	var prompt string
	if pRef, ok := result.Output["prompt"]; ok {
		prompt, _ = pRef.Inline.(string)
	}
	s.NotifyApprovalRequested(ctx, graph, runID, nodeID, url, prompt)
}

func (s *Service) NotifyApprovalRequested(ctx context.Context, graph core.Graph, runID, nodeID, approvalURL, prompt string) {
	if s.Mailer == nil {
		return
	}
	to := s.approvalRecipients(ctx, graph, nodeID)
	if len(to) == 0 {
		return
	}
	// The SUBJECT: RFC 5321 caps a line at 1000 octets, and a longer one is dropped.
	name := core.ClipNotificationLabel(flowDisplayName(graph, graph.ID))
	runURL := buildRunURL(s.PublicBaseURL, graph.Tenant, runID)

	// Only exists when a signer is configured; without one the inbox is the route.
	approvalsURL := buildApprovalsURL(s.PublicBaseURL, graph.Tenant)
	m := maillang.For(flowLang(graph))
	link, linkLabel, shareWarning := approvalURL, m.ApprovalOpenLink, true
	if link == "" {
		link, linkLabel, shareWarning = approvalsURL, m.ApprovalOpenInbox, false
	}

	facts := []emailtheme.Fact{{Label: m.FactFlow, Value: name}, {Label: m.FactStep, Value: core.ClipNotificationLabel(nodeID)}}
	// Both halves carry it, or an HTML reader loses the link entirely.
	if runURL != "" {
		facts = append(facts, emailtheme.Fact{Label: m.FactRun, Value: runURL})
	}
	intro := []string{fmt.Sprintf(m.ApprovalIntro, name)}
	if prompt != "" {
		// The author's own words, never translated, and bounded like any other body text.
		intro = append(intro, core.ClipNotificationText(prompt))
	}
	// Only true of the signed link, which IS the capability.
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

// The same people who were asked are told what was decided.
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
	// The SUBJECT: RFC 5321 caps a line at 1000 octets, and a longer one is dropped.
	name := core.ClipNotificationLabel(flowDisplayName(graph, graph.ID))
	runURL := buildRunURL(s.PublicBaseURL, graph.Tenant, runID)
	m := maillang.For(flowLang(graph))
	approved := decision.Decision == "approve"
	tone := "danger"
	if approved {
		tone = "success"
	}
	subjectFmt, preheaderFmt, heading, introFmt, outro, decided :=
		m.DecidedRejectedSubject, m.DecidedRejectedPreheader, m.DecidedRejectedHeading,
		m.DecidedRejectedIntro, m.DecidedRejectedOutro, m.DecidedRejectedValue
	if approved {
		subjectFmt, preheaderFmt, heading, introFmt, outro, decided =
			m.DecidedApprovedSubject, m.DecidedApprovedPreheader, m.DecidedApprovedHeading,
			m.DecidedApprovedIntro, m.DecidedApprovedOutro, m.DecidedApprovedValue
	}
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

func (s *Service) sendApprovalMail(
	ctx context.Context,
	kind string,
	graph core.Graph,
	to []string,
	text string,
	content emailtheme.Content,
) {
	log.Printf("approval-notify(%s) %s/%s: sending to %d recipient(s)", kind, graph.Tenant, graph.ID, len(to))
	for _, addr := range to {
		if err := s.Mailer.SendThemed(ctx, addr, text, content); err != nil {
			log.Printf("approval-notify(%s) %s/%s -> %s: %v", kind, graph.Tenant, graph.ID, addr, err)
		}
	}
}
