// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
)

// support_notify.go closes the "nobody is ever told" gap in the ticket surface:
// without it, a customer learns support replied only by revisiting the page, and
// an agent learns a ticket exists only by watching the queue. That's fine for a
// demo and useless for a real support desk.
//
// Three edges are worth an email, and no others — a chat thread that mails on
// every state change trains people to ignore it:
//
//   - support replied            → the customer who filed it
//   - the customer replied       → the agent who owns it (or the support inbox)
//   - support marked it resolved → the customer who filed it
//
// A NEW ticket goes to SupportInbox (DAZYFLOW_SUPPORT_INBOX) because there is no
// single agent to address yet — the queue is shared. With no inbox configured
// that edge is silently skipped rather than fanned out to every agent.
//
// Delivery is best-effort and detached from the request, exactly like
// failure_notify.go: a slow or dead SMTP server must never make replying to a
// ticket slow or fail. Nothing here is transactional mail, so the customer's
// opt-out (NotifyPrefs.EmailOnSupportReply) is honoured; agent-side mail is
// operational and always sent.

// supportNotifyTimeout bounds the detached send. Generous — SMTP handshakes on a
// cold connection are slow — but finite so a hung server can't leak goroutines.
const supportNotifyTimeout = 2 * time.Minute

// ticketURLFor builds the absolute link to a ticket for the audience that gets
// the mail. The two sides live at different routes and an agent following the
// customer's URL would land on a ticket that isn't in their tenant.
//
// Only the customer link is pinned to the filing org (see withOrg in
// orglink.go). The customer's own view is tenant-scoped — loadTicketForTenant
// refuses a ticket outside the session's org — so a member of several orgs
// following an unpinned link would be told the ticket doesn't exist. The agent
// queue resolves tickets cross-tenant on purpose (loadTicketForAgent, gated on
// the support-agent role), so pinning it there would try to move the agent into
// the customer's org for no benefit — and agents generally aren't members of it.
func (h *HTTPGateway) ticketURLFor(t core.Ticket, agent bool) string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		return ""
	}
	if agent {
		return base + "/support/queue/" + t.ID
	}
	return withOrg(base+"/support/"+t.ID, t.Tenant)
}

// supportMailReady reports whether this deployment can send support mail at all.
func (h *HTTPGateway) supportMailReady() bool {
	return h.svc != nil && h.svc.Mailer != nil
}

// notifySupportReplied mails the customer that support answered. Honours the
// per-user opt-out; skipped entirely for subjects that aren't password accounts
// (an API-key or SSO subject won't resolve in the user store).
func (h *HTTPGateway) notifySupportReplied(t core.Ticket) {
	if !h.supportMailReady() || t.CreatedBy == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		to, ok := h.supportOptInAddress(ctx, t.CreatedBy)
		if !ok {
			return
		}
		url := h.ticketURLFor(t, false)
		// Goes to the customer, who has an account here, so it is written in
		// THEIR language.
		m := h.svc.mailMsgs(ctx, to)
		c := emailtheme.Content{
			Subject:   fmt.Sprintf(m.SupportRepliedSubject, t.Subject),
			Preheader: m.SupportRepliedPreheader,
			Eyebrow:   m.SupportEyebrow,
			Heading:   m.SupportRepliedHeading,
			Tone:      "info",
			Intro: []string{
				fmt.Sprintf(m.SupportRepliedIntro, t.Subject),
			},
			// The reply text itself is deliberately NOT included: it is stored
			// secret-scrubbed, but email is the one channel that leaves our
			// trust boundary, so the message stays behind the login.
			Outro:   []string{m.SupportRepliedOutro},
			LogoURL: emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: m.SupportButton, URL: url}
		}
		h.sendSupportMail(ctx, to, emailtheme.PlainText(c), c, t)
	})
}

// notifyTicketResolved mails the customer that support closed the loop.
func (h *HTTPGateway) notifyTicketResolved(t core.Ticket) {
	if !h.supportMailReady() || t.CreatedBy == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		to, ok := h.supportOptInAddress(ctx, t.CreatedBy)
		if !ok {
			return
		}
		url := h.ticketURLFor(t, false)
		// Goes to the customer, who has an account here, so it is written in
		// THEIR language.
		m := h.svc.mailMsgs(ctx, to)
		c := emailtheme.Content{
			Subject:   fmt.Sprintf(m.SupportResolvedSubject, t.Subject),
			Preheader: m.SupportResolvedPreheader,
			Eyebrow:   m.SupportEyebrow,
			Heading:   m.SupportResolvedHeading,
			Tone:      "success",
			Intro:     []string{fmt.Sprintf(m.SupportResolvedIntro, t.Subject)},
			Outro:     []string{m.SupportResolvedOutro},
			LogoURL:   emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: m.SupportButton, URL: url}
		}
		h.sendSupportMail(ctx, to, emailtheme.PlainText(c), c, t)
	})
}

