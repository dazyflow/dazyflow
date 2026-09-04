// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Static asset serving: the marketing landing site and the built web app.
// Both are plain directory handlers with one wrinkle each — the landing
// site yields to the app for signed-in visitors, and the app falls back to
// index.html so client-side routes deep-link.

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
)

// staticAPI serves the static-asset endpoints. Its fields are the whole of what
// those handlers touch.
type staticAPI struct {
	svc            *Service
	WildcardDomain string
	// Profiles resolves an org to its claimed subdomain label. Only read for
	// the apex bounce below; nil simply disables it.
	Profiles auth.OrgProfileStore
}

// staticAPI builds them from the gateway's configuration.
func (h *HTTPGateway) staticAPI() *staticAPI {
	return &staticAPI{svc: h.svc, WildcardDomain: h.WildcardDomain, Profiles: h.Profiles}
}

// withOrgBounce wraps a static handler so a deep link naming an org that has
// its own subdomain is forwarded there before anything is served. Applied at
// the mount rather than inside either handler: both are mounted depending on
// whether a landing site is configured, and one of them delegates to the other,
// so this is the only place it lands exactly once.
func (h *staticAPI) withOrgBounce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if target := h.orgBounceTarget(r); target != "" {
			http.Redirect(rw, r, target, http.StatusFound)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// orgBounceTarget decides whether to send this request on to an org's own
// subdomain, returning the absolute URL to redirect to or "" to serve it here.
//
// Mail carries apex links — "View run details", the approvals inbox — because
// the apex is the one host that is always valid: an org can rename or drop its
// subdomain label, and an emailed link outlives that. But session cookies are
// host-only (deliberately: one org's subdomain must never read another's), so a
// member of an org that HAS a subdomain arrives at the apex with no session,
// gets the sign-in page, and ends up with a second session on a second host.
//
// The link already names the org — `?org=<tenant>`, which the app uses to pick
// the active org — so the apex can forward the whole request to the host where
// that member's session already lives. Apex links stay stable; the browser ends
// up in the right place.
//
// Deliberately narrow, because this redirects on a URL parameter:
//
//   - the host is built from a label read out of OUR store and the configured
//     apex, never from anything in the request, so it cannot be pointed
//     elsewhere;
//   - only GET, and only when the request carries no valid session HERE —
//     someone signed in on the apex is already where they should be, and
//     bouncing them would take their session away;
//   - only from the apex, so the redirect cannot loop.
func (h *staticAPI) orgBounceTarget(r *http.Request) string {
	if h.WildcardDomain == "" || h.Profiles == nil || r.Method != http.MethodGet {
		return ""
	}
	// An unregistered /api/ path is an API 404, not something to redirect a
	// client to another host over.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return ""
	}
	// Already on a subdomain (or some other host entirely): nothing to do.
	if !sameHost(bareHost(r.Host), h.WildcardDomain) {
		return ""
	}
	tenant := strings.TrimSpace(r.URL.Query().Get("org"))
	if tenant == "" {
		return ""
	}
	if h.hasValidSession(r) {
		return ""
	}
	pr, err := h.Profiles.GetOrgProfile(r.Context(), tenant)
	if err != nil || strings.TrimSpace(pr.Subdomain) == "" {
		return ""
	}
	// Re-validated rather than trusted: it is a DNS label when it is claimed,
	// and it has to still be one before it becomes part of a host.
	// ValidateSubdomain reports no error for an empty label, so the emptiness
	// is checked too — "" would otherwise build the host ".<apex>".
	label, err := auth.ValidateSubdomain(pr.Subdomain)
	if err != nil || label == "" {
		return ""
	}
	u := *r.URL
	u.Scheme = "https"
	if !strings.HasPrefix(h.svc.PublicBaseURL, "https") {
		u.Scheme = "http"
	}
	// Carry the request's port across. Behind a proxy on 443 the browser sends
	// no port and none is added, which is the production shape — but a
	// deployment reachable on any other port had the port dropped here and the
	// redirect sent to :80, where nothing answers.
	target := label + "." + h.WildcardDomain
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		target = net.JoinHostPort(target, port)
	}
	u.Host = target
	return u.String()
}

