// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

type oauthAPI struct {
	auditor
	svc              *Service
	OAuth            *OAuthRegistry
	EncryptedSecrets *EncryptedSecrets
	WildcardDomain   string
}

func (h *HTTPGateway) oauthAPI() *oauthAPI {
	return &oauthAPI{auditor: h.auditor(), svc: h.svc, OAuth: h.OAuth,
		EncryptedSecrets: h.EncryptedSecrets, WildcardDomain: h.WildcardDomain}
}

// Two endpoints: one mints the authorize URL, one receives the provider's
// redirect. Both are reachable by a browser, so both assume a hostile caller.

func (h *oauthAPI) oauthAuthorize(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	provider := r.PathValue("provider")
	// Bind the flow to THIS browser: the state token alone only proves some browser
	// started one.
	binding, err := newOAuthBinding()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "mint state binding: "+err.Error())
		return
	}
	target, status, msg := h.buildAuthorizeURL(p,
		provider,
		r.URL.Query().Get("account"),
		r.URL.Query().Get("return_to"),
		scopeSubsetForIntegration(provider, r.URL.Query().Get("integration")),
		binding,
		h.originHost(r),
	)
	if status != http.StatusOK {
		writeJSONError(rw, status, msg)
		return
	}
	h.setOAuthStateCookie(rw, r, binding)
	http.Redirect(rw, r, target, http.StatusFound)
}

// Only when it is this deployment's own host; anything else is not trusted.
func (h *oauthAPI) originHost(r *http.Request) string {
	if h.WildcardDomain == "" {
		return ""
	}
	bare := strings.ToLower(bareHost(r.Host))
	if bare == h.WildcardDomain || hostIsSubdomainOf(bare, h.WildcardDomain) {
		return r.Host
	}
	return ""
}

const oauthStateCookie = "dz_oauth_state"

