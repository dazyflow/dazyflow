// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/emailtheme"
)

// A ticket thread nobody is told about is a ticket nobody answers. Both sides get
// mail, and the sweeper nudges whichever side has been waiting.

const supportNotifyTimeout = 2 * time.Minute

// The two audiences land on different pages for the same ticket.
func (h *supportAPI) ticketURLFor(t core.Ticket, agent bool) string {
	base := strings.TrimRight(h.svc.PublicBaseURL, "/")
	if base == "" {
		return ""
	}
	if agent {
		return base + "/support/queue/" + t.ID
	}
	return withOrg(base+"/support/"+t.ID, t.Tenant)
}

func (h *supportAPI) supportMailReady() bool {
	return h.svc != nil && h.svc.Mailer != nil
}

func (h *supportAPI) notifySupportReplied(t core.Ticket) {
	if !h.supportMailReady() || t.CreatedBy == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		to, ok := h.supportOptInAddress(ctx, t.CreatedBy)
		if !ok {
			return
		}
		url := h.ticketURLFor(t, false)
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
			// The reply text is deliberately NOT included: it is scrubbed on ingest, but mail
			// leaves the deployment and a support thread can carry anything.
			Outro:   []string{m.SupportRepliedOutro},
			LogoURL: emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: m.SupportButton, URL: url}
		}
		h.sendSupportMail(ctx, to, emailtheme.PlainText(c), c, t)
	})
}

func (h *supportAPI) notifyTicketResolved(t core.Ticket) {
	if !h.supportMailReady() || t.CreatedBy == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		to, ok := h.supportOptInAddress(ctx, t.CreatedBy)
		if !ok {
			return
		}
		url := h.ticketURLFor(t, false)
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

func (h *supportAPI) notifyUserReplied(t core.Ticket) {
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

func (h *supportAPI) notifyWaitingOnUser(t core.Ticket) {
	if !h.supportMailReady() || t.CreatedBy == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		to, ok := h.supportOptInAddress(ctx, t.CreatedBy)
		if !ok {
			return
		}
		url := h.ticketURLFor(t, false)
		m := h.svc.mailMsgs(ctx, to)
		c := emailtheme.Content{
			Subject:   fmt.Sprintf(m.SupportWaitingSubject, t.Subject),
			Preheader: m.SupportWaitingPreheader,
			Eyebrow:   m.SupportEyebrow,
			Heading:   m.SupportWaitingHeading,
			Tone:      "info",
			Intro:     []string{fmt.Sprintf(m.SupportWaitingIntro, t.Subject)},
			Outro:     []string{m.SupportWaitingOutro},
			LogoURL:   emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: m.SupportButton, URL: url}
		}
		h.sendSupportMail(ctx, to, emailtheme.PlainText(c), c, t)
	})
}

func (h *supportAPI) notifyWaitingOnSupport(t core.Ticket, waiting time.Duration) {
	to := supportQueueRecipient(t, h.SupportInbox)
	if !h.supportMailReady() || to == "" {
		return
	}
	h.goSupportMail(func(ctx context.Context) {
		url := h.ticketURLFor(t, true)
		age := formatWaited(waiting)
		c := emailtheme.Content{
			Subject:   fmt.Sprintf("Unanswered for %s: %s", age, t.Subject),
			Preheader: "A support ticket has been waiting with nobody on it.",
			Eyebrow:   "Support queue",
			Heading:   "A ticket is still waiting",
			Tone:      "warning",
			Intro: []string{
				fmt.Sprintf("%s wrote on “%s” %s ago and nobody has opened it since.",
					t.Tenant, t.Subject, age),
			},
			Facts: []emailtheme.Fact{
				{Label: "Organization", Value: t.Tenant},
				{Label: "Ticket", Value: t.Subject},
				{Label: "Waiting", Value: age},
			},
			LogoURL: emailLogoURL(h.svc.PublicBaseURL),
		}
		if url != "" {
			c.Button = &emailtheme.Button{Label: "Open in queue", URL: url}
		}
		text := fmt.Sprintf("%s wrote on the support ticket %q %s ago and nobody has opened it since.\n\nOpen it: %s\n",
			t.Tenant, t.Subject, age, url)
		h.sendSupportMail(ctx, to, text, c, t)
	})
}

// The sweeper's hook: turns a waiting ticket into one reminder per waiting period.
func (h *supportAPI) NotifyTicketWaiting(t core.Ticket, side NudgeSide, waiting time.Duration) {
	if side == NudgeUser {
		h.notifyWaitingOnUser(t)
		return
	}
	h.notifyWaitingOnSupport(t, waiting)
}

// The way a person would say it, not a duration.
func formatWaited(d time.Duration) string {
	if days := int(d.Hours()) / 24; days >= 1 {
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	hours := int(d.Hours())
	if hours <= 1 {
		return "1 hour"
	}
	return fmt.Sprintf("%d hours", hours)
}

func supportQueueRecipient(t core.Ticket, inbox string) string {
	if t.AssignedTo != "" {
		return t.AssignedTo
	}
	return inbox
}

func (h *supportAPI) notifyTicketFiled(t core.Ticket) {
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

func (h *supportAPI) supportOptInAddress(ctx context.Context, subject string) (string, bool) {
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

func (h *supportAPI) goSupportMail(fn func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.Background(), supportNotifyTimeout)
	go func() {
		defer cancel()
		fn(ctx)
	}()
}

func (h *supportAPI) sendSupportMail(ctx context.Context, to, text string, c emailtheme.Content, t core.Ticket) {
	if err := h.svc.Mailer.SendThemed(ctx, to, text, c); err != nil && h.svc.Logger != nil {
		h.svc.Logger.Printf("support-notify [ticket=%s tenant=%s]: %v", t.ID, t.Tenant, err)
	}
}

func (h *HTTPGateway) NotifyTicketWaiting(t core.Ticket, side NudgeSide, waiting time.Duration) {
	h.supportAPI().NotifyTicketWaiting(t, side, waiting)
}
