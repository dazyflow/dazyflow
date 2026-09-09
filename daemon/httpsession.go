// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
)

// verifyCookieOrigin is the CSRF defense for cookie-authenticated requests. The
// session cookie is already SameSite=Lax + HttpOnly, which blocks the classical
// fetch-driven attack in modern browsers; this covers older ones and the few
// fetch shapes Lax misses, by requiring every cookie-auth POST/PUT/PATCH/DELETE
// to carry an Origin matching AllowedOrigins.
//
// GET/HEAD/OPTIONS pass through, as do requests with no session cookie — a
// Bearer client attaches none, so it has no CSRF surface.
//
// An empty AllowedOrigins refuses rather than allows: no browser-served origin
// should be writing, because the deployment has not opted into browser auth.
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
	if c, err := r.Cookie(sessionCookieName); err != nil || c.Value != token {
		return
	}
	key := auth.SessionLookupKey(token)
	sess, err := h.Sessions.GetSession(r.Context(), key)
	if err != nil {
		return
	}
	next, renew := auth.NextSessionExpiry(sess, h.sessionTTL(), h.cookies().maxSessionAge(), time.Now())
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

type sessionCookies struct {
	urlBuilder
	ttl    time.Duration
	maxAge time.Duration
}

func (c sessionCookies) sessionTTL() time.Duration {
	if c.ttl <= 0 {
		return 7 * 24 * time.Hour
	}
	return c.ttl
}

// maxSessionAge is the absolute ceiling a session can reach from CreatedAt,
// defaulting to 30d when unset. A non-positive MaxSessionAge keeps the
// default rather than disabling the cap, so the gateway always has a
// backstop even if the daemon forgets to wire it; an operator who truly
// wants unbounded sliding sets it explicitly to a very large value.
func (c sessionCookies) maxSessionAge() time.Duration {
	if c.maxAge <= 0 {
		return 30 * 24 * time.Hour
	}
	return c.maxAge
}

func (c sessionCookies) setSessionCookie(rw http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.requestIsHTTPS(r),
	})
}

func (c sessionCookies) clearSessionCookie(rw http.ResponseWriter, r *http.Request) {
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.requestIsHTTPS(r),
	})
}

func (h *HTTPGateway) cookies() sessionCookies {
	return sessionCookies{urlBuilder: h.urls(), ttl: h.SessionTTL, maxAge: h.MaxSessionAge}
}

func (h *HTTPGateway) sessionTTL() time.Duration { return h.cookies().sessionTTL() }

func (h *HTTPGateway) setSessionCookie(rw http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	h.cookies().setSessionCookie(rw, r, token, expires)
}