func newOAuthBinding() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SameSite=Lax so it survives the provider's cross-site redirect back.
func (h *oauthAPI) setOAuthStateCookie(rw http.ResponseWriter, r *http.Request, binding string) {
	http.SetCookie(rw, &http.Cookie{
		Name:  oauthStateCookie,
		Value: binding,
		Path:  "/api/v1/oauth",
		// Scoped to the apex, or a subdomain callback cannot read it.
		Domain:   h.cookieDomain(r),
		MaxAge:   int(h.OAuth.state.ttl / time.Second),
		HttpOnly: true,
		Secure:   strings.HasPrefix(h.svc.PublicBaseURL, "https"),
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *oauthAPI) cookieDomain(r *http.Request) string {
	if r == nil || h.originHost(r) == "" {
		return ""
	}
	return h.WildcardDomain
}

func (h *oauthAPI) clearOAuthStateCookie(rw http.ResponseWriter, r *http.Request) {
	http.SetCookie(rw, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/api/v1/oauth",
		Domain:   h.cookieDomain(r),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   strings.HasPrefix(h.svc.PublicBaseURL, "https"),
		SameSite: http.SameSiteLaxMode,
	})
}

// Shared by both entry points, so the state and binding cannot diverge.
func (h *oauthAPI) buildAuthorizeURL(p core.Principal, providerName, account, returnTo string, scopes []string, binding, host string) (string, int, string) {
	if h.OAuth == nil {
		return "", http.StatusNotImplemented, "OAuth not configured"
	}
	if p.Tenant == "" {
		return "", http.StatusForbidden, "principal has no tenant"
	}
	// Connecting WRITES a token, so it is gated on secret:write.
	if providerName == "google" {
		if !core.CanAdminOrg(p) {
			return "", http.StatusForbidden, "connecting a Google account requires organization:admin"
		}
	} else if err := core.Require(p, core.PermSecretWrite); err != nil {
		return "", http.StatusForbidden, err.Error()
	}
	prov, ok := h.OAuth.Provider(providerName)
	if !ok {
		return "", http.StatusNotFound, fmt.Sprintf("unknown OAuth provider %q", providerName)
	}
	if prov.ClientID == "" || prov.ClientSecret == "" {
		return "", http.StatusServiceUnavailable,
			fmt.Sprintf("provider %q is not configured (missing client_id/secret)", providerName)
	}
	if account == "" {
		account = "default"
	}
	if err := core.ValidSecretName("oauth." + providerName + "." + account); err != nil {
		return "", http.StatusBadRequest, fmt.Sprintf("account %q: %v", account, err)
	}
	if returnTo == "" {
		returnTo = "/apps"
	}
	// Rejects protocol-relative paths too: "//evil.com" is not same-origin.
	if !safeReturnPath(returnTo) {
		return "", http.StatusBadRequest, "return_to must be a relative path starting with /"
	}
	state, err := h.OAuth.state.mint(pendingOAuth{
		tenant:   p.Tenant,
		provider: providerName,
		account:  account,
		returnTo: returnTo,
		binding:  binding,
		host:     host,
	})
	if err != nil {
		return "", http.StatusInternalServerError, fmt.Sprintf("mint state: %v", err)
	}
	q := url.Values{}
	q.Set("client_id", prov.ClientID)
	q.Set("redirect_uri", h.OAuth.redirectURI(providerName))
	q.Set("response_type", "code")
	q.Set("state", state)
	reqScopes := prov.Scopes
	if len(scopes) > 0 {
		reqScopes = scopes // incremental: only this integration's scopes
	}
	if len(reqScopes) > 0 {
		q.Set("scope", strings.Join(reqScopes, " "))
	}
	for k, v := range prov.AuthorizeExtras {
		q.Set(k, v)
	}
	target := prov.AuthorizeURL
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	return target + sep + q.Encode(), http.StatusOK, ""
}

func (h *oauthAPI) oauthCallback(rw http.ResponseWriter, r *http.Request) {
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth not configured")
		return
	}
	providerName := r.PathValue("provider")
	prov, ok := h.OAuth.Provider(providerName)
	if !ok {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("unknown OAuth provider %q", providerName))
		return
	}

	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")
	providerErr := q.Get("error")

	pending, ok := h.OAuth.state.consume(state)
	if !ok {
		// Replay, expiry, or forgery: all answered the same way.
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired OAuth state")
		return
	}
	if pending.provider != providerName {
		writeJSONError(rw, http.StatusBadRequest, "state/provider mismatch")
		return
	}
	// A flow started via the redirect path must finish in the same browser.
	if pending.binding != "" {
		c, cerr := r.Cookie(oauthStateCookie)
		if cerr != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(pending.binding)) != 1 {
			h.clearOAuthStateCookie(rw, r)
			writeJSONError(rw, http.StatusBadRequest, "OAuth state did not match this browser session")
			return
		}
		h.clearOAuthStateCookie(rw, r)
	}

	if providerErr != "" {
		h.redirectBack(rw, r, pending, providerName, "error", "provider returned error: "+providerErr)
		return
	}
	if code == "" {
		h.redirectBack(rw, r, pending, providerName, "error", "provider returned no code")
		return
	}

	tok, err := h.OAuth.exchangeCode(r.Context(), prov, code)
	if err != nil {
		h.redirectBack(rw, r, pending, providerName, "error", "exchange: "+err.Error())
		return
	}
	if _, err := h.OAuth.store(r.Context(), pending.tenant, providerName, pending.account, tok); err != nil {
		h.redirectBack(rw, r, pending, providerName, "error", "store: "+err.Error())
		return
	}
	h.redirectBack(rw, r, pending, providerName, "success", "")
}

// On the host the flow STARTED on, so a subdomain user lands back there.
func (h *oauthAPI) redirectBack(rw http.ResponseWriter, r *http.Request, pending pendingOAuth, providerName, status, errMsg string) {
	target := pending.host
	if target != "" {
		bare := strings.ToLower(bareHost(target))
		sameHost := strings.EqualFold(target, r.Host)
		valid := h.WildcardDomain != "" &&
			(bare == h.WildcardDomain || hostIsSubdomainOf(bare, h.WildcardDomain))
		if sameHost || !valid {
			target = "" // already there, or not ours — stay path-relative
		}
	}
	scheme := "https"
	if !strings.HasPrefix(h.svc.PublicBaseURL, "https") {
		scheme = "http"
	}
	redirectWithStatus(rw, r, pending.returnTo, providerName, pending.account, status, errMsg, scheme, target)
}

