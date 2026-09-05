// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
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
// authenticate (they simply can't suspend).
//
// CacheTTL memoizes the two answers for a short window. Without it these
// are two uncached primary-key reads on EVERY authenticated request —
// measured at 483us of the 485us an authenticated request spent in the
// auth chain, against 1.4us for the (already cached) session lookup, and
// the user read decodes four JSON columns to reach one boolean. A zero
// TTL disables the cache and restores the uncached behaviour exactly.
//
// Revocation semantics mirror CachingSessionStore deliberately, because
// this cache sits in the same request path: a suspension applied on THIS
// instance takes effect immediately (the platform-admin handlers call
// Invalidate), and the TTL bounds only cross-instance lag — the same
// window the session cache already has. Only definitive answers are
// cached: a transient store error still fails open, as before, but is
// never remembered, so a blip can't pin the gate open for the window.
type ModerationGate struct {
	Inner Authenticator
	Users UserStore
	Orgs  OrgProfileStore

	// CacheTTL is the memo window; <= 0 disables caching.
	CacheTTL time.Duration
	// CacheMax bounds each cache's entry count; <= 0 picks a default.
	CacheMax int

	once  sync.Once
	users *suspensionCache
	orgs  *suspensionCache
}

// initCaches builds the two caches on first use so a zero-value
// ModerationGate (struct literal, as every call site builds it) works.
func (g *ModerationGate) initCaches() {
	g.once.Do(func() {
		if g.CacheTTL <= 0 {
			return
		}
		g.users = newSuspensionCache(g.CacheTTL, g.CacheMax)
		g.orgs = newSuspensionCache(g.CacheTTL, g.CacheMax)
	})
}

// Invalidate drops the cached moderation answers for a subject and/or a
// tenant, so a suspend/unsuspend applied on this instance is enforced on
// the very next request instead of waiting out the TTL. Either argument
// may be empty. Safe on a gate with caching disabled.
func (g *ModerationGate) Invalidate(subject, tenant string) {
	if g == nil {
		return
	}
	g.initCaches()
	if g.users != nil && subject != "" {
		g.users.invalidate(subject)
	}
	if g.orgs != nil && tenant != "" {
		g.orgs.invalidate(tenant)
	}
}

// CacheStats reports cumulative hit/miss counts across both caches, for
// the /metrics endpoint. Mirrors CachingSessionStore.Stats.
func (g *ModerationGate) CacheStats() (hits, misses int64) {
	g.initCaches()
	for _, c := range []*suspensionCache{g.users, g.orgs} {
		if c != nil {
			h, m := c.stats()
			hits, misses = hits+h, misses+m
		}
	}
	return hits, misses
}

func (g *ModerationGate) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	p, err := g.Inner.Authenticate(ctx, credential)
	if err != nil {
		return core.Principal{}, err
	}
	g.initCaches()
	// Org lockout first: it's the broader hammer (every member, every
	// service account in the tenant) and a single lookup.
	if g.Orgs != nil && p.Tenant != "" {
		if g.suspended(g.orgs, p.Tenant, func() (bool, error) {
			prof, err := g.Orgs.GetOrgProfile(ctx, p.Tenant)
			// ErrUnknownOrgProfile (no profile row) is normal — active.
			return err == nil && prof.Suspended(), unlessUnknown(err, ErrUnknownOrgProfile)
		}) {
			return core.Principal{}, ErrAccountSuspended
		}
	}
	// User lockout: the subject is the user's email for password/session
	// principals. API-key/service-account subjects that aren't real users
	// return ErrUnknownUser and fall through (only the org check applies).
	if g.Users != nil && p.Subject != "" {
		if g.suspended(g.users, p.Subject, func() (bool, error) {
			u, err := g.Users.GetByEmail(ctx, p.Subject)
			return err == nil && u.Suspended(), unlessUnknown(err, ErrUnknownUser)
		}) {
			return core.Principal{}, ErrAccountSuspended
		}
	}
	return p, nil
}

// suspended answers one half of the gate, through cache when one is
// configured. lookup returns the verdict plus the error that decides
// whether the verdict is worth remembering: nil means definitive.
func (g *ModerationGate) suspended(cache *suspensionCache, key string, lookup func() (bool, error)) bool {
	if cache == nil {
		v, _ := lookup()
		return v
	}
	if v, ok := cache.get(key); ok {
		return v
	}
	v, err := lookup()
	if err == nil {
		cache.put(key, v)
	}
	return v
}

// unlessUnknown maps a store's "no such row" to nil, because that is a
// definitive answer (the account is not suspended) and caching it is what
// keeps service-account subjects — which never have a user row — from
// paying a guaranteed-miss round trip on every request. Any other error
// is transient and is passed through so the verdict is not remembered.
func unlessUnknown(err, unknown error) error {
	if errors.Is(err, unknown) {
		return nil
	}
	return err
}
