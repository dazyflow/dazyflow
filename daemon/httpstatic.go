// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Static asset serving: the marketing landing site and the built web app.
// Both are plain directory handlers with one wrinkle each — the landing
// site yields to the app for signed-in visitors, and the app falls back to
// index.html so client-side routes deep-link.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticAPI serves the static-asset endpoints. Its fields are the whole of what
// those handlers touch.
type staticAPI struct {
	svc            *Service
	WildcardDomain string
}

// staticAPI builds them from the gateway's configuration.
func (h *HTTPGateway) staticAPI() *staticAPI {
	return &staticAPI{svc: h.svc, WildcardDomain: h.WildcardDomain}
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
