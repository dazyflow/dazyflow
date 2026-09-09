// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

// Resolving whose language a transactional email is written in.
//
// An email's language belongs to whoever will READ it, and how we know that
// differs per email — which is why these are three functions rather than one:
//
//	mailLang    the recipient is an account holder, so their own preference wins.
//	inviteLang  an invitation has no account behind the address, so it follows
//	            the inviter, who knows who they are writing to. If the invitee
//	            does already have an account, theirs wins.
//	flowLang    the email is sent by a flow, so it follows the flow's own
//	            language: it is the flow speaking.
//
// All three degrade to English rather than failing: an email in the wrong
// language is a papercut, an email not sent is a broken product.

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

func (s *Service) mailLang(ctx context.Context, email string) string {
	if s == nil {
		return ""
	}
	return langFromStore(ctx, s.Users, email)
}

func flowLang(graph core.Graph) string { return graph.Language }

func (s *Service) mailMsgs(ctx context.Context, email string) maillang.Messages {
	return maillang.For(s.mailLang(ctx, email))
}

type langPicker struct {
	svc   *Service
	Users auth.UserStore
}

func (l langPicker) mailLang(ctx context.Context, email string) string {
	if l := langFromStore(ctx, l.Users, email); l != "" {
		return l
	}
	return l.svc.mailLang(ctx, email)
}

// inviteLang picks the language for an invitation: the invitee's own
// preference when they already have an account, otherwise the inviter's.
func (l langPicker) inviteLang(ctx context.Context, invitee, inviter string) string {
	if l := l.mailLang(ctx, invitee); l != "" {
		return l
	}
	return l.mailLang(ctx, inviter)
}

func (h *HTTPGateway) lang() langPicker { return langPicker{svc: h.svc, Users: h.Users} }

func (h *HTTPGateway) mailLang(ctx context.Context, email string) string {
	return h.lang().mailLang(ctx, email)
}

func (h *HTTPGateway) inviteLang(ctx context.Context, invitee, inviter string) string {
	return h.lang().inviteLang(ctx, invitee, inviter)
}
