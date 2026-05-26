package daemon

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
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
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		// OAuth ends up WRITING a token to the secret store; gate on
		// the same permission that gates direct secret writes.
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	providerName := r.PathValue("provider")
	prov, ok := h.OAuth.providers[providerName]
	if !ok {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("unknown OAuth provider %q", providerName))
		return
	}
	if prov.ClientID == "" || prov.ClientSecret == "" {
		writeJSONError(rw, http.StatusServiceUnavailable,
			fmt.Sprintf("provider %q is not configured (missing client_id/secret)", providerName))
		return
	}

	account := r.URL.Query().Get("account")
	if account == "" {
		account = "default"
	}
	if err := validSecretName("oauth." + providerName + "." + account); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("account %q: %v", account, err))
		return
	}

	// Default returnTo to a UI route that the gateway's caller
	// usually serves; if the caller didn't pass one, "/" is a
	// safe-enough fallback.
	returnTo := r.URL.Query().Get("return_to")
	if returnTo == "" {
		returnTo = "/integrations"
	}
	// Same-origin only, to stop open-redirect attacks.
	if !strings.HasPrefix(returnTo, "/") {
		writeJSONError(rw, http.StatusBadRequest, "return_to must be a relative path starting with /")
		return
	}

	state, err := h.OAuth.state.mint(pendingOAuth{
		tenant:   p.Tenant,
		provider: providerName,
		account:  account,
		returnTo: returnTo,
	})
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("mint state: %v", err))
		return
	}

	q := url.Values{}
	q.Set("client_id", prov.ClientID)
	q.Set("redirect_uri", h.OAuth.redirectURI(providerName))
	q.Set("response_type", "code")
	q.Set("state", state)
	if len(prov.Scopes) > 0 {
		// Standard OAuth scope separator is space; some providers
		// (Slack) want comma. Provider configs spell which they
		// want by setting Scopes accordingly — the daemon doesn't
		// re-split. We join with space here; a Slack-specific
		// provider config would join its own scope string ahead
		// of time.
		q.Set("scope", strings.Join(prov.Scopes, " "))
	}
	// Provider-specific extras (Google's access_type=offline +
	// prompt=consent, etc.). Applied last so they can override the
	// standard params if a provider really insists.
	for k, v := range prov.AuthorizeExtras {
		q.Set(k, v)
	}

	target := prov.AuthorizeURL
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	http.Redirect(rw, r, target+sep+q.Encode(), http.StatusFound)
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
	prov, ok := h.OAuth.providers[providerName]
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
		out = append(out, map[string]any{
			"name":     n,
			"accounts": connected[n], // empty slice = not connected
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"providers": out})
}
