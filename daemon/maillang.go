// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/maillang"
)

// Resolving whose language a transactional email is written in.
//
// There is no single answer, which is why these are three functions rather
// than one: an email's language belongs to whoever will READ it, and how we
// know that differs per email.
//
//	mailLang    the recipient is an account holder, so their own preference
//	            (Settings → Language) is the answer.
//	inviteLang  an invitation has no account behind the address — that is what
//	            an invitation is — so it follows the person doing the inviting,
//	            who is the one who knows who they are writing to. If the
//	            invitee DOES already have an account, theirs wins.
//	flowLang    the email is sent by a flow (an approval request and its
//	            outcome), so it follows the flow's own language: it is the flow
//	            speaking, and its author chose what language its steps write in.
//
// Every one of them degrades to English rather than failing: an email in the
// wrong language is a papercut, and an email not sent because a preference
// lookup failed is a broken product.

// langFromStore reads one account's language preference. Empty — meaning
// English — for every way the lookup can come up short: no store, no address,
// no such user, or a user who never chose.
func langFromStore(ctx context.Context, store auth.UserStore, email string) string {
	if store == nil || email == "" {
		return ""
	}
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return ""
	}
	return u.UI.Language
}

// mailLang reads the language preference of the account at `email`.
func (s *Service) mailLang(ctx context.Context, email string) string {
	if s == nil {
		return ""
	}
	return langFromStore(ctx, s.Users, email)
}

// mailLang is the gateway's resolver. It asks its OWN user store before the
// service's: the two fields are separate handles that production wiring points
// at the same store, and the HTTP layer's is the one the auth endpoints
// (signup, reset, verification) already read — so preferring it keeps this
// answer consistent with the account those handlers just touched.
func (h *HTTPGateway) mailLang(ctx context.Context, email string) string {
	if l := langFromStore(ctx, h.Users, email); l != "" {
		return l
	}
	return h.svc.mailLang(ctx, email)
}

// inviteLang picks the language for an invitation: the invitee's own
// preference when they already have an account, otherwise the inviter's.
func (h *HTTPGateway) inviteLang(ctx context.Context, invitee, inviter string) string {
	if l := h.mailLang(ctx, invitee); l != "" {
		return l
	}
	return h.mailLang(ctx, inviter)
}

// flowLang is the language a flow writes in — the same field the Date & time
// step reads, so a Swedish flow's approval email and the dates inside it agree.
func flowLang(graph core.Graph) string { return graph.Language }

// mailMsgs is maillang.For for a recipient's account language, in one call.
func (s *Service) mailMsgs(ctx context.Context, email string) maillang.Messages {
	return maillang.For(s.mailLang(ctx, email))
}
