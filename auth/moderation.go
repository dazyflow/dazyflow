// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ErrAccountSuspended is returned when a credential is valid but the
// account behind it — the user, or the org it acts in — has been
// suspended by a platform admin. Distinct from ErrInvalidCredential so
// the HTTP/gRPC layer can answer 403 ("you're locked out") rather than
// 401 ("who are you"), and the lockout screen can explain why.
var ErrAccountSuspended = errors.New("account suspended")

// ModerationGate wraps the auth Chain with the platform-admin lockout
// check. It runs AFTER the inner authenticator has proven the credential,
// then refuses the resulting principal when either the acting user or the
// acting org is suspended — so the same gate covers sessions, API keys,
// and OIDC bearers in one place, including keys minted before a
// suspension and members of an org suspended out from under them.
//
// Both stores are optional: a nil store skips that half of the check, so
// dev deployments without a Postgres user/profile backend still
// authenticate (they simply can't suspend). Suspension is rare, so the
// two extra primary-key reads this adds per request are acceptable for a
// workflow tool's request volume; sessions already cost one such read.
type ModerationGate struct {
	Inner Authenticator
	Users UserStore
	Orgs  OrgProfileStore
}

func (g *ModerationGate) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	p, err := g.Inner.Authenticate(ctx, credential)
	if err != nil {
		return core.Principal{}, err
	}
	// Org lockout first: it's the broader hammer (every member, every
	// service account in the tenant) and a single lookup.
	if g.Orgs != nil && p.Tenant != "" {
		prof, err := g.Orgs.GetOrgProfile(ctx, p.Tenant)
		if err == nil && prof.Suspended() {
			return core.Principal{}, ErrAccountSuspended
		}
		// ErrUnknownOrgProfile (no profile row) is normal — treat as active.
	}
	// User lockout: the subject is the user's email for password/session
	// principals. API-key/service-account subjects that aren't real users
	// return ErrUnknownUser and fall through (only the org check applies).
	if g.Users != nil && p.Subject != "" {
		u, err := g.Users.GetByEmail(ctx, p.Subject)
		if err == nil && u.Suspended() {
			return core.Principal{}, ErrAccountSuspended
		}
	}
	return p, nil
}
