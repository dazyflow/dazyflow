// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Browser session handling: the session cookie's lifetime and renewal, the
// requireAuth wrapper every authenticated route goes through, and pulling a
// credential off a request (bearer header or cookie).

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// verifyCookieOrigin is the CSRF defense layer for cookie-authenticated
// requests. The session cookie already has SameSite=Lax + HttpOnly, which
// in modern browsers blocks the classical fetch-driven CSRF attack on its
// own. This middleware adds belt-and-suspenders defense for older browsers
// and for the small set of fetch shapes Lax doesn't cover (e.g. some
// top-level POST navigations in older Safari): every cookie-auth POST /
// PUT / PATCH / DELETE must carry an Origin header that matches one of
// the configured AllowedOrigins.
//
// Behaviour:
//   - GET / HEAD / OPTIONS pass through (they shouldn't mutate state; the
//     CORS preflight needs to land first anyway).
//   - Requests with no session cookie pass through (Bearer-auth clients
//     have no cookies attached, so there's no CSRF surface).
//   - Cookie-auth state-changing requests must have an Origin header
//     present and matching AllowedOrigins. Missing or mismatched Origin
//     returns 403.
//
// When AllowedOrigins is empty (single-tenant dev mode without web-origin
// configured), cookie-auth state-changing requests fall back to refusing —
// "no allowed origins" implies no browser-served origin should be
// performing writes; the deployment hasn't opted into browser auth.
func (h *HTTPGateway) verifyCookieOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(rw, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			// No session cookie → not a CSRF target.
			next.ServeHTTP(rw, r)
			return
		}
		// Both refusals below carry the machine code "csrf_origin" rather
		// than the status-derived default. The web client maps it onto a
		// sentence a person can act on: the raw text is a correct diagnosis
		// aimed at whoever configured the deployment, and it was being
		// rendered verbatim, in red, to whoever happened to click first.
		origin := r.Header.Get("Origin")
		if origin == "" {
			writeAPIError(rw, http.StatusForbidden, "csrf_origin",
				"cookie-authenticated state-changing requests require an Origin header (CSRF defense)")
			return
		}
		if !h.originAllowed(origin) {
			h.logger.Printf("CSRF reject: Origin=%q not in allowed=%v wildcard=%q (host=%q) — if this daemon serves the web bundle, set DAZYFLOW_PUBLIC_BASE_URL to this origin or add it to DAZYFLOW_WEB_ORIGIN", origin, h.AllowedOrigins, h.WildcardDomain, r.Host)
			writeAPIError(rw, http.StatusForbidden, "csrf_origin",
				fmt.Sprintf("cookie-authenticated request from disallowed origin %q (CSRF defense)", origin))
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func (h *HTTPGateway) requireAuth(next func(rw http.ResponseWriter, r *http.Request, p core.Principal)) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		token := credentialFromRequest(r)
		if token == "" {
			writeJSONError(rw, http.StatusUnauthorized, "missing Authorization: Bearer <token> or session cookie")
			return
		}
		p, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			// A valid credential behind a suspended user/org is a lockout,
			// not an identity failure — answer 403 so the web client can
			// show "your account is suspended" rather than bouncing to
			// sign-in (where the credential would just be rejected again).
			if errors.Is(err, auth.ErrAccountSuspended) {
				writeJSONError(rw, http.StatusForbidden, "account suspended")
				return
			}
			writeJSONError(rw, http.StatusUnauthorized, fmt.Sprintf("auth: %v", err))
			return
		}
		// Slide the session forward on activity so an active user isn't
		// bounced at the idle-TTL boundary. Must run before next() writes
		// the response body, since it sets a Set-Cookie header.
		h.maybeRenewSession(rw, r, token)
		next(rw, r, p)
	}
}

const sessionCookieName = "dazyflow_session"

// sessionTTL is the sliding idle window, defaulting to 7d when SessionTTL
// is unset (or non-positive). Centralizes the `ttl := h.SessionTTL; if ttl
// <= 0 { ttl = … }` default repeated at every session-issue site and in
// maybeRenewSession.
func (h *HTTPGateway) sessionTTL() time.Duration {
	if h.SessionTTL <= 0 {
		return 7 * 24 * time.Hour
	}
	return h.SessionTTL
}

// maxSessionAge is the absolute ceiling a session can reach from CreatedAt,
// defaulting to 30d when unset. A non-positive MaxSessionAge keeps the
// default rather than disabling the cap, so the gateway always has a
// backstop even if the daemon forgets to wire it; an operator who truly
// wants unbounded sliding sets it explicitly to a very large value.
func (h *HTTPGateway) maxSessionAge() time.Duration {
	if h.MaxSessionAge <= 0 {
		return 30 * 24 * time.Hour
	}
	return h.MaxSessionAge
}

// maybeRenewSession slides a cookie-backed session's expiry forward so an
// active user isn't logged out at the idle-TTL boundary. It runs after a
// successful authentication on every request, but only writes the store
// (and re-issues the cookie) once the session has passed its renewal
// threshold — see auth.NextSessionExpiry — so steady traffic stays
// write-free. Bearer callers manage their own credential lifetime and are
// skipped; only the browser cookie needs its Expires refreshed to match
// the slid server-side expiry. Best-effort: any error leaves the existing
// (still-valid) session untouched.
func (h *HTTPGateway) maybeRenewSession(rw http.ResponseWriter, r *http.Request, token string) {
	if h.Sessions == nil || !strings.HasPrefix(token, auth.SessionTokenPrefix) {
		return
	}
	// Only refresh the cookie for cookie-authenticated requests; a bearer
	// session token has no cookie to update.
	if c, err := r.Cookie(sessionCookieName); err != nil || c.Value != token {
		return
	}
	key := auth.SessionLookupKey(token)
	sess, err := h.Sessions.GetSession(r.Context(), key)
	if err != nil {
		return
	}
	next, renew := auth.NextSessionExpiry(sess, h.sessionTTL(), h.maxSessionAge(), time.Now())
	if !renew {
		return
	}
	sess.ExpiresAt = next
	if err := h.Sessions.PutSession(r.Context(), sess); err != nil {
		h.logger.Printf("session renew: %v", err)
		return
	}
	h.setSessionCookie(rw, r, token, next)
}

// setSessionCookie installs the host-only session cookie for token, expiring
// at expires. The Secure flag tracks whether the request reached us over TLS
// (requestIsHTTPS, which also honors a trusted X-Forwarded-Proto). Single
// source for every cookie-issuing sign-in path (password, SSO, handoff).
func (h *HTTPGateway) setSessionCookie(rw http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
}

// clearSessionCookie expires the session cookie (sign-out). Mirrors
// setSessionCookie's attributes so the browser matches and drops it.
func (h *HTTPGateway) clearSessionCookie(rw http.ResponseWriter, r *http.Request) {
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.requestIsHTTPS(r),
	})
}

// credentialFromRequest extracts a bearer credential from either the
// Authorization header (preferred, used by dzctl and API-key clients)
// or the session cookie set by /auth/signin (used by the browser).
func credentialFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if token != "" {
			return token
		}
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}
