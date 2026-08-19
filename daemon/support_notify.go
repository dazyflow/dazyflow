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
func (h *HTTPGateway) ticketURLFor(t core.Ticket, agent bool) string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		return ""
	}
	if agent {
		return base + "/support/queue/" + t.ID
	}
	return base + "/support/" + t.ID
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
		c := emailtheme.Content{
			Subject:   fmt.Sprintf("Support replied: %s", t.Subject),
			Preheader: "Support has answered your ticket.",
			Eyebrow:   "Support",
			Heading:   "Support replied to your ticket",
			Tone:      "info",
			Intro: []string{
				fmt.Sprintf("Someone from support answered “%s”.", t.Subject),
			},
			// The reply text itself is deliberately NOT included: it is stored
			// secret-scrubbed, but email is the one channel that leaves our
			// trust boundary, so the message stays behind the login.
			Outro:   []string{"Open the ticket to read the full reply and respond."},
			LogoURL: emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: "View ticket", URL: url}
		}
		text := fmt.Sprintf("Support replied to your ticket %q.\n\nOpen it to read the reply: %s\n", t.Subject, url)
		h.sendSupportMail(ctx, to, text, c, t)
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
		c := emailtheme.Content{
			Subject:   fmt.Sprintf("Resolved: %s", t.Subject),
			Preheader: "Your support ticket was marked resolved.",
			Eyebrow:   "Support",
			Heading:   "Your ticket was marked resolved",
			Tone:      "success",
			Intro:     []string{fmt.Sprintf("Support marked “%s” resolved.", t.Subject)},
			Outro:     []string{"If it isn't actually fixed, reply on the ticket and it reopens."},
			LogoURL:   emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: "View ticket", URL: url}
		}
		text := fmt.Sprintf("Support marked your ticket %q resolved.\n\nReply here if it isn't fixed: %s\n", t.Subject, url)
		h.sendSupportMail(ctx, to, text, c, t)
	})
}

// notifyUserReplied mails the support side that the customer came back. Goes to
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
