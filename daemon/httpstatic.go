// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/dazyflow/dazyflow/auth"
)

type staticAPI struct {
	svc            *Service
	WildcardDomain string
	Profiles       auth.OrgProfileStore
}

func (h *HTTPGateway) staticAPI() *staticAPI {
	return &staticAPI{svc: h.svc, WildcardDomain: h.WildcardDomain, Profiles: h.Profiles}
}

// A deep link naming an org with its own subdomain is sent there, not served here.
func (h *staticAPI) withOrgBounce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if target := h.orgBounceTarget(r); target != "" {
			http.Redirect(rw, r, target, http.StatusFound)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// Only for a claimed subdomain this deployment actually serves.
func (h *staticAPI) orgBounceTarget(r *http.Request) string {
	if h.WildcardDomain == "" || h.Profiles == nil || r.Method != http.MethodGet {
		return ""
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return ""
	}
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
	// Re-validated rather than trusted: the stored value could predate the rules.
	label, err := auth.ValidateSubdomain(pr.Subdomain)
	if err != nil || label == "" {
		return ""
	}
	u := *r.URL
	u.Scheme = "https"
	if !strings.HasPrefix(h.svc.PublicBaseURL, "https") {
		u.Scheme = "http"
	}
	// Behind a proxy the browser's port is not the listener's, so carry it across.
	target := label + "." + h.WildcardDomain
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		target = net.JoinHostPort(target, port)
	}
	u.Host = target
	return u.String()
}

func (h *staticAPI) landingDistHandler(landingDir, webDir string) http.Handler {
	spa := webDistHandler(webDir)
	landingFS := http.FileServer(http.Dir(landingDir))
	landingIndex := filepath.Join(landingDir, "landing.html")
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(rw, r)
			return
		}
		// Auth-gated, or the marketing site leaks to signed-in deep links.
		if r.URL.Path == "/" {
			if h.hasValidSession(r) || isOrgSubdomainHost(r.Host, h.WildcardDomain) {
				spa.ServeHTTP(rw, r)
				return
			}
			http.ServeFile(rw, r, landingIndex)
			return
		}
		if landingHas(landingDir, r.URL.Path) {
			landingFS.ServeHTTP(rw, r)
			return
		}
		spa.ServeHTTP(rw, r)
	})
}

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

// Presence only: this decides which page to serve, not what it may do.
func (h *staticAPI) hasValidSession(r *http.Request) bool {
	token := credentialFromRequest(r)
	if token == "" {
		return false
	}
	_, err := h.svc.Authenticate(r.Context(), token)
	return err == nil
}

// SPA fallback: an unknown path is the router's business, not a 404.
func webDistHandler(root string) http.Handler {
	fileServer := http.FileServer(http.Dir(root))
	indexPath := filepath.Join(root, "index.html")
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(rw, r)
			return
		}
		clean := filepath.Clean(r.URL.Path)
		p := filepath.Join(root, clean)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			setStaticCacheControl(rw.Header(), clean)
			fileServer.ServeHTTP(rw, r)
			return
		}
		if ext := filepath.Ext(clean); ext != "" && ext != "." {
			http.NotFound(rw, r)
			return
		}
		// The shell names this build's hashed assets, so it must never be cached.
		rw.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(rw, r, indexPath)
	})
}

// A content-hashed name cannot change, so it is safe to cache for ever.
func immutableAsset(clean string) bool {
	dir, file := filepath.Split(filepath.ToSlash(clean))
	return strings.HasSuffix(dir, "/assets/") && contentHashed(file)
}

// Matches the hash shape Vite emits.
func contentHashed(name string) bool {
	name = strings.TrimSuffix(name, ".map")
	ext := filepath.Ext(name)
	if ext == "" {
		return false
	}
	stem := name[:len(name)-len(ext)]
	if len(stem) < 9 || stem[len(stem)-9] != '-' {
		return false
	}
	for _, c := range stem[len(stem)-8:] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// The lifetime the name earns: hashed is immutable, everything else revalidates.
func setStaticCacheControl(h http.Header, clean string) {
	if h.Get("Cache-Control") != "" {
		return
	}
	if immutableAsset(clean) {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	if strings.HasSuffix(clean, ".html") {
		h.Set("Cache-Control", "no-cache")
		return
	}
	// Replaceable in place, so it must revalidate rather than be cached for ever.
	h.Set("Cache-Control", "public, max-age=3600")
}
