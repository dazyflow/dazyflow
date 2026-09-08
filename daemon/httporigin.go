// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (h *HTTPGateway) withCORSAndLogging(next http.Handler) http.Handler {
	allowCreds := len(h.AllowedOrigins) > 0 || h.WildcardDomain != ""
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if h.Metrics != nil {
			rec := &statusRecorder{ResponseWriter: rw}
			rw = rec
			start := time.Now()
			defer func() {
				h.Metrics.ObserveHTTP(r.Method, rec.statusCode(), time.Since(start).Seconds())
			}()
		}
		origin := r.Header.Get("Origin")
		// Unconditionally: the ACAO value varies, so a shared cache must not reuse it.
		rw.Header().Set("Vary", "Origin")
		switch {
		case allowCreds && origin != "" && h.originAllowed(origin):
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			rw.Header().Set("Access-Control-Allow-Credentials", "true")
		case !allowCreds:
			rw.Header().Set("Access-Control-Allow-Origin", "*")
		default:
			// Emit no CORS headers at all rather than a header the browser will reject.
		}
		rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		rw.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
		if h.requestIsHTTPS(r) {
			rw.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(r.URL.Path, "/form/") {
			rw.Header().Set("X-Frame-Options", "DENY")
			rw.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			rw.Header().Set("Content-Security-Policy", h.appCSP())
		}
		if r.Method == http.MethodOptions {
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func (h *HTTPGateway) appCSP() string {
	h.cspOnce.Do(func() {
		m := h.mapAPI()
		h.csp = buildAppCSP(cspOrigin(m.TileURL), cspOrigin(m.GeocoderURL))
	})
	return h.csp
}

// Each directive is tied to something the app actually does; widening one is a
// decision, not a convenience. img-src carries `data:` because every brand mark
// is inlined, which is what lets third-party logos render without a third-party
// request.
func buildAppCSP(tileOrigin, geocoderOrigin string) string {
	imgSrc := "'self' data: blob:"
	if tileOrigin != "" {
		imgSrc += " " + tileOrigin
	}
	connectSrc := "'self'"
	if geocoderOrigin != "" {
		connectSrc += " " + geocoderOrigin
	}
	return "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src " + imgSrc + "; " +
		"font-src 'self' data:; " +
		"connect-src " + connectSrc + "; " +
		"media-src 'self' blob:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"
}

// The origin this request ARRIVED on, which is not the configured base URL.
type urlBuilder struct {
	svc        *Service
	trustProxy bool
}

// Behind a proxy the listener is plaintext, so the forwarded header decides.
func (u urlBuilder) requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if u.trustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// Credentialed CORS: a wildcard is not permitted, so this must be exact.
func (h *HTTPGateway) originAllowed(origin string) bool {
	for _, a := range h.AllowedOrigins {
		if a == origin {
			return true
		}
	}
	// A wildcard must be specific enough that it cannot match the whole internet.
	if IsValidWildcardDomain(h.WildcardDomain) {
		if u, err := url.Parse(origin); err == nil {
			if hostIsSubdomainOf(u.Hostname(), h.WildcardDomain) {
				return true
			}
		}
	}
	return false
}

func hostIsSubdomainOf(host, domain string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	domain = strings.ToLower(domain)
	return domain != "" && strings.HasSuffix(host, "."+domain)
}

func isOrgSubdomainHost(host, wildcardDomain string) bool {
	if wildcardDomain == "" {
		return false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return hostIsSubdomainOf(host, wildcardDomain)
}

// At least two labels, or "*.com" would allow every site.
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

func (h *HTTPGateway) urls() urlBuilder {
	return urlBuilder{svc: h.svc, trustProxy: h.TrustProxyHeaders}
}

func (h *HTTPGateway) requestIsHTTPS(r *http.Request) bool { return h.urls().requestIsHTTPS(r) }
