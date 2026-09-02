// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Cross-origin and transport policy: the CORS/CSP/logging middleware every
// response passes through, and the host checks it relies on — which origins
// are allowed, whether a request arrived over HTTPS, and whether a host is
// one of the deployment's org subdomains.

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (h *HTTPGateway) withCORSAndLogging(next http.Handler) http.Handler {
	// Cookie-based sessions require reflecting the EXACT origin plus
	// Access-Control-Allow-Credentials: true — the wildcard "*" is incompatible
	// with credentials per the CORS spec. A deployment that configures no
	// browser origin at all (neither AllowedOrigins nor WildcardDomain) hasn't
	// opted into browser auth, so it serves "*" without credentials instead.
	allowCreds := len(h.AllowedOrigins) > 0 || h.WildcardDomain != ""
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// Wrap the writer to capture the status code, and time the
		// request for the RED metrics. The recorder delegates Flush so
		// SSE streams are unaffected.
		if h.Metrics != nil {
			rec := &statusRecorder{ResponseWriter: rw}
			rw = rec
			start := time.Now()
			defer func() {
				h.Metrics.ObserveHTTP(r.Method, rec.statusCode(), time.Since(start).Seconds())
			}()
		}
		origin := r.Header.Get("Origin")
		// Vary on Origin unconditionally. The ACAO value below is
		// origin-dependent, so announcing it only on the matching branch let a
		// shared cache store one origin's response — complete with its
		// Access-Control-Allow-Origin — and replay it to a different origin.
		// The header has to describe how the response varies whether or not
		// this particular request matched.
		rw.Header().Set("Vary", "Origin")
		switch {
		case allowCreds && origin != "" && h.originAllowed(origin):
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			rw.Header().Set("Access-Control-Allow-Credentials", "true")
		case !allowCreds:
			// No browser origin configured: open to any origin, but only
			// without credentials.
			rw.Header().Set("Access-Control-Allow-Origin", "*")
		default:
			// Credentialed mode with a missing or disallowed Origin. Emit no
			// ACAO at all. The previous fallback echoed the AllowedOrigins list
			// joined by commas, which is not a valid ACAO value (the grammar
			// admits a single origin or "*"), so no browser ever accepted it —
			// it only muddied caches. A non-browser client (dzctl, curl) never
			// reads the header; a browser correctly refuses the read.
		}
		rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		rw.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		// HSTS only over HTTPS — sending it on a plain-HTTP response is
		// pointless (browsers ignore it) and on a mixed setup could
		// wedge a not-yet-TLS host. 1 year, includeSubDomains.
		if h.requestIsHTTPS(r) {
			rw.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// Conservative content-type hardening for the API surface.
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		// Clickjacking + referrer hardening for the authenticated app surface.
		// The /form/ surface is deliberately embeddable (it sets its own
		// permissive CSP), so don't frame-deny it.
		if !strings.HasPrefix(r.URL.Path, "/form/") {
			rw.Header().Set("X-Frame-Options", "DENY")
			rw.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			rw.Header().Set("Content-Security-Policy", appCSP)
		}
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// appCSP is the Content-Security-Policy for the authenticated app surface
// (everything except the deliberately-embeddable /form/ pages, which set
// their own). Cheap defense-in-depth given the cookie-auth model: it was the
// one standard header missing here, so an injected <script> or a stolen
// stylesheet origin had nothing standing in its way.
//
// Each directive is tied to something the built bundle actually does:
//
//   - script-src 'self' — web/dist/index.html loads exactly one external
//     module script and no inline script, so no 'unsafe-inline' is needed.
//     This is the directive that matters; keep it inline-free.
//   - style-src ... 'unsafe-inline' — the app uses ~550 React style={{…}}
//     props, which are inline style attributes. Unavoidable without a
//     rewrite, and far less dangerous than inline script.
//   - img-src data: blob: — the CSS inlines small assets as data: URIs, and
//     generated previews/downloads use blob:.
//   - connect-src 'self' — no code path fetches a cross-origin API; SSE and
//     the JSON API are same-origin.
//   - frame-ancestors 'none' — the modern equivalent of the X-Frame-Options
//     DENY set above; both are sent so older browsers are covered too.
//   - form-action 'self', base-uri 'self', object-src 'none' — close the
//     usual injection escape hatches.
const appCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"media-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// urlBuilder answers "what origin did this request arrive on", which is all a
// handler needs to build a link back to itself. Kept narrow deliberately: the
// proxy-header gate is a security decision and lives in exactly one place.
type urlBuilder struct {
	svc        *Service
	trustProxy bool
}

// requestIsHTTPS reports whether the request reached the user over TLS.
// Directly: r.TLS is set. Behind a TLS-terminating reverse proxy the
// connection to dzd is plain HTTP, so we consult X-Forwarded-Proto —
// but only when TrustProxyHeaders is on, since an untrusted client
// could otherwise forge it to flip on the Secure cookie flag.
func (u urlBuilder) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if u.trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// originAllowed reports whether a browser Origin header is trusted for
// CORS + CSRF. An origin is allowed if it exactly matches one of the
// configured AllowedOrigins, or — when WildcardDomain is set — if it is
// a subdomain of that domain (e.g. "https://acme.dazyflow.app" against
// WildcardDomain "dazyflow.app"). The Origin header is set by the
// browser and not forgeable by page script, so suffix-matching the host
// is safe; "evil-dazyflow.app" doesn't end in ".dazyflow.app" so it
// won't match. The apex itself ("https://dazyflow.app") is intentionally
// NOT matched here — it carries a scheme/port we want pinned, so it must
// be listed explicitly in AllowedOrigins like any other exact origin.
func (h *HTTPGateway) originAllowed(origin string) bool {
	for _, a := range h.AllowedOrigins {
		if a == origin {
			return true
		}
	}
	// Only honour the wildcard if it's specific enough to be safe. A
	// single-label value like "com" would suffix-match every ".com" origin —
	// catastrophic — so a misconfigured domain trusts nobody rather than
	// everybody. cmd/dzd also rejects such a value at boot (fail-loud); this
	// is the defense-in-depth backstop on the request path.
	if IsValidWildcardDomain(h.WildcardDomain) {
		if u, err := url.Parse(origin); err == nil {
			if hostIsSubdomainOf(u.Hostname(), h.WildcardDomain) {
				return true
			}
		}
	}
	return false
}

// hostIsSubdomainOf reports whether host is a (single- or multi-level)
// subdomain of domain. Both are compared case-insensitively. The apex
// (host == domain) returns false — only strict subdomains match.
func hostIsSubdomainOf(host, domain string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	domain = strings.ToLower(domain)
	return domain != "" && strings.HasSuffix(host, "."+domain)
}

// isOrgSubdomainHost reports whether an inbound request's Host is a per-org
// wildcard subdomain (anything under the configured apex), so the landing
// handler can serve the app rather than the marketing page there. The port,
// if any, is stripped first; false when the wildcard feature is off.
func isOrgSubdomainHost(host, wildcardDomain string) bool {
	if wildcardDomain == "" {
		return false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return hostIsSubdomainOf(host, wildcardDomain)
}

// IsValidWildcardDomain reports whether d is specific enough to use as a
// CORS/CSRF subdomain-suffix match. It requires at least two non-empty labels
// (one dot), so "dazyflow.app" is accepted but a bare public suffix like "com"
// — which would trust every ".com" origin — is rejected. This is a coarse
// label-count guard, not a public-suffix-list check: a value like "co.uk"
// passes the count but is still a registry suffix, so operators must not set
// one. Empty d (wildcard disabled) returns false.
func IsValidWildcardDomain(d string) bool {
	d = strings.Trim(strings.TrimSpace(strings.ToLower(d)), ".")
	if d == "" {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" {
			return false
		}
	}
	return true
}

// effectiveBaseURL returns the origin to build user-facing URLs (trigger,
// hosted form, editor link) against. The operator's --public-base-url is
// authoritative when set. Otherwise we derive it from the request — honoring
// X-Forwarded-Proto/Host so a reverse proxy's external origin wins — so the
// trigger/form links a user gets are USABLE (absolute) instead of bare paths
// like "/form/...". The derived value is best-effort: behind a proxy that
// doesn't forward those headers it may be the internal host, which is why
// public_base_configured still reports whether the authoritative value is set.
func (u urlBuilder) effectiveBaseURL(r *http.Request) string {
	if b := strings.TrimRight(u.svc.PublicBaseURL, "/"); b != "" {
		return b
	}
	if r == nil {
		return ""
	}
	scheme := "http"
	if u.requestIsHTTPS(r) {
		scheme = "https"
	}
	// Only trust X-Forwarded-Host when the operator has opted into proxy
	// headers (DAZYFLOW_TRUST_PROXY_HEADERS), mirroring requestIsHTTPS. Without
	// the gate a client could set X-Forwarded-Host to reflect an attacker origin
	// back into the convenience URLs (canvas/trigger/share links) it receives.
	host := r.Host
	if u.trustProxy {
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			host = fwd
		}
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// urls exposes the request-origin helpers to a domain handler.
func (h *HTTPGateway) urls() urlBuilder {
	return urlBuilder{svc: h.svc, trustProxy: h.TrustProxyHeaders}
}

func (h *HTTPGateway) requestIsHTTPS(r *http.Request) bool { return h.urls().requestIsHTTPS(r) }
