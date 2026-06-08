package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// HTTP surface for the OAuth flow. Two endpoints:
//
//	GET /api/v1/oauth/{provider}/authorize?account=NAME&return_to=/...
//	    Auth-required. Mints state, 302s to provider.
//
//	GET /api/v1/oauth/{provider}/callback?code=...&state=...
//	    UN-authenticated. State token is the only thing that ties
//	    the callback back to the authorizing principal.
//
// The callback is unauthenticated because the OAuth provider
// redirects the user's browser without a Bearer token. Security
// rests on the state token being unguessable (256 bits of entropy)
// and single-use (consumed on callback, expires in 10 min).
//
// On successful callback the handler 302s the user back to
// `return_to` with `?oauth=success&provider=...&account=...`. On
// failure: `?oauth=error&provider=...&error=...`. Lets the UI
// show a toast without an extra round-trip.

// oauthAuthorize starts the flow. Requires bearer auth so we know
// which tenant the resulting token belongs to.
func (h *HTTPGateway) oauthAuthorize(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	provider := r.PathValue("provider")
	target, status, msg := h.buildAuthorizeURL(p,
		provider,
		r.URL.Query().Get("account"),
		r.URL.Query().Get("return_to"),
		// ?integration=<label> requests only that service's scopes
		// (incremental authorization); empty/unknown → the provider's full
		// scope set, unchanged.
		scopeSubsetForIntegration(provider, r.URL.Query().Get("integration")),
	)
	if status != http.StatusOK {
		writeJSONError(rw, status, msg)
		return
	}
	http.Redirect(rw, r, target, http.StatusFound)
}