// landingDistHandler serves an optional static marketing site
// (landingDir) alongside the SPA (webDir) on the same origin. The
// root is auth-gated — a signed-in browser gets the app shell, an
// anonymous visitor gets the landing page — so a logged-out user who
// would previously have hit the SPA's sign-in screen at / now lands on
// marketing copy instead, while signed-in users keep their dashboard
// at /. Marketing pages and assets that resolve to a real file under
// landingDir (/style.css, /pricing, /privacy, /terms, /shots/*, …)
// serve publicly; everything else (the SPA's own assets and
// client-side routes) falls through to the SPA handler.
func (h *staticAPI) landingDistHandler(landingDir, webDir string) http.Handler {
	spa := webDistHandler(webDir)
	landingFS := http.FileServer(http.Dir(landingDir))
	landingIndex := filepath.Join(landingDir, "landing.html")
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// /api/* never falls through to static; defense in depth in
		// case a route was missed in mountRoutes.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(rw, r)
			return
		}
		// Root is auth-gated: the session cookie rides along on a
		// top-level navigation (SameSite=Lax), so a valid one means a
		// signed-in user who wants the app, not the marketing page.
		//
		// Marketing lives ONLY on the apex. On an org subdomain
		// (klahr.dazyflow.app) the org's app is the front door, so an
		// anonymous visitor gets the SPA — which routes them to sign-in with
		// their org preselected — instead of the apex marketing page.
		if r.URL.Path == "/" {
			if h.hasValidSession(r) || isOrgSubdomainHost(r.Host, h.WildcardDomain) {
				spa.ServeHTTP(rw, r)
				return
			}
			http.ServeFile(rw, r, landingIndex)
			return
		}
		// Public marketing pages/assets: anything that resolves to a
		// real file under landingDir. The SPA's routes and hashed
		// assets don't exist there, so they fall through below.
		if landingHas(landingDir, r.URL.Path) {
			landingFS.ServeHTTP(rw, r)
			return
		}
		spa.ServeHTTP(rw, r)
	})
}

// landingHas reports whether urlPath resolves to a servable file under
// dir — either a regular file, or a directory holding an index.html
// (so /pricing maps to pricing/index.html, matching the FileServer's
// directory-index behaviour). Used to decide landing-vs-SPA dispatch.
func landingHas(dir, urlPath string) bool {
	clean := filepath.Clean(urlPath)
	if clean == "/" || strings.HasPrefix(clean, "..") {
		return false
	}
	p := filepath.Join(dir, clean)
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	idx, err := os.Stat(filepath.Join(p, "index.html"))
	return err == nil && !idx.IsDir()
}

// hasValidSession reports whether the request carries a credential
// (session cookie or Bearer token) that authenticates successfully.
// Used to gate the marketing landing at / — it must not error the
// request, only classify it.
func (h *staticAPI) hasValidSession(r *http.Request) bool {
	token := credentialFromRequest(r)
	if token == "" {
		return false
	}
	_, err := h.svc.Authenticate(r.Context(), token)
	return err == nil
}

// webDistHandler serves files from root with SPA fallback to
// index.html for any path that doesn't resolve to an actual file —
// matches what `nginx try_files $uri /index.html` does. The fallback
// is the load-bearing piece for client-side routing (React Router's
// /flows/foo and /runs/bar paths aren't real files on disk).
//
// We intentionally do NOT serve under /api/* — those paths belong to
// the API and an unregistered /api/something should 404 as an API
// error, not return the index.html (which would mislead a client
// that's looking for JSON).
func webDistHandler(root string) http.Handler {
	fileServer := http.FileServer(http.Dir(root))
	indexPath := filepath.Join(root, "index.html")
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		// /api/* never falls through to static; defense in depth in
		// case a route was missed in mountRoutes.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(rw, r)
			return
		}
		// filepath.Clean + Join already strip "..", but the request
		// path is URL-formatted; FileServer handles the full
		// translation when we hand off below. Here we only need the
		// existence check.
		clean := filepath.Clean(r.URL.Path)
		p := filepath.Join(root, clean)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(rw, r)
			return
		}
		// File doesn't exist. If the path looks like an asset
		// request (has a file extension), 404 so a missing JS/CSS
		// surfaces as a build-broken error rather than masquerading
		// as HTML. Path-style requests (no extension, e.g. /flows,
		// /runs/abc) fall through to index.html so React Router
		// resolves them client-side.
		if ext := filepath.Ext(clean); ext != "" && ext != "." {
			http.NotFound(rw, r)
			return
		}
		http.ServeFile(rw, r, indexPath)
	})
}
