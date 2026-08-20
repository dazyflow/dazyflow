// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Org profile routes: the display name and icon, the org's subdomain (claim,
// availability check, and the public resolve the app hits before sign-in),
// and the ACME hostname allowlist the TLS issuer consults.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// getOrgProfile returns the per-org display name + last-edited time.
// Always returns a row (even if the profile hasn't been written yet)
// so the UI can show the current value (empty) and the tenant ID side
// by side without distinguishing "no row" from "blank row".
func (h *HTTPGateway) getOrgProfile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profiles not configured")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = p.Tenant
	}
	if tenant != p.Tenant && !isPlatformAdmin(p) {
		writeJSONError(rw, http.StatusForbidden, "cannot view another tenant's profile")
		return
	}
	pr, err := h.Profiles.GetOrgProfile(r.Context(), tenant)
	if err != nil {
		// Empty row is the right shape — the UI fills in a default.
		writeJSON(rw, http.StatusOK, map[string]any{
			"tenant":          tenant,
			"display_name":    "",
			"wildcard_domain": h.WildcardDomain,
		})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       pr.Tenant,
		"display_name": pr.DisplayName,
		"icon":         pr.Icon,
		"subdomain":    pr.Subdomain,
		"updated_at":   pr.UpdatedAt,
		// The apex the subdomain hangs off (e.g. "dazyflow.app"), so the
		// editor renders "<label>.<domain>" and only shows the field when the
		// deploy supports per-org subdomains. Empty = feature off.
		"wildcard_domain": h.WildcardDomain,
	})
}

// maxOrgIconBytes caps the inline org icon (data: URL). Icons are
// downscaled client-side; this is a backstop against an oversized blob
// bloating the profile store.
const maxOrgIconBytes = 256 * 1024

func (h *HTTPGateway) putOrgProfile(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profiles not configured")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	body, ok := decodeRequestJSON[struct {
		DisplayName string `json:"display_name"`
		Icon        string `json:"icon"`
	}](rw, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(body.DisplayName)
	if len(name) > 80 {
		writeJSONError(rw, http.StatusBadRequest, "display name is too long (max 80)")
		return
	}
	if len(body.Icon) > maxOrgIconBytes {
		writeJSONError(rw, http.StatusBadRequest, "icon is too large")
		return
	}
	pr := auth.OrgProfile{
		Tenant:      p.Tenant,
		DisplayName: name,
		Icon:        body.Icon,
		UpdatedAt:   time.Now().UTC(),
	}
	// Preserve the subdomain: it's a full-row upsert, and the subdomain is
	// owned by the dedicated endpoint below (putOrgSubdomain). Without this
	// carry-over a name/icon save would silently clear a claimed subdomain.
	if existing, err := h.Profiles.GetOrgProfile(r.Context(), p.Tenant); err == nil {
		pr.Subdomain = existing.Subdomain
	}
	if err := h.Profiles.PutOrgProfile(r.Context(), pr); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "org_profile.update", p.Tenant, "name="+name)
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       pr.Tenant,
		"display_name": pr.DisplayName,
		"icon":         pr.Icon,
		"subdomain":    pr.Subdomain,
		"updated_at":   pr.UpdatedAt,
	})
}

// putOrgSubdomain claims (or clears) the org's subdomain label — the dedicated
// owner-only endpoint that owns the subdomain column, separate from the
// name/icon PUT so neither clobbers the other. Validates the label (DNS shape
// + reserved-name check), then upserts; a label already claimed by another org
// surfaces as 409 so the UI can say "taken" rather than 500. An empty label
// clears the subdomain.
func (h *HTTPGateway) putOrgSubdomain(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil {
		writeJSONError(rw, http.StatusNotImplemented, "org profiles not configured")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	if h.WildcardDomain == "" {
		writeAPIError(rw, http.StatusNotImplemented, "subdomains_disabled",
			"this deployment doesn't have per-org subdomains enabled")
		return
	}
	body, ok := decodeRequestJSON[struct {
		Subdomain string `json:"subdomain"`
	}](rw, r)
	if !ok {
		return
	}
	label, err := auth.ValidateSubdomain(body.Subdomain)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_subdomain",
			"a subdomain may only use lowercase letters, numbers and hyphens (and can't be a reserved name)")
		return
	}
	// Load-merge-write so the name/icon already on the profile survive.
	pr := auth.OrgProfile{Tenant: p.Tenant, UpdatedAt: time.Now().UTC()}
	if existing, gerr := h.Profiles.GetOrgProfile(r.Context(), p.Tenant); gerr == nil {
		pr.DisplayName = existing.DisplayName
		pr.Icon = existing.Icon
	}
	pr.Subdomain = label
	if err := h.Profiles.PutOrgProfile(r.Context(), pr); err != nil {
		if errors.Is(err, auth.ErrSubdomainTaken) {
			writeAPIError(rw, http.StatusConflict, "subdomain_taken",
				"that subdomain is already taken — try another")
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "org_subdomain.update", p.Tenant, "subdomain="+label)
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":          pr.Tenant,
		"subdomain":       pr.Subdomain,
		"wildcard_domain": h.WildcardDomain,
	})
}