// buildAuthorizeURL is the shared path between the legacy redirect
// endpoint and the new /me/connections/{provider}/authorize JSON
// endpoint. Returns the provider's full authorize URL on success;
// (HTTP status, error message) when the request is malformed or
// OAuth is unconfigured.
//
// Pulled out so the JSON variant doesn't have to fake an http.Redirect
// just to capture the target — and so the URL-building logic stays
// single-source as more providers / extras are added.
// scopes, when non-empty, overrides the provider's full scope list — used
// for incremental authorization (request only one integration's scopes).
// nil/empty falls back to the provider's complete Scopes.
func (h *HTTPGateway) buildAuthorizeURL(p core.Principal, providerName, account, returnTo string, scopes []string) (string, int, string) {
	if h.OAuth == nil {
		return "", http.StatusNotImplemented, "OAuth not configured"
	}
	if p.Tenant == "" {
		return "", http.StatusForbidden, "principal has no tenant"
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		// OAuth ends up WRITING a token to the secret store; gate on
		// the same permission that gates direct secret writes.
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
	if err := validSecretName("oauth." + providerName + "." + account); err != nil {
		return "", http.StatusBadRequest, fmt.Sprintf("account %q: %v", account, err)
	}
	if returnTo == "" {
		returnTo = "/apps"
	}
	if !strings.HasPrefix(returnTo, "/") {
		return "", http.StatusBadRequest, "return_to must be a relative path starting with /"
	}
	state, err := h.OAuth.state.mint(pendingOAuth{
		tenant:   p.Tenant,
		provider: providerName,
		account:  account,
		returnTo: returnTo,
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

// oauthCallback receives the provider's redirect, exchanges the
// code for tokens, stores them, and 302s the user back to
// `return_to`. No auth — state token is the only credential.
func (h *HTTPGateway) oauthCallback(rw http.ResponseWriter, r *http.Request) {
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
		// No matching pending state — either replay, expiry, or a
		// stray request. We don't know where to send the user, so
		// return a plain 400 page. The UI catches users on the same
		// origin so a redirect-to-error isn't worth the extra hop.
		writeJSONError(rw, http.StatusBadRequest, "invalid or expired OAuth state")
		return
	}
	if pending.provider != providerName {
		// Defensive: state token bound to a different provider — should
		// be impossible if mint/consume are matched, but if it ever
		// happens (proxy weirdness?) we'd rather fail loudly.
		writeJSONError(rw, http.StatusBadRequest, "state/provider mismatch")
		return
	}

	if providerErr != "" {
		// User declined consent, scope refused, etc. Bounce back to
		// the UI with the error in the query so the UI can show a
		// toast.
		redirectWithStatus(rw, r, pending.returnTo, providerName, pending.account, "error",
			"provider returned error: "+providerErr)
		return
	}
	if code == "" {
		redirectWithStatus(rw, r, pending.returnTo, providerName, pending.account, "error",
			"provider returned no code")
		return
	}

	tok, err := h.OAuth.exchangeCode(r.Context(), prov, code)
	if err != nil {
		redirectWithStatus(rw, r, pending.returnTo, providerName, pending.account, "error",
			"exchange: "+err.Error())
		return
	}
	if _, err := h.OAuth.store(r.Context(), pending.tenant, providerName, pending.account, tok); err != nil {
		redirectWithStatus(rw, r, pending.returnTo, providerName, pending.account, "error",
			"store: "+err.Error())
		return
	}
	redirectWithStatus(rw, r, pending.returnTo, providerName, pending.account, "success", "")
}

// redirectWithStatus 302s the user back to return_to with
// `?oauth=success|error&provider=…&account=…[&error=…]` so the UI
// can render a toast without polling.
func redirectWithStatus(rw http.ResponseWriter, r *http.Request, returnTo, provider, account, status, errMsg string) {
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
		// Truncate so a long error message doesn't blow up the URL.
		if len(errMsg) > 256 {
			errMsg = errMsg[:256] + "…"
		}
		q.Set("error", errMsg)
	}
	u.RawQuery = q.Encode()
	http.Redirect(rw, r, u.String(), http.StatusFound)
}

// oauthListProviders is the UI's hook for "what can I connect to?".
// Returns the registered provider names plus, for each, whether
// the tenant currently has a stored token under any account.
func (h *HTTPGateway) oauthListProviders(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	names := h.OAuth.Providers()

	// For each provider, find any stored accounts by listing the
	// tenant's secrets and matching the `oauth.<provider>.` prefix.
	// One List() call rather than N — the typical tenant has a small
	// number of secrets.
	connected := map[string][]string{}
	if h.EncryptedSecrets != nil {
		all, err := h.EncryptedSecrets.List(r.Context(), p.Tenant)
		if err == nil {
			for _, name := range all {
				const pfx = "oauth."
				if !strings.HasPrefix(name, pfx) {
					continue
				}
				rest := name[len(pfx):]
				dot := strings.Index(rest, ".")
				if dot < 0 {
					continue
				}
				prov, account := rest[:dot], rest[dot+1:]
				connected[prov] = append(connected[prov], account)
			}
		}
	}
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		row := map[string]any{
			"name":     n,
			"accounts": connected[n], // empty slice = not connected
		}
		// stale_accounts: accounts whose stored token's granted scope
		// no longer covers what the current provider config requires
		// (typical cause: we added a new scope after the user
		// connected, so the access token can't call the new endpoint).
		// The frontend renders a "Reconnect required" pill on these.
		// Skip the all-scopes staleness check for providers that authorize
		// incrementally (Google): an account connected for one service
		// legitimately lacks the others' scopes, so comparing against the
		// full set would flag every account as needing reconnection. Those
		// scopes are topped up per-integration at connect time instead.
		if provider, ok := h.OAuth.Provider(n); ok && len(provider.Scopes) > 0 && !providerUsesIncrementalScopes(n) {
			stale := h.staleAccounts(r.Context(), p.Tenant, n, connected[n], provider.Scopes)
			if len(stale) > 0 {
				row["stale_accounts"] = stale
			}
		}
		out = append(out, row)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"providers": out})
}

// staleAccounts compares each connected account's stored token scope
// against the current required scope set and returns the names whose
// grant is missing at least one required scope. A token whose scope
// field is empty (some providers don't echo it on success) is treated
// as fresh — we have no signal to declare it stale and false positives
// would push users into a needless reauthorize loop.
func (h *HTTPGateway) staleAccounts(ctx context.Context, tenant, provider string, accounts, required []string) []string {
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

// splitScopes accepts the wire formats different providers use —
// Google uses spaces, GitHub and Slack use commas, and a few clients
// mix both. Splitting on either gives a tolerant set; lowercase
// because scope strings are case-insensitive in practice (Google
// returns the canonical URL form, which is mixed-case but identity-
// compared in real callers).
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

// grantedCovers reports whether every required scope is present in
// granted. A missing scope (in granted) is the signal — extras in
// granted are fine.
func grantedCovers(granted map[string]struct{}, required []string) bool {
	for _, req := range required {
		if _, ok := granted[strings.ToLower(req)]; !ok {
			return false
		}
	}
	return true
}