// notifyUserReplied mails the support side that the customer came back.
//
// English, deliberately: this goes to the operator's own staff — the assigned
// agent, or the shared inbox from configuration — and a config-file address
// carries no language preference to read. Same for notifyTicketFiled below. Goes to
// the assigned agent when there is one, otherwise the shared inbox — an
// unclaimed ticket is nobody's personal responsibility.
func (h *HTTPGateway) notifyUserReplied(t core.Ticket) {
	to := supportQueueRecipient(t, h.SupportInbox)
	if !h.supportMailReady() || to == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		url := h.ticketURLFor(t, true)
		c := emailtheme.Content{
			Subject:   fmt.Sprintf("Customer replied: %s", t.Subject),
			Preheader: "A customer replied on a support ticket.",
			Eyebrow:   "Support queue",
			Heading:   "A customer replied",
			Tone:      "info",
			Intro:     []string{fmt.Sprintf("%s replied on “%s”.", t.Tenant, t.Subject)},
			Facts: []emailtheme.Fact{
				{Label: "Organization", Value: t.Tenant},
				{Label: "Ticket", Value: t.Subject},
			},
			LogoURL: emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: "Open in queue", URL: url}
		}
		text := fmt.Sprintf("%s replied on the support ticket %q.\n\nOpen it: %s\n", t.Tenant, t.Subject, url)
		h.sendSupportMail(ctx, to, text, c, t)
	})
}

// supportQueueRecipient picks who on the support side hears about activity on a
// ticket: the agent who owns it, or the shared inbox when it's unclaimed.
// Returns "" when neither exists, which the callers treat as "send nothing"
// rather than fanning out to every provisioned agent.
func supportQueueRecipient(t core.Ticket, inbox string) string {
	if t.AssignedTo != "" {
		return t.AssignedTo
	}
	return inbox
}

// notifyTicketFiled mails the shared support inbox that a new ticket landed.
// No-op when the operator hasn't configured one.
func (h *HTTPGateway) notifyTicketFiled(t core.Ticket) {
	if !h.supportMailReady() || h.SupportInbox == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		url := h.ticketURLFor(t, true)
		facts := []emailtheme.Fact{
			{Label: "Organization", Value: t.Tenant},
			{Label: "Filed by", Value: t.CreatedBy},
		}
		if t.FlowID != "" {
			facts = append(facts, emailtheme.Fact{Label: "Flow", Value: t.FlowID})
		}
		if t.BundleID != "" {
			facts = append(facts, emailtheme.Fact{Label: "Diagnostic", Value: "attached"})
		}
		c := emailtheme.Content{
			Subject:   fmt.Sprintf("New support ticket: %s", t.Subject),
			Preheader: "A new ticket is waiting in the support queue.",
			Eyebrow:   "Support queue",
			Heading:   "New support ticket",
			Tone:      "info",
			Intro:     []string{fmt.Sprintf("“%s” was filed and is unassigned.", t.Subject)},
			Facts:     facts,
			LogoURL:   emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: "Open in queue", URL: url}
		}
		text := fmt.Sprintf("New support ticket %q from %s.\n\nOpen it: %s\n", t.Subject, t.Tenant, url)
		h.sendSupportMail(ctx, h.SupportInbox, text, c, t)
	})
}

// supportOptInAddress resolves a ticket subject to a mailable address, applying
// the user's opt-out. Returns ok=false when there's nothing to send to.
func (h *HTTPGateway) supportOptInAddress(ctx context.Context, subject string) (string, bool) {
	if h.svc.Users == nil {
		return "", false
	}
	u, err := h.svc.Users.GetByEmail(ctx, subject)
	if err != nil {
		return "", false
	}
	if !u.Notify.EmailOnSupportReplyEnabled() {
		return "", false
	}
	return u.Email, true
}

// goSupportMail runs fn detached from the request, on the same bounded-context
// pattern the failure notifier uses.
func (h *HTTPGateway) goSupportMail(fn func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.Background(), supportNotifyTimeout)
	go func() {
		defer cancel()
		fn(ctx)
	}()
}

func (h *HTTPGateway) sendSupportMail(ctx context.Context, to, text string, c emailtheme.Content, t core.Ticket) {
	if err := h.svc.Mailer.SendThemed(ctx, to, text, c); err != nil && h.svc.Logger != nil {
		h.svc.Logger.Printf("support-notify [ticket=%s tenant=%s]: %v", t.ID, t.Tenant, err)
	}
}