// orgSubdomainAvailable is the owner-only pre-check the editor calls as the
// user types, so they learn a label is taken/invalid before saving. Returns
// {available, reason}. The caller's OWN current label reads as available (so
// re-saving an unchanged value isn't flagged as a conflict).
func (h *HTTPGateway) orgSubdomainAvailable(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.Profiles == nil || h.WildcardDomain == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"available": false, "reason": "disabled"})
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	label, err := auth.ValidateSubdomain(r.URL.Query().Get("label"))
	if err != nil || label == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"available": false, "reason": "invalid"})
		return
	}
	owner, err := h.Profiles.GetOrgProfileBySubdomain(r.Context(), label)
	if err != nil {
		// No org holds it → free.
		writeJSON(rw, http.StatusOK, map[string]any{"available": true})
		return
	}
	if owner.Tenant == p.Tenant {
		writeJSON(rw, http.StatusOK, map[string]any{"available": true, "reason": "current"})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"available": false, "reason": "taken"})
}

// resolveSubdomain is the PUBLIC (pre-auth) lookup the sign-in page uses to map
// a wildcard host label ("klahr" from klahr.dazyflow.app) back to the org's
// real tenant ID, so the SSO probe + Google start target the right org. Only
// the tenant + display name are returned (both already public on the sign-in
// surface); 404 when the label isn't claimed. No auth: a subdomain is public
// by nature, and this leaks nothing a visit to the host wouldn't.
func (h *HTTPGateway) resolveSubdomain(rw http.ResponseWriter, r *http.Request) {
	if h.Profiles == nil || h.WildcardDomain == "" {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such organization")
		return
	}
	label, err := auth.ValidateSubdomain(r.URL.Query().Get("label"))
	if err != nil || label == "" {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such organization")
		return
	}
	pr, err := h.Profiles.GetOrgProfileBySubdomain(r.Context(), label)
	if err != nil {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such organization")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"tenant":       pr.Tenant,
		"display_name": pr.DisplayName,
		// The org logo (data: URL or glyph name), so the sign-in page can
		// brand itself for the org behind the subdomain. Already public.
		"icon": pr.Icon,
	})
}

// tlsAllow is the Caddy on-demand-TLS authorization endpoint ("ask"): Caddy
// calls GET /api/v1/auth/tls-allow?domain=<host> before issuing a certificate
// for a wildcard-subdomain host, and only issues when this returns 2xx. We
// allow exactly the org subdomains that have been CLAIMED — so an attacker
// pointing arbitrary <random>.<apex> hosts at our IP can't make us mint certs
// (Let's Encrypt rate-limit abuse) for hosts that map to no org. The apex
// itself is served by its own managed-cert site block and never reaches here.
func (h *HTTPGateway) tlsAllow(rw http.ResponseWriter, r *http.Request) {
	if h.Profiles == nil || h.WildcardDomain == "" {
		http.Error(rw, "subdomains disabled", http.StatusForbidden)
		return
	}
	host := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
	suffix := "." + strings.ToLower(h.WildcardDomain)
	if host == "" || !strings.HasSuffix(host, suffix) {
		http.Error(rw, "not a managed host", http.StatusForbidden)
		return
	}
	rawLabel := strings.TrimSuffix(host, suffix)
	// Infrastructure hosts we serve (e.g. docs) are reserved — they never map to
	// an org, so the claimed-org check below would reject them — but we DO front
	// them and they need a cert. Authorize those explicitly. (They can't be
	// abused for rate-limit spam: it's a fixed, closed allowlist of our own
	// hosts, not attacker-controlled.)
	if auth.IsServedInfraSubdomain(rawLabel) {
		rw.WriteHeader(http.StatusOK)
		return
	}
	label, err := auth.ValidateSubdomain(rawLabel)
	if err != nil || label == "" {
		http.Error(rw, "invalid label", http.StatusForbidden)
		return
	}
	if _, err := h.Profiles.GetOrgProfileBySubdomain(r.Context(), label); err != nil {
		http.Error(rw, "no such organization", http.StatusForbidden)
		return
	}
	rw.WriteHeader(http.StatusOK) // claimed → Caddy may issue the cert
}