func redirectWithStatus(rw http.ResponseWriter, r *http.Request, returnTo, provider, account, status, errMsg, scheme, host string) {
	// Already validated at mint time; re-checked because this is the redirect.
	if !safeReturnPath(returnTo) {
		returnTo = "/apps"
	}
	u, err := url.Parse(returnTo)
	if err != nil {
		http.Error(rw, "invalid return_to", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("oauth", status)
	q.Set("provider", provider)
	q.Set("account", account)
	if errMsg != "" {
		if len(errMsg) > 256 {
			errMsg = errMsg[:256] + "…"
		}
		q.Set("error", errMsg)
	}
	u.RawQuery = q.Encode()
	if host != "" {
		u.Scheme = scheme
		u.Host = host
	}
	http.Redirect(rw, r, u.String(), http.StatusFound)
}

func (h *oauthAPI) oauthListProviders(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	names := h.OAuth.Providers()

	connected := h.connectedAccountsByProvider(r.Context(), p.Tenant)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		row := map[string]any{
			"name":     n,
			"accounts": connected[n], // empty slice = not connected
		}
		// A stored token whose granted scope no longer covers what the provider needs.
		if provider, ok := h.OAuth.Provider(n); ok && len(provider.Scopes) > 0 && !providerUsesIncrementalScopes(n) {
			stale := h.staleAccounts(r.Context(), p.Tenant, n, connected[n], provider.Scopes)
			if len(stale) > 0 {
				row["stale_accounts"] = stale
			}
		}
		// Checked here so the user is told before a run fails.
		h.OAuth.RefreshStaleAccounts(r.Context(), p.Tenant, n, connected[n])
		if dead := h.OAuth.ReconnectNeeded(r.Context(), p.Tenant, n, connected[n]); len(dead) > 0 {
			row["needs_reconnect"] = dead
		}
		out = append(out, row)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"providers": out})
}

func (h *oauthAPI) connectedAccounts(ctx context.Context, tenant, provider string) []string {
	out := h.connectedAccountsByProvider(ctx, tenant)[provider]
	if out == nil {
		out = make([]string, 0)
	}
	sort.Strings(out)
	return out
}

func (h *oauthAPI) connectedAccountsByProvider(ctx context.Context, tenant string) map[string][]string {
	out := map[string][]string{}
	if h.EncryptedSecrets == nil {
		return out
	}
	all, err := h.EncryptedSecrets.List(ctx, tenant)
	if err != nil {
		return out
	}
	const pfx = "oauth."
	for _, name := range all {
		if !strings.HasPrefix(name, pfx) {
			continue
		}
		rest := name[len(pfx):]
		dot := strings.Index(rest, ".")
		if dot < 0 {
			continue
		}
		prov, account := rest[:dot], rest[dot+1:]
		out[prov] = append(out[prov], account)
	}
	return out
}

func (h *oauthAPI) oauthListAccounts(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OAuth == nil {
		writeAPIError(rw, http.StatusNotImplemented, "oauth_not_configured", "OAuth not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "principal has no tenant")
		return
	}
	if !requireOrgAdmin(rw, p) {
		return
	}
	provider := r.PathValue("provider")
	if _, ok := h.OAuth.Provider(provider); !ok {
		writeAPIError(rw, http.StatusNotFound, "provider_not_found",
			fmt.Sprintf("unknown OAuth provider %q", provider))
		return
	}

	groups := scopeGroupsForProvider(provider)
	services := make([]string, 0, len(groups))
	for svc := range groups {
		services = append(services, svc)
	}
	sort.Strings(services)

	ctx := r.Context()
	accounts := make([]map[string]any, 0)
	for _, account := range h.connectedAccounts(ctx, p.Tenant, provider) {
		tok, err := h.OAuth.GetOAuthToken(core.WithTenant(ctx, p.Tenant), provider, account)
		var grantedSet map[string]struct{}
		scopeList := make([]string, 0)
		if err == nil && tok != nil {
			grantedSet = splitScopes(tok.Scope)
			for s := range grantedSet {
				scopeList = append(scopeList, s)
			}
			sort.Strings(scopeList)
		}
		coverage := make(map[string]bool, len(services))
		for _, svc := range services {
			// An empty grant covers nothing, so it must not read as "covers everything".
			coverage[svc] = grantedSet != nil && grantedCovers(grantedSet, groups[svc])
		}
		accounts = append(accounts, map[string]any{
			"account":  account,
			"coverage": coverage,
			"scopes":   scopeList,
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{
		"provider": provider,
		"services": services,
		"accounts": accounts,
	})
}

func (h *oauthAPI) staleAccounts(ctx context.Context, tenant, provider string, accounts, required []string) []string {
	if h.OAuth == nil || tenant == "" {
		return nil
	}
	stale := make([]string, 0)
	for _, account := range accounts {
		tok, err := h.OAuth.GetOAuthToken(core.WithTenant(ctx, tenant), provider, account)
		if err != nil || tok == nil || tok.Scope == "" {
			continue
		}
		granted := splitScopes(tok.Scope)
		if !grantedCovers(granted, required) {
			stale = append(stale, account)
		}
	}
	return stale
}

// Providers disagree on the separator, so accept both.
func splitScopes(s string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n'
	}) {
		if raw == "" {
			continue
		}
		set[strings.ToLower(raw)] = struct{}{}
	}
	return set
}

func grantedCovers(granted map[string]struct{}, required []string) bool {
	for _, req := range required {
		if _, ok := granted[strings.ToLower(req)]; !ok {
			return false
		}
	}
	return true
}
